// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package gomote

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"go.chromium.org/luci/swarming/client/swarming"
	"go.chromium.org/luci/swarming/client/swarming/swarmingtest"
	swarmpb "go.chromium.org/luci/swarming/proto/api_v2"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/rendezvous"
	"golang.org/x/build/internal/swarmclient"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testSwarmingBucketName = "unit-testing-bucket-swarming"

func fakeGomoteSwarmingServer(t *testing.T, ctx context.Context, configClient *swarmclient.ConfigClient, swarmClient swarming.Client, rdv rendezvousClient) protos.GomoteServiceServer {
	signer, err := ssh.ParsePrivateKey([]byte(devCertCAPrivate))
	if err != nil {
		t.Fatalf("unable to parse raw certificate authority private key into signer=%s", err)
	}
	return &SwarmingServer{
		bucket:                  &fakeBucketHandler{bucketName: testSwarmingBucketName},
		buildlets:               remote.NewSessionPool(ctx),
		gceBucketName:           testSwarmingBucketName,
		sshCertificateAuthority: signer,
		luciConfigClient:        configClient,
		rendezvous:              rdv,
		swarmingClient:          swarmClient,
	}
}

func setupGomoteSwarmingTest(t *testing.T, ctx context.Context, swarmClient swarming.Client) protos.GomoteServiceClient {
	contents, err := os.ReadFile("../swarmclient/testdata/bb-sample.cfg")
	if err != nil {
		t.Fatalf("unable to read test buildbucket config: %s", err)
	}
	configClient := swarmclient.NewMemoryConfigClient(ctx, []*swarmclient.ConfigEntry{
		&swarmclient.ConfigEntry{"cr-buildbucket.cfg", contents},
	})
	lis, err := nettest.NewLocalListener("tcp")
	if err != nil {
		t.Fatalf("unable to create net listener: %s", err)
	}
	rdv := rendezvous.NewFake(context.Background(), func(ctx context.Context, jwt string) bool { return true })
	sopts := access.FakeIAPAuthInterceptorOptions()
	s := grpc.NewServer(sopts...)
	protos.RegisterGomoteServiceServer(s, fakeGomoteSwarmingServer(t, ctx, configClient, swarmClient, rdv))
	go s.Serve(lis)

	// create GRPC client
	copts := []grpc.DialOption{
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithTimeout(5 * time.Second),
	}
	conn, err := grpc.Dial(lis.Addr().String(), copts...)
	if err != nil {
		lis.Close()
		t.Fatalf("unable to create GRPC client: %s", err)
	}
	gc := protos.NewGomoteServiceClient(conn)
	t.Cleanup(func() {
		conn.Close()
		s.Stop()
		lis.Close()
	})
	return gc
}

func TestSwarmingListSwarmingBuilders(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stdout)

	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClient())
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	response, err := client.ListSwarmingBuilders(ctx, &protos.ListSwarmingBuildersRequest{})
	if err != nil {
		t.Fatalf("client.ListSwarmingBuilders = nil, %s; want no error", err)
	}
	got := response.GetBuilders()
	if diff := cmp.Diff([]string{"gotip-linux-amd64-boringcrypto"}, got); diff != "" {
		t.Errorf("ListBuilders() mismatch (-want, +got):\n%s", diff)
	}
}

func TestSwarmingListSwarmingBuildersError(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stdout)

	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClient())
	req := &protos.ListSwarmingBuildersRequest{}
	got, err := client.ListSwarmingBuilders(context.Background(), req)
	if err != nil && status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unexpected error: %s; want %s", err, codes.Unauthenticated)
	}
	if err == nil {
		t.Fatalf("client.ListSwarmingBuilder(ctx, %v) = %v, nil; want error", req, got)
	}
}

func TestSwarmingCreateInstance(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	req := &protos.CreateInstanceRequest{BuilderType: "gotip-linux-amd64-boringcrypto"}

	msc := mockSwarmClient()
	msc.NewTaskMock = func(_ context.Context, req *swarmpb.NewTaskRequest) (*swarmpb.TaskRequestMetadataResponse, error) {
		taskID := uuid.New().String()
		return &swarmpb.TaskRequestMetadataResponse{
			TaskId: taskID,
			Request: &swarmpb.TaskRequestResponse{
				TaskId: taskID,
				Name:   req.Name,
			},
		}, nil
	}
	msc.TaskResultMock = func(_ context.Context, taskID string, _ *swarming.TaskResultFields) (*swarmpb.TaskResultResponse, error) {
		return &swarmpb.TaskResultResponse{
			TaskId: taskID,
			State:  swarmpb.TaskState_RUNNING,
		}, nil
	}

	gomoteClient := setupGomoteSwarmingTest(t, context.Background(), msc)

	stream, err := gomoteClient.CreateInstance(ctx, req)
	if err != nil {
		t.Fatalf("client.CreateInstance(ctx, %v) = %v,  %s; want no error", req, stream, err)
	}
	var updateComplete bool
	for {
		update, err := stream.Recv()
		if err == io.EOF && !updateComplete {
			t.Fatal("stream.Recv = stream, io.EOF; want no EOF")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv() = nil, %s; want no error", err)
		}
		if update.GetStatus() == protos.CreateInstanceResponse_COMPLETE {
			updateComplete = true
		}
	}
}

