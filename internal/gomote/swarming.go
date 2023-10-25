// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package gomote

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"go.chromium.org/luci/swarming/client/swarming"
	swarmpb "go.chromium.org/luci/swarming/proto/api_v2"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/rendezvous"
	"golang.org/x/build/internal/swarmclient"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type rendezvousClient interface {
	DeregisterInstance(ctx context.Context, id string)
	HandleReverse(w http.ResponseWriter, r *http.Request)
	RegisterInstance(ctx context.Context, id string, wait time.Duration)
	WaitForInstance(ctx context.Context, id string) (buildlet.Client, error)
}

// SwarmingServer is a gomote server implementation which supports LUCI swarming bots.
type SwarmingServer struct {
	// embed the unimplemented server.
	protos.UnimplementedGomoteServiceServer

	bucket                  bucketHandle
	buildlets               *remote.SessionPool
	gceBucketName           string
	luciConfigClient        *swarmclient.ConfigClient
	rendezvous              rendezvousClient
	sshCertificateAuthority ssh.Signer
	swarmingClient          swarming.Client
}

// NewSwarming creates a gomote server. If the rawCAPriKey is invalid, the program will exit.
func NewSwarming(rsp *remote.SessionPool, rawCAPriKey []byte, gomoteGCSBucket string, storageClient *storage.Client, configClient *swarmclient.ConfigClient, rdv *rendezvous.Rendezvous, swarmClient swarming.Client) (*SwarmingServer, error) {
	signer, err := ssh.ParsePrivateKey(rawCAPriKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse raw certificate authority private key into signer=%w", err)
	}
	return &SwarmingServer{
		bucket:                  storageClient.Bucket(gomoteGCSBucket),
		buildlets:               rsp,
		gceBucketName:           gomoteGCSBucket,
		luciConfigClient:        configClient,
		rendezvous:              rdv,
		sshCertificateAuthority: signer,
		swarmingClient:          swarmClient,
	}, nil
}

// CreateInstance will create a gomote instance within a swarming task for the authenticated user.
func (ss *SwarmingServer) CreateInstance(req *protos.CreateInstanceRequest, stream protos.GomoteService_CreateInstanceServer) error {
	creds, err := access.IAPFromContext(stream.Context())
	if err != nil {
		log.Printf("CreateInstance access.IAPFromContext(ctx) = nil, %s", err)
		return status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetBuilderType() == "" {
		return status.Errorf(codes.InvalidArgument, "invalid builder type")
	}
	bots, err := ss.luciConfigClient.ListSwarmingBots(stream.Context())
	if err != nil {
		log.Printf("luciConfigClient.ListSwarmingBots(ctx) = %s", err)
		return err
	}
	var botDesc *swarmclient.SwarmingBot
	for _, bot := range bots {
		if bot.BucketName == "ci" && req.GetBuilderType() == bot.Name {
			botDesc = bot
			break
		}
	}
	if botDesc == nil {
		return status.Errorf(codes.InvalidArgument, "unknown builder type")
	}
	userName, err := emailToUser(creds.Email)
	if err != nil {
		return status.Errorf(codes.Internal, "invalid user email format")
	}
	type result struct {
		buildletClient buildlet.Client
		err            error
	}
	rc := make(chan result, 1)
	dimensions := make(map[string]string)
	for _, bd := range botDesc.Dimensions {
		k, v, ok := strings.Cut(bd, ":")
		if ok {
			dimensions[k] = v
		} else {
			log.Printf("failed dimension cut: %s", bd)
		}
	}
	name := fmt.Sprintf("gomote-%s-%s", userName, uuid.NewString())
	go func() {
		bc, err := ss.startNewSwarmingTask(stream.Context(), name, dimensions, &SwarmOpts{})
		if err != nil {
			log.Printf("startNewSwarmingTask() = %s", err)
		}
		rc <- result{bc, err}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return status.Errorf(codes.DeadlineExceeded, "timed out waiting for gomote instance to be created")
		case <-ticker.C:
			err := stream.Send(&protos.CreateInstanceResponse{
				Status:       protos.CreateInstanceResponse_WAITING,
				WaitersAhead: int64(0), // Not convinced querying for pending jobs is useful
			})
			if err != nil {
				return status.Errorf(codes.Internal, "unable to stream result: %s", err)
			}
		case r := <-rc:
			if r.err != nil {
				log.Printf("error creating gomote buildlet instance=%s: %s", name, r.err)
				return status.Errorf(codes.Internal, "gomote creation failed instance=%s", name)
			}
			gomoteID := ss.buildlets.AddSession(creds.ID, userName, req.GetBuilderType(), "swarming task", r.buildletClient)
			log.Printf("created buildlet %s for %s (%s)", gomoteID, userName, r.buildletClient.String())
			session, err := ss.buildlets.Session(gomoteID)
			if err != nil {
				return status.Errorf(codes.Internal, "unable to query for gomote timeout") // this should never happen
			}
			wd, err := r.buildletClient.WorkDir(stream.Context())
			if err != nil {
				return status.Errorf(codes.Internal, "could not read working dir: %v", err)
			}
			err = stream.Send(&protos.CreateInstanceResponse{
				Instance: &protos.Instance{
					GomoteId:    gomoteID,
					BuilderType: req.GetBuilderType(),
					HostType:    "swarming task",
					Expires:     session.Expires.Unix(),
					WorkingDir:  wd,
				},
				Status:       protos.CreateInstanceResponse_COMPLETE,
				WaitersAhead: 0,
			})
			if err != nil {
				return status.Errorf(codes.Internal, "unable to stream result: %s", err)
			}
			return nil
		}
	}
}

