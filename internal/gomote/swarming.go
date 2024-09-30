// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package gomote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/swarming/client/swarming"
	swarmpb "go.chromium.org/luci/swarming/proto/api_v2"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/rendezvous"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// expDisableGolangbuild disables the use of golangbuild during the swarming task gomote bootstrap process.
const expDisableGolangbuild = "disable-golang-build"

type rendezvousClient interface {
	DeregisterInstance(ctx context.Context, id string)
	HandleReverse(w http.ResponseWriter, r *http.Request)
	RegisterInstance(ctx context.Context, id string, wait time.Duration)
	WaitForInstance(ctx context.Context, id string) (buildlet.Client, error)
}

// BuildersClient is a partial interface of the buildbuicketpb.BuildersClient interface.
type BuildersClient interface {
	GetBuilder(ctx context.Context, in *buildbucketpb.GetBuilderRequest, opts ...grpc.CallOption) (*buildbucketpb.BuilderItem, error)
	ListBuilders(ctx context.Context, in *buildbucketpb.ListBuildersRequest, opts ...grpc.CallOption) (*buildbucketpb.ListBuildersResponse, error)
}

// SwarmingServer is a gomote server implementation which supports LUCI swarming bots.
type SwarmingServer struct {
	// embed the unimplemented server.
	protos.UnimplementedGomoteServiceServer

	bucket                  bucketHandle
	buildersClient          BuildersClient
	buildlets               *remote.SessionPool
	gceBucketName           string
	rendezvous              rendezvousClient
	sshCertificateAuthority ssh.Signer
	swarmingClient          swarming.Client
}

// NewSwarming creates a gomote server. If the rawCAPriKey is invalid, the program will exit.
func NewSwarming(rsp *remote.SessionPool, rawCAPriKey []byte, gomoteGCSBucket string, storageClient *storage.Client, rdv *rendezvous.Rendezvous, swarmClient swarming.Client, buildersClient buildbucketpb.BuildersClient) (*SwarmingServer, error) {
	signer, err := ssh.ParsePrivateKey(rawCAPriKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse raw certificate authority private key into signer=%w", err)
	}
	return &SwarmingServer{
		bucket:                  storageClient.Bucket(gomoteGCSBucket),
		buildersClient:          buildersClient,
		buildlets:               rsp,
		gceBucketName:           gomoteGCSBucket,
		rendezvous:              rdv,
		sshCertificateAuthority: signer,
		swarmingClient:          swarmClient,
	}, nil
}