func TestSwarmingCreateInstanceError(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stdout)

	testCases := []struct {
		desc     string
		ctx      context.Context
		request  *protos.CreateInstanceRequest
		wantCode codes.Code
	}{
		{
			desc:     "unauthenticated request",
			ctx:      context.Background(),
			request:  &protos.CreateInstanceRequest{},
			wantCode: codes.Unauthenticated,
		},
		{
			desc:     "missing builder type",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			request:  &protos.CreateInstanceRequest{},
			wantCode: codes.InvalidArgument,
		},
		{
			desc: "invalid builder type",
			ctx:  access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			request: &protos.CreateInstanceRequest{
				BuilderType: "funky-time-builder",
			},
			wantCode: codes.InvalidArgument,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClient())

			stream, err := client.CreateInstance(tc.ctx, tc.request)
			if err != nil {
				t.Fatalf("client.CreateInstance(ctx, %v) = %v,  %s; want no error", tc.request, stream, err)
			}
			for {
				_, got := stream.Recv()
				if got == io.EOF {
					t.Fatal("stream.Recv = stream, io.EOF; want no EOF")
				}
				if got != nil && status.Code(got) != tc.wantCode {
					t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
				}
				return
			}
		})
	}
}

func TestSwarmingDestroyInstance(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	if _, err := client.DestroyInstance(ctx, &protos.DestroyInstanceRequest{
		GomoteId: gomoteID,
	}); err != nil {
		t.Fatalf("client.DestroyInstance(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingDestroyInstanceError(t *testing.T) {
	// This test will create a gomote instance and attempt to call DestroyInstance.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		gomoteID   string // Used iff overrideID is true.
		wantCode   codes.Code
	}{
		{
			desc:     "unauthenticated request",
			ctx:      context.Background(),
			wantCode: codes.Unauthenticated,
		},
		{
			desc:       "missing gomote id",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			overrideID: true,
			gomoteID:   "",
			wantCode:   codes.InvalidArgument,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: true,
			gomoteID:   "chucky",
			wantCode:   codes.NotFound,
		},
		{
			desc:       "wrong gomote id",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: false,
			wantCode:   codes.PermissionDenied,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
			gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
			if tc.overrideID {
				gomoteID = tc.gomoteID
			}
			req := &protos.DestroyInstanceRequest{
				GomoteId: gomoteID,
			}
			got, err := client.DestroyInstance(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.DestroyInstance(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

func TestStartNewSwarmingTask(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stdout)

	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	rdv := rendezvous.New(ctx, rendezvous.OptionValidator(func(ctx context.Context, jwt string) bool {
		return true
	}))
	ts := httptest.NewTLSServer(http.HandlerFunc(rdv.HandleReverse))
	defer ts.Close()

	msc := mockSwarmClient()
	msc.NewTaskMock = func(_ context.Context, req *swarmpb.NewTaskRequest) (*swarmpb.TaskRequestMetadataResponse, error) {
		taskID := uuid.New().String()
		return &swarmpb.TaskRequestMetadataResponse{
			TaskId: taskID,
			Request: &swarmpb.TaskRequestResponse{
				TaskId: taskID,
				Name:   req.Name,
			},
		}, nil
	}
	msc.TaskResultMock = func(_ context.Context, taskID string, _ *swarming.TaskResultFields) (*swarmpb.TaskResultResponse, error) {
		return &swarmpb.TaskResultResponse{
			TaskId: taskID,
			State:  swarmpb.TaskState_RUNNING,
		}, nil
	}
	ss := &SwarmingServer{
		UnimplementedGomoteServiceServer: protos.UnimplementedGomoteServiceServer{},
		bucket:                           nil,
		buildlets:                        &remote.SessionPool{},
		gceBucketName:                    "",
		luciConfigClient:                 &swarmclient.ConfigClient{},
		sshCertificateAuthority:          nil,
		rendezvous:                       rdv,
		swarmingClient:                   msc,
	}
	id := "task-123"
	errCh := make(chan error, 2)
	if _, err := ss.startNewSwarmingTask(ctx, id, map[string]string{"cipd_platform": "linux-amd64"}, &SwarmOpts{
		OnInstanceRegistration: func() {
			client := ts.Client()
			req, err := http.NewRequest("GET", ts.URL, nil)
			req.Header.Set(rendezvous.HeaderID, id)
			req.Header.Set(rendezvous.HeaderToken, "test-token")
			req.Header.Set(rendezvous.HeaderHostname, "test-hostname")
			resp, err := client.Do(req)
			if err != nil {
				errCh <- fmt.Errorf("client.Do() = %s; want no error", err)
				return
			}
			if b, err := io.ReadAll(resp.Body); err != nil {
				errCh <- fmt.Errorf("io.ReadAll(body) = %b, %s, want no error", b, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 101 {
				errCh <- fmt.Errorf("resp.StatusCode  %d; want 101", resp.StatusCode)
				return
			}
		},
	}); err == nil || !strings.Contains(err.Error(), "revdial.Dialer closed") {
		errCh <- fmt.Errorf("startNewSwarmingTask() = bc, %s; want \"revdial.Dialer closed\" error", err)
	}
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func mockSwarmClient() *swarmingtest.Client {
	return &swarmingtest.Client{
		NewTaskMock: func(context.Context, *swarmpb.NewTaskRequest) (*swarmpb.TaskRequestMetadataResponse, error) {
			panic("NewTask not implemented")
		},
		CountTasksMock: func(context.Context, float64, swarmpb.StateQuery, []string) (*swarmpb.TasksCount, error) {
			panic("CountTasks not implemented")
		},
		ListTasksMock: func(context.Context, int32, float64, swarmpb.StateQuery, []string) ([]*swarmpb.TaskResultResponse, error) {
			panic("ListTasks not implemented")
		},
		CancelTaskMock: func(context.Context, string, bool) (*swarmpb.CancelResponse, error) {
			panic("CancelTask not implemented")
		},
		TaskRequestMock: func(context.Context, string) (*swarmpb.TaskRequestResponse, error) {
			panic("TaskRequest not implemented")
		},
		TaskOutputMock: func(context.Context, string) (*swarmpb.TaskOutputResponse, error) {
			panic("TaskOutput not implemented")
		},
		TaskResultMock: func(context.Context, string, *swarming.TaskResultFields) (*swarmpb.TaskResultResponse, error) {
			panic("TaskResult not implemented")
		},
		CountBotsMock: func(context.Context, []*swarmpb.StringPair) (*swarmpb.BotsCount, error) {
			panic("CountBots not implemented")
		},
		ListBotsMock: func(context.Context, []*swarmpb.StringPair) ([]*swarmpb.BotInfo, error) {
			panic("ListBots not implemented")
		},
		DeleteBotMock: func(context.Context, string) (*swarmpb.DeleteResponse, error) { panic("TerminateBot not implemented") },
		TerminateBotMock: func(context.Context, string, string) (*swarmpb.TerminateResponse, error) {
			panic("TerminateBot not implemented")
		},
		ListBotTasksMock: func(context.Context, string, int32, float64, swarmpb.StateQuery) ([]*swarmpb.TaskResultResponse, error) {
			panic("ListBotTasks not implemented")
		},
		FilesFromCASMock: func(context.Context, string, *swarmpb.CASReference) ([]string, error) {
			panic("FilesFromCAS not implemented")
		},
	}
}

func mockSwarmClientSimple() *swarmingtest.Client {
	msc := mockSwarmClient()
	msc.NewTaskMock = func(_ context.Context, req *swarmpb.NewTaskRequest) (*swarmpb.TaskRequestMetadataResponse, error) {
		taskID := uuid.New().String()
		return &swarmpb.TaskRequestMetadataResponse{
			TaskId: taskID,
			Request: &swarmpb.TaskRequestResponse{
				TaskId: taskID,
				Name:   req.Name,
			},
		}, nil
	}
	msc.TaskResultMock = func(_ context.Context, taskID string, _ *swarming.TaskResultFields) (*swarmpb.TaskResultResponse, error) {
		return &swarmpb.TaskResultResponse{
			TaskId: taskID,
			State:  swarmpb.TaskState_RUNNING,
		}, nil
	}
	return msc
}

func mustCreateSwarmingInstance(t *testing.T, client protos.GomoteServiceClient, iap access.IAPFields) string {
	req := &protos.CreateInstanceRequest{
		BuilderType: "gotip-linux-amd64-boringcrypto",
	}
	stream, err := client.CreateInstance(access.FakeContextWithOutgoingIAPAuth(context.Background(), iap), req)
	if err != nil {
		t.Fatalf("client.CreateInstance(ctx, %v) = %v,  %s; want no error", req, stream, err)
	}
	var updateComplete bool
	var gomoteID string
	for {
		update, err := stream.Recv()
		if err == io.EOF && !updateComplete {
			t.Fatal("stream.Recv = stream, io.EOF; want no EOF")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv() = nil, %s; want no error", err)
		}
		if update.GetStatus() == protos.CreateInstanceResponse_COMPLETE {
			gomoteID = update.Instance.GetGomoteId()
			updateComplete = true
		}
	}
	return gomoteID
}