// DestroyInstance will destroy a gomote instance. It will ensure that the caller is authenticated and is the owner of the instance
// before it destroys the instance.
func (ss *SwarmingServer) DestroyInstance(ctx context.Context, req *protos.DestroyInstanceRequest) (*protos.DestroyInstanceResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("DestroyInstance access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid gomote ID")
	}
	_, err = ss.session(req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns a meaningful GRPC error.
		return nil, err
	}
	if err := ss.buildlets.DestroySession(req.GetGomoteId()); err != nil {
		log.Printf("DestroyInstance remote.DestroySession(%s) = %s", req.GetGomoteId(), err)
		return nil, status.Errorf(codes.Internal, "unable to destroy gomote instance")
	}
	// TODO(go.dev/issue/63819) consider destroying the bot after the task has ended.
	return &protos.DestroyInstanceResponse{}, nil
}

// ListSwarmingBuilders lists all of the swarming builders which run for gotip. The requester must be authenticated.
func (ss *SwarmingServer) ListSwarmingBuilders(ctx context.Context, req *protos.ListSwarmingBuildersRequest) (*protos.ListSwarmingBuildersResponse, error) {
	_, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("ListSwarmingInstances access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	bots, err := ss.luciConfigClient.ListSwarmingBots(ctx)
	if err != nil {
		log.Printf("luciConfigClient.ListSwarmingBots(ctx) = %s", err)
		return nil, status.Errorf(codes.Internal, "unable to query for bots")
	}
	var builders []string
	for _, bot := range bots {
		if bot.BucketName == "ci" && strings.HasPrefix(bot.Name, "gotip") {
			builders = append(builders, bot.Name)
		}
	}
	return &protos.ListSwarmingBuildersResponse{Builders: builders}, nil
}

// ListInstances will list the gomote instances owned by the requester. The requester must be authenticated.
func (ss *SwarmingServer) ListInstances(ctx context.Context, req *protos.ListInstancesRequest) (*protos.ListInstancesResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("ListInstances access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	res := &protos.ListInstancesResponse{}
	for _, s := range ss.buildlets.List() {
		if s.OwnerID != creds.ID {
			continue
		}
		res.Instances = append(res.Instances, &protos.Instance{
			GomoteId:    s.ID,
			BuilderType: s.BuilderType,
			HostType:    s.HostType,
			Expires:     s.Expires.Unix(),
		})
	}
	return res, nil
}

// session is a helper function that retrieves a session associated with the gomoteID and ownerID.
func (ss *SwarmingServer) session(gomoteID, ownerID string) (*remote.Session, error) {
	session, err := ss.buildlets.Session(gomoteID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "specified gomote instance does not exist")
	}
	if session.OwnerID != ownerID {
		return nil, status.Errorf(codes.PermissionDenied, "not allowed to modify this gomote session")
	}
	return session, nil
}

// SwarmOpts provides additional options for swarming task creation.
type SwarmOpts struct {
	// OnInstanceRequested optionally specifies a hook to run synchronously
	// after the computeService.Instances.Insert call, but before
	// waiting for its operation to proceed.
	OnInstanceRequested func()

	// OnInstanceCreated optionally specifies a hook to run synchronously
	// after the instance operation succeeds.
	OnInstanceCreated func()

	// OnInstanceRegistration optionally specifies a hook to run synchronously
	// after the instance has been registered in rendezvous.
	OnInstanceRegistration func()
}

// startNewSwarmingTask starts a new swarming task on a bot with the buildlet
// running on it. It returns a buildlet client configured to speak to it.
// The request will last as long as the lifetime of the context. The dimensions
// are a set of key value pairs used to describe what instance type to create.
func (ss *SwarmingServer) startNewSwarmingTask(ctx context.Context, name string, dimensions map[string]string, opts *SwarmOpts) (buildlet.Client, error) {
	ss.rendezvous.RegisterInstance(ctx, name, 10*time.Minute)
	condRun(opts.OnInstanceRegistration)

	taskID, err := ss.newSwarmingTask(ctx, name, dimensions, opts)
	if err != nil {
		ss.rendezvous.DeregisterInstance(ctx, name)
		return nil, err
	}
	log.Printf("gomote: swarming task requested name=%s taskID=%s", name, taskID)
	condRun(opts.OnInstanceRequested)

	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var taskUp bool
	tryThenPeriodicallyDo(queryCtx, 5*time.Second, func(ctx context.Context, _ time.Time) {
		resp, err := ss.swarmingClient.TaskResult(ctx, taskID, &swarming.TaskResultFields{WithPerf: false})
		if err != nil {
			log.Printf("gomote: unable to query for swarming task state: name=%s taskID=%s %s", name, taskID, err)
			return
		}
		switch taskState := resp.GetState(); taskState {
		case swarmpb.TaskState_COMPLETED, swarmpb.TaskState_RUNNING:
			taskUp = true
			cancel()
		case swarmpb.TaskState_EXPIRED, swarmpb.TaskState_INVALID, swarmpb.TaskState_BOT_DIED, swarmpb.TaskState_CANCELED,
			swarmpb.TaskState_CLIENT_ERROR, swarmpb.TaskState_KILLED, swarmpb.TaskState_NO_RESOURCE, swarmpb.TaskState_TIMED_OUT:
			log.Printf("gomote: swarming task creation failed name=%s state=%s", name, taskState)
			cancel()
		case swarmpb.TaskState_PENDING:
			// continue waiting
		default:
			log.Printf("gomote: unexpected swarming task state for %s: %s", name, taskState)
		}
	})
	if !taskUp {
		ss.rendezvous.DeregisterInstance(ctx, name)
		return nil, fmt.Errorf("unable to create swarming task name=%s taskID=%s", name, taskID)
	}
	condRun(opts.OnInstanceCreated)

	bc, err := ss.waitForInstanceOrFailure(ctx, taskID, name)
	if err != nil {
		ss.rendezvous.DeregisterInstance(ctx, name)
		return nil, err
	}
	return bc, nil
}

// waitForInstanceOrFailure waits for either the swarming task to enter a failed state or the successful connection from
// a buildlet client.
func (ss *SwarmingServer) waitForInstanceOrFailure(ctx context.Context, taskID, name string) (buildlet.Client, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)

	checkForTaskFailure := func(pollCtx context.Context) <-chan error {
		errCh := make(chan error, 1)
		go func() {
			internal.PeriodicallyDo(pollCtx, 10*time.Second, func(ctx context.Context, _ time.Time) {
				resp, err := ss.swarmingClient.TaskResult(ctx, taskID, &swarming.TaskResultFields{WithPerf: false})
				if err != nil {
					log.Printf("gomote: unable to query for swarming task state: name=%s taskID=%s %s", name, taskID, err)
					return
				}
				switch taskState := resp.GetState(); taskState {
				case swarmpb.TaskState_RUNNING:
					// expected
				case swarmpb.TaskState_EXPIRED, swarmpb.TaskState_INVALID, swarmpb.TaskState_BOT_DIED, swarmpb.TaskState_CANCELED,
					swarmpb.TaskState_CLIENT_ERROR, swarmpb.TaskState_KILLED, swarmpb.TaskState_NO_RESOURCE, swarmpb.TaskState_TIMED_OUT, swarmpb.TaskState_COMPLETED:
					errCh <- fmt.Errorf("swarming task creation failed name=%s state=%s", name, taskState)
				default:
					log.Printf("gomote: unexpected swarming task state for %s: %s", name, taskState)
				}
			})
		}()
		return errCh
	}

	type result struct {
		err error
		bc  buildlet.Client
	}

	getConn := func(waitCtx context.Context) <-chan *result {
		ch := make(chan *result, 1)
		go func() {
			bc, err := ss.rendezvous.WaitForInstance(waitCtx, name)
			if err != nil {
				ss.rendezvous.DeregisterInstance(ctx, name)
			}
			ch <- &result{err: err, bc: bc}
		}()
		return ch
	}

	statusChan := checkForTaskFailure(queryCtx)
	resChan := getConn(queryCtx)

	select {
	case err := <-statusChan:
		cancel()
		ss.rendezvous.DeregisterInstance(ctx, name)
		log.Printf("gomote: failed waiting for task to run %q: %s", name, err)
		return nil, err
	case r := <-resChan:
		cancel()
		if r.err != nil {
			log.Printf("gomote: failed to establish connection %q: %s", name, r.err)
			return nil, r.err
		}
		return r.bc, r.err
	}
}