// Authenticate will allow the caller to verify that they are properly authenticated and authorized to interact with the
// Service.
func (ss *SwarmingServer) Authenticate(ctx context.Context, req *protos.AuthenticateRequest) (*protos.AuthenticateResponse, error) {
	_, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("Authenticate access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	return &protos.AuthenticateResponse{}, nil
}

// AddBootstrap adds the bootstrap version of Go to an instance and returns the URL for the bootstrap version. If no
// bootstrap version is defined then the returned version URL will be empty.
func (ss *SwarmingServer) AddBootstrap(ctx context.Context, req *protos.AddBootstrapRequest) (*protos.AddBootstrapResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("AddBootstrap access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	ses, bc, err := ss.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	bs, err := ss.validBuilders(ctx)
	if err != nil {
		return nil, err
	}
	builder, ok := bs[ses.BuilderType]
	if !ok {
		return nil, status.Errorf(codes.Internal, "unable to determine builder definition")
	}
	cp, err := builderProperties(builder)
	if err != nil {
		log.Printf("AddBootstrap: bootstrap version not found for %s: %s", builder.GetId().GetBuilder(), err)
		return &protos.AddBootstrapResponse{}, nil
	}
	if cp.BootstrapVersion == "latest" {
		return &protos.AddBootstrapResponse{}, nil
	}
	var cipdPlatform string
	for _, bd := range builder.GetConfig().GetDimensions() {
		if !strings.HasPrefix(bd, "cipd_platform:") {
			continue
		}
		var ok bool
		_, cipdPlatform, ok = strings.Cut(bd, ":")
		if !ok {
			return nil, status.Errorf(codes.Internal, "unknown builder type")
		}
		break
	}
	goos, goarch, err := platformToGoValues(cipdPlatform)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unknown platform type")
	}
	url := fmt.Sprintf("https://storage.googleapis.com/go-builder-data/gobootstrap-%s-%s-go%s.tar.gz", goos, goarch, cp.BootstrapVersion)
	if err = bc.PutTarFromURL(ctx, url, cp.BootstrapVersion); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to download bootstrap Go")
	}
	return &protos.AddBootstrapResponse{BootstrapGoUrl: url}, nil
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
	bs, err := ss.validBuilders(stream.Context())
	if err != nil {
		return err
	}
	builder, ok := bs[req.GetBuilderType()]
	if !ok {
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

	for _, bd := range builder.GetConfig().GetDimensions() {
		k, v, ok := strings.Cut(bd, ":")
		if ok {
			dimensions[k] = v
		} else {
			log.Printf("failed dimension cut: %s", bd)
		}
	}
	name := fmt.Sprintf("gomote-%s-%s", userName, uuid.NewString())
	cp, err := builderProperties(builder)
	if err != nil {
		log.Printf("CreateInstance: builder configuration not found for %s: %s", builder.GetId().GetBuilder(), err)
		return status.Errorf(codes.Internal, "invalid builder configuration")
	}
	useGolangbuild := !slices.Contains(req.GetExperimentOption(), expDisableGolangbuild)
	go func() {
		bc, err := ss.startNewSwarmingTask(stream.Context(), name, dimensions, cp, &SwarmOpts{}, useGolangbuild)
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
			gomoteID := ss.buildlets.AddSession(creds.ID, userName, req.GetBuilderType(), req.GetBuilderType(), r.buildletClient)
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

// ExecuteCommand will execute a command on a gomote instance. The output from the command will be streamed back to the caller if the output is set.
func (ss *SwarmingServer) ExecuteCommand(req *protos.ExecuteCommandRequest, stream protos.GomoteService_ExecuteCommandServer) error {
	creds, err := access.IAPFromContext(stream.Context())
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	ses, bc, err := ss.sessionAndClient(stream.Context(), req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return err
	}
	builderType := req.GetImitateHostType()
	if builderType == "" {
		builderType = ses.BuilderType
	}
	remoteErr, execErr := bc.Exec(stream.Context(), req.GetCommand(), buildlet.ExecOpts{
		Dir:         req.GetDirectory(),
		SystemLevel: req.GetSystemLevel(),
		Output: &streamWriter{writeFunc: func(p []byte) (int, error) {
			err := stream.Send(&protos.ExecuteCommandResponse{
				Output: p,
			})
			if err != nil {
				return 0, fmt.Errorf("unable to send data=%w", err)
			}
			return len(p), nil
		}},
		Args:     req.GetArgs(),
		ExtraEnv: req.GetAppendEnvironment(),
		Debug:    req.GetDebug(),
		Path:     req.GetPath(),
	})
	if execErr != nil {
		// there were system errors preventing the command from being started or seen to completion.
		return status.Errorf(codes.Aborted, "unable to execute command: %s", execErr)
	}
	if remoteErr != nil {
		// the command failed remotely
		return status.Errorf(codes.Unknown, "command execution failed: %s", remoteErr)
	}
	return nil
}

// InstanceAlive will ensure that the gomote instance is still alive and will extend the timeout. The requester must be authenticated.
func (ss *SwarmingServer) InstanceAlive(ctx context.Context, req *protos.InstanceAliveRequest) (*protos.InstanceAliveResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("InstanceAlive access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid gomote ID")
	}
	_, err = ss.session(req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	if err := ss.buildlets.RenewTimeout(req.GetGomoteId()); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to renew timeout")
	}
	return &protos.InstanceAliveResponse{}, nil
}

// ListDirectory lists the contents of the directory on a gomote instance.
func (ss *SwarmingServer) ListDirectory(ctx context.Context, req *protos.ListDirectoryRequest) (*protos.ListDirectoryResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" || req.GetDirectory() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid arguments")
	}
	_, bc, err := ss.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	opt := buildlet.ListDirOpts{
		Recursive: req.GetRecursive(),
		Digest:    req.GetDigest(),
		Skip:      req.GetSkipFiles(),
	}
	var entries []string
	if err = bc.ListDir(context.Background(), req.GetDirectory(), opt, func(bi buildlet.DirEntry) {
		entries = append(entries, bi.String())
	}); err != nil {
		return nil, status.Errorf(codes.Unimplemented, "method ListDirectory not implemented")
	}
	return &protos.ListDirectoryResponse{
		Entries: entries,
	}, nil
}

const (
	// golangbuildModeAll is golangbuild's MODE_ALL mode that
	// builds and tests the project all within the same build.
	//
	// See https://source.chromium.org/chromium/infra/infra/+/main:go/src/infra/experimental/golangbuild/golangbuildpb/params.proto;l=148-149;drc=4e874bfb4ff7ff0620940712983ca82e8ea81028.
	golangbuildModeAll = 0
	// golangbuildPerfMode is golangbuild's MODE_PERF that
	// runs performance tests.
	//
	// See https://source.chromium.org/chromium/infra/infra/+/main:go/src/infra/experimental/golangbuild/golangbuildpb/params.proto;l=174-177;drc=fdea4abccf8447808d4e702c8d09fdd20fd81acb.
	golangbuildPerfMode = 4
)

func (ss *SwarmingServer) validBuilders(ctx context.Context) (map[string]*buildbucketpb.BuilderItem, error) {
	listBuilders := func(bucket string) ([]*buildbucketpb.BuilderItem, error) {
		var builders []*buildbucketpb.BuilderItem
		var nextToken string
		for {
			buildersResp, err := ss.buildersClient.ListBuilders(ctx, &buildbucketpb.ListBuildersRequest{
				Project:   "golang",
				Bucket:    bucket,
				PageSize:  1000,
				PageToken: nextToken,
			})
			if err != nil {
				return nil, err
			}
			builders = append(builders, buildersResp.GetBuilders()...)
			if tok := buildersResp.GetNextPageToken(); tok != "" {
				nextToken = tok
				continue
			}
			return builders, nil
		}
	}
	// list all the valid builders in ci-workers
	builderBucket := "ci-workers"
	builderResponse, err := listBuilders(builderBucket)
	if err != nil {
		log.Printf("buildersClient.ListBuilders(ctx, %s) = nil, %s", builderBucket, err)
		return nil, status.Errorf(codes.Internal, "unable to query for builders")
	}
	builders := make(map[string]*buildbucketpb.BuilderItem)
	for _, builder := range builderResponse {
		bID := builder.GetId()
		if bID == nil {
			continue
		}
		name := bID.GetBuilder()
		if !strings.HasSuffix(name, "-test_only") {
			continue
		}
		builders[strings.TrimSuffix(name, "-test_only")] = builder
	}
	// list all the valid builders in ci
	builderBucket = "ci"
	builderResponse, err = listBuilders(builderBucket)
	if err != nil {
		log.Printf("buildersClient.ListBuilders(ctx, %s) = nil, %s", builderBucket, err)
		return nil, status.Errorf(codes.Internal, "unable to query for builders")
	}
	for _, builder := range builderResponse {
		bID := builder.GetId()
		if bID == nil {
			continue
		}
		name := bID.GetBuilder()
		if _, ok := builders[name]; ok {
			// should not happen
			continue
		}
		config, err := builderProperties(builder)
		if err != nil || !slices.Contains([]int{golangbuildModeAll, golangbuildPerfMode}, config.Mode) {
			continue
		}
		builders[name] = builder
	}
	return builders, nil
}