func buildletStartup(goos, goarch string) string {
	return fmt.Sprintf(`
export GO_TASK_ROOT=$(pwd) &&
export GO_BIN=$GO_TASK_ROOT/bin &&
mkdir $GO_BIN &&
export PATH=$PATH:$GO_BIN &&
curl -s https://storage.googleapis.com/go-builder-data/buildlet.%s-%s -L --output $GO_BIN/buildlet &&
chmod +x $GO_BIN/buildlet &&
$GO_BIN/buildlet --coordinator=gomotessh.golang.org:443 --reverse-type swarming-task -swarming-bot -halt=false
`, goos, goarch)
}

func createStringPairs(m map[string]string) []*swarmpb.StringPair {
	dims := make([]*swarmpb.StringPair, 0, len(m))
	for k, v := range m {
		dims = append(dims, &swarmpb.StringPair{
			Key:   k,
			Value: v,
		})
	}
	return dims
}

func platformToGoValues(platform string) (goos string, goarch string, err error) {
	goos, goarch, ok := strings.Cut(platform, "-")
	if !ok {
		return "", "", fmt.Errorf("cipd_platform not in proper format=%s", platform)
	}
	if goos == "Mac" {
		goos = "darwin"
	}
	return goos, goarch, nil
}

func (ss *SwarmingServer) newSwarmingTask(ctx context.Context, name string, dimensions map[string]string, opts *SwarmOpts) (string, error) {
	cipdPlatform, ok := dimensions["cipd_platform"]
	if !ok {
		return "", fmt.Errorf("dimensions require cipd_platform: instance=%s", name)
	}
	goos, goarch, err := platformToGoValues(cipdPlatform)
	if err != nil {
		return "", err
	}
	req := &swarmpb.NewTaskRequest{
		ExpirationSecs: 86400,
		Name:           name,
		Priority:       30,
		Properties: &swarmpb.TaskProperties{
			Caches: []*swarmpb.CacheEntry{},
			CipdInput: &swarmpb.CipdInput{
				Packages: []*swarmpb.CipdPackage{
					{Path: "tools/bin", PackageName: "infra/tools/luci-auth/" + cipdPlatform, Version: "latest"},
					{Path: "tools", PackageName: "golang/bootstrap-go/" + cipdPlatform, Version: "latest"},
					{Path: "tools", PackageName: "infra/3pp/tools/gcloud/" + cipdPlatform, Version: "latest"},
					{Path: "tools", PackageName: "infra/3pp/tools/cpython3/" + cipdPlatform, Version: "latest"},
				},
			},
			EnvPrefixes: []*swarmpb.StringListPair{
				{Key: "PATH", Value: []string{"tools/bin"}},
			},
			Command:     []string{"bash", "-cx", buildletStartup(goos, goarch)},
			RelativeCwd: "",
			Dimensions:  createStringPairs(dimensions),
			Env: []*swarmpb.StringPair{
				&swarmpb.StringPair{
					Key:   "GOMOTEID",
					Value: name,
				},
			},
			ExecutionTimeoutSecs: 86400,
		},
		ServiceAccount: "coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
		Realm:          "golang:ci",
	}
	taskMD, err := ss.swarmingClient.NewTask(ctx, req)
	if err != nil {
		log.Printf("gomote: swarming task creation failed name=%s: %s", name, err)
		return "", fmt.Errorf("unable to start task: %w", err)
	}
	log.Printf("gomote: task created: id=%s https://chromium-swarm.appspot.com/task?id=%s", taskMD.TaskId, taskMD.TaskId)
	return taskMD.TaskId, nil
}

func condRun(fn func()) {
	if fn != nil {
		fn()
	}
}

// tryThenPeriodicallyDo calls f and then calls f every period until the provided context is cancelled.
func tryThenPeriodicallyDo(ctx context.Context, period time.Duration, f func(context.Context, time.Time)) {
	f(ctx, time.Now())
	internal.PeriodicallyDo(ctx, period, f)
}