// ListSwarmingBuilders lists all of the swarming builders which run for the Go master or release branches. The requester must be authenticated.
func (ss *SwarmingServer) ListSwarmingBuilders(ctx context.Context, req *protos.ListSwarmingBuildersRequest) (*protos.ListSwarmingBuildersResponse, error) {
	_, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("ListSwarmingInstances access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	bs, err := ss.validBuilders(ctx)
	if err != nil {
		return nil, err
	}
	var builders []string
	for builder, _ := range bs {
		builders = append(builders, builder)
	}
	sort.Strings(builders)
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

// ReadTGZToURL retrieves a directory from the gomote instance and writes the file to GCS. It returns a signed URL which the caller uses
// to read the file from GCS.
func (ss *SwarmingServer) ReadTGZToURL(ctx context.Context, req *protos.ReadTGZToURLRequest) (*protos.ReadTGZToURLResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	_, bc, err := ss.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	tgz, err := bc.GetTar(ctx, req.GetDirectory())
	if err != nil {
		return nil, status.Errorf(codes.Aborted, "unable to retrieve tar from gomote instance: %s", err)
	}
	defer tgz.Close()
	objectName := uuid.NewString()
	objectHandle := ss.bucket.Object(objectName)
	// A context for writes is used to ensure we can cancel the context if a
	// problem is encountered while writing to the object store. The API documentation
	// states that the context should be canceled to stop writing without saving the data.
	writeCtx, cancel := context.WithCancel(ctx)
	tgzWriter := objectHandle.NewWriter(writeCtx)
	defer cancel()
	if _, err = io.Copy(tgzWriter, tgz); err != nil {
		return nil, status.Errorf(codes.Aborted, "unable to stream tar.gz: %s", err)
	}
	// when close is called, the object is stored in the bucket.
	if err := tgzWriter.Close(); err != nil {
		return nil, status.Errorf(codes.Aborted, "unable to store object: %s", err)
	}
	url, err := ss.signURLForDownload(objectName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to create signed URL for download: %s", err)
	}
	return &protos.ReadTGZToURLResponse{
		Url: url,
	}, nil
}

// RemoveFiles removes files or directories from the gomote instance.
func (ss *SwarmingServer) RemoveFiles(ctx context.Context, req *protos.RemoveFilesRequest) (*protos.RemoveFilesResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("RemoveFiles access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	// TODO(go.dev/issue/48742) consider what additional path validation should be implemented.
	if req.GetGomoteId() == "" || len(req.GetPaths()) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid arguments")
	}
	_, bc, err := ss.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	if err := bc.RemoveAll(ctx, req.GetPaths()...); err != nil {
		log.Printf("RemoveFiles buildletClient.RemoveAll(ctx, %q) = %s", req.GetPaths(), err)
		return nil, status.Errorf(codes.Unknown, "unable to remove files")
	}
	return &protos.RemoveFilesResponse{}, nil
}

// SignSSHKey signs the public SSH key with a certificate. The signed public SSH key is intended for use with the gomote service SSH
// server. It will be signed by the certificate authority of the server and will restrict access to the gomote instance that it was
// signed for.
func (ss *SwarmingServer) SignSSHKey(ctx context.Context, req *protos.SignSSHKeyRequest) (*protos.SignSSHKeyResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	session, err := ss.session(req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	signedPublicKey, err := remote.SignPublicSSHKey(ctx, ss.sshCertificateAuthority, req.GetPublicSshKey(), session.ID, session.OwnerID, 5*time.Minute)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unable to sign ssh key")
	}
	return &protos.SignSSHKeyResponse{
		SignedPublicSshKey: signedPublicKey,
	}, nil
}

// UploadFile creates a URL and a set of HTTP post fields which are used to upload a file to a staging GCS bucket. Uploaded files are made available to the
// gomote instances via a subsequent call to one of the WriteFromURL endpoints.
func (ss *SwarmingServer) UploadFile(ctx context.Context, req *protos.UploadFileRequest) (*protos.UploadFileResponse, error) {
	_, err := access.IAPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	objectName := uuid.NewString()
	url, fields, err := ss.signURLForUpload(objectName)
	if err != nil {
		log.Printf("unable to create signed URL: %s", err)
		return nil, status.Errorf(codes.Internal, "unable to create signed url")
	}
	return &protos.UploadFileResponse{
		Url:        url,
		Fields:     fields,
		ObjectName: objectName,
	}, nil
}

// signURLForUpload generates a signed URL and a set of http Post fields to be used to upload an object to GCS without authenticating.
func (ss *SwarmingServer) signURLForUpload(object string) (url string, fields map[string]string, err error) {
	if object == "" {
		return "", nil, errors.New("invalid object name")
	}
	pv4, err := ss.bucket.GenerateSignedPostPolicyV4(object, &storage.PostPolicyV4Options{
		Expires:  time.Now().Add(10 * time.Minute),
		Insecure: false,
	})
	if err != nil {
		return "", nil, fmt.Errorf("unable to generate signed url: %w", err)
	}
	return pv4.URL, pv4.Fields, nil
}

// WriteFileFromURL initiates an HTTP request to the passed in URL and streams the contents of the request to the gomote instance.
func (ss *SwarmingServer) WriteFileFromURL(ctx context.Context, req *protos.WriteFileFromURLRequest) (*protos.WriteFileFromURLResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("WriteTGZFromURL access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	_, bc, err := ss.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	var rc io.ReadCloser
	// objects stored in the gomote staging bucket are only accessible when you have been granted explicit permissions. A builder
	// requires a signed URL in order to access objects stored in the gomote staging bucket.
	if onObjectStore(ss.gceBucketName, req.GetUrl()) {
		object, err := objectFromURL(ss.gceBucketName, req.GetUrl())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid object URL")
		}
		rc, err = ss.bucket.Object(object).NewReader(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "unable to create object reader: %s", err)
		}
	} else {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, req.GetUrl(), nil)
		if err != nil {
			log.Printf("gomote: unable to create HTTP request: %s", err)
			return nil, status.Errorf(codes.Internal, "unable to create HTTP request")
		}
		// TODO(amedee) find sane client defaults, possibly rely on context timeout in request.
		client := &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout: 5 * time.Second,
			},
		}
		resp, err := client.Do(httpRequest)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "failed to get file from URL: %s", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, status.Errorf(codes.Aborted, "unable to get file from %q: response code: %d", req.GetUrl(), resp.StatusCode)
		}
		rc = resp.Body
	}
	defer rc.Close()
	if err := bc.Put(ctx, rc, req.GetFilename(), fs.FileMode(req.GetMode())); err != nil {
		return nil, status.Errorf(codes.Aborted, "failed to send the file to the gomote instance: %s", err)
	}
	return &protos.WriteFileFromURLResponse{}, nil
}

// WriteTGZFromURL will instruct the gomote instance to download the tar.gz from the provided URL. The tar.gz file will be unpacked in the work directory
// relative to the directory provided.
func (ss *SwarmingServer) WriteTGZFromURL(ctx context.Context, req *protos.WriteTGZFromURLRequest) (*protos.WriteTGZFromURLResponse, error) {
	creds, err := access.IAPFromContext(ctx)
	if err != nil {
		log.Printf("WriteTGZFromURL access.IAPFromContext(ctx) = nil, %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "request does not contain the required authentication")
	}
	if req.GetGomoteId() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid gomote ID")
	}
	if req.GetUrl() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "missing URL")
	}
	_, bc, err := ss.sessionAndClient(ctx, req.GetGomoteId(), creds.ID)
	if err != nil {
		// the helper function returns meaningful GRPC error.
		return nil, err
	}
	url := req.GetUrl()
	if onObjectStore(ss.gceBucketName, url) {
		object, err := objectFromURL(ss.gceBucketName, url)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid URL")
		}
		url, err = ss.signURLForDownload(object)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "unable to sign url for download: %s", err)
		}
	}
	if err := bc.PutTarFromURL(ctx, url, req.GetDirectory()); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "unable to write tar.gz: %s", err)
	}
	return &protos.WriteTGZFromURLResponse{}, nil
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

// sessionAndClient is a helper function that retrieves a session and buildlet client for the
// associated gomoteID and ownerID. The gomote instance timeout is renewed if the gomote id and owner id
// are valid.
func (ss *SwarmingServer) sessionAndClient(ctx context.Context, gomoteID, ownerID string) (*remote.Session, buildlet.Client, error) {
	session, err := ss.session(gomoteID, ownerID)
	if err != nil {
		return nil, nil, err
	}
	bc, err := ss.buildlets.BuildletClient(gomoteID)
	if err != nil {
		return nil, nil, status.Errorf(codes.NotFound, "specified gomote instance does not exist")
	}
	if err := ss.buildlets.KeepAlive(ctx, gomoteID); err != nil {
		log.Printf("gomote: unable to keep alive %s: %s", gomoteID, err)
	}
	return session, bc, nil
}

// signURLForDownload generates a signed URL and fields to be used to upload an object to GCS without authenticating.
func (ss *SwarmingServer) signURLForDownload(object string) (url string, err error) {
	url, err = ss.bucket.SignedURL(object, &storage.SignedURLOptions{
		Expires: time.Now().Add(10 * time.Minute),
		Method:  http.MethodGet,
		Scheme:  storage.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("unable to generate signed url: %w", err)
	}
	return url, err
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
func (ss *SwarmingServer) startNewSwarmingTask(ctx context.Context, name string, dimensions map[string]string, properties *configProperties, opts *SwarmOpts, useGolangbuild bool) (buildlet.Client, error) {
	ss.rendezvous.RegisterInstance(ctx, name, 10*time.Minute)
	condRun(opts.OnInstanceRegistration)

	taskID, err := ss.newSwarmingTask(ctx, name, dimensions, properties, opts, useGolangbuild)
	if err != nil {
		ss.rendezvous.DeregisterInstance(ctx, name)
		return nil, err
	}
	log.Printf("gomote: swarming task requested name=%s taskID=%s", name, taskID)
	condRun(opts.OnInstanceRequested)

	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
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
	queryCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)

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
	cmd := `import urllib.request
import sys
import platform
import subprocess
import os
import stat

def add_os_file_ext(filename):
    if sys.platform == "win32":
        return filename+".exe"
    return filename

def sep():
    if sys.platform == "win32":
        return "\\"
    else:
        return "/"

def delete_if_exists(file_path):
    if os.path.exists(file_path):
        os.remove(file_path)

def make_executable(file_path):
    if sys.platform != "win32":
        st = os.stat(file_path)
        os.chmod(file_path, st.st_mode | stat.S_IEXEC)

if __name__ == "__main__":
    buildlet_name = add_os_file_ext("buildlet")
    delete_if_exists(buildlet_name)
    urllib.request.urlretrieve("https://storage.googleapis.com/go-builder-data/buildlet.%s-%s", buildlet_name)
    make_executable(os.getcwd() + sep() + buildlet_name)
    buildlet_name = "."+sep()+buildlet_name
    subprocess.run([buildlet_name, "--coordinator=gomotessh.golang.org:443", "--reverse-type=swarming-task", "-swarming-bot", "-halt=false"], shell=False, env=os.environ.copy())
`
	return fmt.Sprintf(cmd, goos, goarch)
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
	if goos == "Mac" || goos == "mac" {
		goos = "darwin"
	}
	if goarch == "armv6l" {
		goarch = "arm"
	}
	return goos, goarch, nil
}

func (ss *SwarmingServer) newSwarmingTask(ctx context.Context, name string, dimensions map[string]string, properties *configProperties, opts *SwarmOpts, useGolangbuild bool) (string, error) {
	if useGolangbuild {

		return ss.newSwarmingTaskWithGolangbuild(ctx, name, dimensions, properties, opts)
	}
	cipdPlatform, ok := dimensions["cipd_platform"]
	if !ok {
		return "", fmt.Errorf("dimensions require cipd_platform: instance=%s", name)
	}
	goos, goarch, err := platformToGoValues(cipdPlatform)
	if err != nil {
		return "", err
	}
	packages := []*swarmpb.CipdPackage{
		{Path: "tools/bin", PackageName: "infra/tools/luci-auth/" + cipdPlatform, Version: "latest"},
		{Path: "tools/bootstrap-go", PackageName: "golang/bootstrap-go/" + cipdPlatform, Version: properties.BootstrapVersion},
	}
	pythonBin := "python3"
	switch goos {
	case "darwin":
		pythonBin = `tools/bin/python3`
		packages = append(packages,
			&swarmpb.CipdPackage{Path: "tools/bin", PackageName: "infra/tools/mac_toolchain/" + cipdPlatform, Version: "latest"},
			&swarmpb.CipdPackage{Path: "tools", PackageName: "infra/3pp/tools/cpython3/" + cipdPlatform, Version: "latest"})
	case "windows":
		pythonBin = `tools\bin\python3.exe`
		packages = append(packages, &swarmpb.CipdPackage{Path: "tools", PackageName: "infra/3pp/tools/cpython3/" + cipdPlatform, Version: "latest"})
	}

	req := &swarmpb.NewTaskRequest{
		Name:           name,
		Priority:       20, // 30 is the priority for builds
		ServiceAccount: "coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
		TaskSlices: []*swarmpb.TaskSlice{
			&swarmpb.TaskSlice{
				Properties: &swarmpb.TaskProperties{
					CipdInput: &swarmpb.CipdInput{
						Packages: packages,
					},
					EnvPrefixes: []*swarmpb.StringListPair{
						{Key: "PATH", Value: []string{"tools/bin", "go/bin"}},
						{Key: "GOROOT_BOOTSTRAP", Value: []string{"tools/bootstrap-go"}},
						{Key: "GOPATH", Value: []string{"gopath"}},
					},
					Command:    []string{pythonBin, "-c", buildletStartup(goos, goarch)},
					Dimensions: createStringPairs(dimensions),
					Env: []*swarmpb.StringPair{
						&swarmpb.StringPair{
							Key:   "GOMOTEID",
							Value: name,
						},
					},
					// The swarming limits state it must be between 30s and 601140s. This information is returned
					// as part of an error message when you attempt a request with a value outside of these boundaries.
					ExecutionTimeoutSecs: 601140,
				},
				ExpirationSecs:  86400,
				WaitForCapacity: false,
			},
		},
		Tags:  []string{"golang_mode:gomote"},
		Realm: "golang:ci",
	}
	taskMD, err := ss.swarmingClient.NewTask(ctx, req)
	if err != nil {
		log.Printf("gomote: swarming task creation failed name=%s: %s", name, err)
		return "", fmt.Errorf("unable to start task: %w", err)
	}
	log.Printf("gomote: task created: id=%s https://chromium-swarm.appspot.com/task?id=%s", taskMD.TaskId, taskMD.TaskId)
	return taskMD.TaskId, nil
}

func golangbuildStartup(goos, goarch, golangbuildBin string) string {
	cmd := `import urllib.request
import sys
import platform
import subprocess
import os
import stat

def make_executable(file_path):
    if sys.platform != "win32":
        st = os.stat(file_path)
        os.chmod(file_path, st.st_mode | stat.S_IEXEC)

if __name__ == "__main__":
    ext = ""
    if sys.platform == "win32":
        ext = ".exe"
    sep = "/"
    if sys.platform == "win32":
        sep = "\\\\"
    buildlet_file = "buildlet" + ext
    buildlet_path = "." + sep + buildlet_file
    if os.path.exists(buildlet_path):
        os.remove(buildlet_path)
    urllib.request.urlretrieve("https://storage.googleapis.com/go-builder-data/buildlet.%s-%s", buildlet_path)
    make_executable(os.getcwd() + sep + buildlet_file)
    subprocess.run(["%s", buildlet_path, "--workdir="+os.getcwd(), "--coordinator=gomotessh.golang.org:443", "--reverse-type=swarming-task", "-swarming-bot", "-halt=false"], shell=False, env=os.environ.copy())
`
	return fmt.Sprintf(cmd, goos, goarch, golangbuildBin)
}

func (ss *SwarmingServer) newSwarmingTaskWithGolangbuild(ctx context.Context, name string, dimensions map[string]string, properties *configProperties, opts *SwarmOpts) (string, error) {
	log.Printf("gomote: swarming task creation using golangbuild name=%s", name)
	cipdPlatform, ok := dimensions["cipd_platform"]
	if !ok {
		return "", fmt.Errorf("dimensions require cipd_platform: instance=%s", name)
	}
	goos, goarch, err := platformToGoValues(cipdPlatform)
	if err != nil {
		return "", err
	}
	packages := []*swarmpb.CipdPackage{
		{Path: "tools/bin", PackageName: "infra/experimental/golangbuild/" + cipdPlatform, Version: "latest"},
		{Path: "tools/bin", PackageName: "infra/tools/cipd/" + cipdPlatform, Version: "latest"},
		{Path: "tools/bin", PackageName: "infra/tools/luci-auth/" + cipdPlatform, Version: "latest"},
	}
	pythonBin := "python3"
	golangbuildBin := "golangbuild"
	switch goos {
	case "darwin":
		golangbuildBin = `tools/bin/golangbuild`
		pythonBin = `tools/bin/python3`
		packages = append(packages,
			&swarmpb.CipdPackage{Path: "tools", PackageName: "infra/3pp/tools/cpython3/" + cipdPlatform, Version: "latest"})
	case "windows":
		golangbuildBin = `tools\\bin\\golangbuild.exe`
		pythonBin = `tools\bin\python3.exe`
		packages = append(packages, &swarmpb.CipdPackage{Path: "tools", PackageName: "infra/3pp/tools/cpython3/" + cipdPlatform, Version: "latest"})
	}

	req := &swarmpb.NewTaskRequest{
		Name:           name,
		Priority:       20, // 30 is the priority for builds
		ServiceAccount: "coordinator-builder@golang-ci-luci.iam.gserviceaccount.com",
		TaskSlices: []*swarmpb.TaskSlice{
			&swarmpb.TaskSlice{
				Properties: &swarmpb.TaskProperties{
					CipdInput: &swarmpb.CipdInput{
						Packages: packages,
					},
					EnvPrefixes: []*swarmpb.StringListPair{
						{Key: "PATH", Value: []string{"tools/bin"}},
					},
					Command:    []string{pythonBin, "-c", golangbuildStartup(goos, goarch, golangbuildBin)},
					Dimensions: createStringPairs(dimensions),
					Env: []*swarmpb.StringPair{
						&swarmpb.StringPair{
							Key:   "GOMOTEID",
							Value: name,
						},
						&swarmpb.StringPair{
							Key:   "GOMOTE_SETUP",
							Value: properties.BuilderId,
						},
					},
					// The swarming limits state it must be between 30s and 601140s. This information is returned
					// as part of an error message when you attempt a request with a value outside of these boundaries.
					ExecutionTimeoutSecs: 601140,
				},
				ExpirationSecs:  86400,
				WaitForCapacity: false,
			},
		},
		Tags:  []string{"golang_mode:gomote"},
		Realm: "golang:ci",
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

type configProperties struct {
	BootstrapVersion string `json:"bootstrap_version"`
	Mode             int    `json:"mode"`
	BuilderId        string // <project>/<bucket>/<builder>
}

func builderProperties(builder *buildbucketpb.BuilderItem) (*configProperties, error) {
	cp := new(configProperties)
	if err := json.Unmarshal([]byte(builder.GetConfig().GetProperties()), cp); err != nil {
		return nil, fmt.Errorf("builder property unmarshal error: %s", err)
	}
	if cp.BootstrapVersion == "" {
		cp.BootstrapVersion = "latest"
	}
	id := builder.GetId()
	cp.BuilderId = fmt.Sprintf("%s/%s/%s", id.GetProject(), id.GetBucket(), id.GetBuilder())
	return cp, nil
}
