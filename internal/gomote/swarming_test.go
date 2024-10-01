// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package gomote

import (
	"context"
	"errors"
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
	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/swarming/client/swarming"
	"go.chromium.org/luci/swarming/client/swarming/swarmingtest"
	swarmpb "go.chromium.org/luci/swarming/proto/api_v2"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/rendezvous"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
)

const testSwarmingBucketName = "unit-testing-bucket-swarming"

func fakeGomoteSwarmingServer(t *testing.T, ctx context.Context, swarmClient swarming.Client, rdv rendezvousClient) protos.GomoteServiceServer {
	signer, err := ssh.ParsePrivateKey([]byte(devCertCAPrivate))
	if err != nil {
		t.Fatalf("unable to parse raw certificate authority private key into signer=%s", err)
	}
	return &SwarmingServer{
		bucket:                  &fakeBucketHandler{bucketName: testSwarmingBucketName},
		buildlets:               remote.NewSessionPool(ctx),
		gceBucketName:           testSwarmingBucketName,
		sshCertificateAuthority: signer,
		rendezvous:              rdv,
		swarmingClient:          swarmClient,
		buildersClient:          &FakeBuildersClient{},
	}
}

func setupGomoteSwarmingTest(t *testing.T, ctx context.Context, swarmClient swarming.Client) protos.GomoteServiceClient {
	lis, err := nettest.NewLocalListener("tcp")
	if err != nil {
		t.Fatalf("unable to create net listener: %s", err)
	}
	rdv := rendezvous.NewFake(context.Background(), func(ctx context.Context, jwt string) bool { return true })
	sopts := access.FakeIAPAuthInterceptorOptions()
	s := grpc.NewServer(sopts...)
	protos.RegisterGomoteServiceServer(s, fakeGomoteSwarmingServer(t, ctx, swarmClient, rdv))
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

func TestSwarmingAuthenticate(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClient())
	got, err := client.Authenticate(ctx, &protos.AuthenticateRequest{})
	if err != nil {
		t.Fatalf("client.Authenticate(ctx, request) = %v,  %s; want no error", got, err)
	}
}

func TestSwarmingAuthenticateError(t *testing.T) {
	wantCode := codes.Unauthenticated
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClient())
	_, err := client.Authenticate(context.Background(), &protos.AuthenticateRequest{})
	if status.Code(err) != wantCode {
		t.Fatalf("client.Authenticate(ctx, request) = _, %s; want %s", status.Code(err), wantCode)
	}
}

func TestSwarmingAddBootstrap(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	req := &protos.AddBootstrapRequest{
		GomoteId: gomoteID,
	}
	got, err := client.AddBootstrap(ctx, req)
	if err != nil {
		t.Fatalf("client.AddBootstrap(ctx, %v) = %v, %s; want no error", req, got, err)
	}
}

func TestSwarmingAddBootstrapError(t *testing.T) {
	// This test will create a gomote instance and attempt to call AddBootstrap.
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
			wantCode:   codes.NotFound,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			overrideID: true,
			gomoteID:   "xyz",
			wantCode:   codes.NotFound,
		},
		{
			desc:     "gomote is not owned by caller",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("user-x", "email-y")),
			wantCode: codes.PermissionDenied,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
			gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
			if tc.overrideID {
				gomoteID = tc.gomoteID
			}
			req := &protos.AddBootstrapRequest{
				GomoteId: gomoteID,
			}
			got, err := client.AddBootstrap(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.AddBootstrap(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
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
	if diff := cmp.Diff([]string{"gotip-linux-amd64", "gotip-linux-amd64-boringcrypto", "gotip-linux-arm"}, got); diff != "" {
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

func TestSwarmingExecuteCommand(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	stream, err := client.ExecuteCommand(ctx, &protos.ExecuteCommandRequest{
		GomoteId:          gomoteID,
		Command:           "ls",
		SystemLevel:       false,
		Debug:             false,
		AppendEnvironment: nil,
		Path:              nil,
		Directory:         "/workdir",
		Args:              []string{"-alh"},
	})
	if err != nil {
		t.Fatalf("client.ExecuteCommand(ctx, req) = response, %s; want no error", err)
	}
	var out []byte
	for {
		res, err := stream.Recv()
		if err != nil && err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv() = _, %s; want no error", err)
		}
		out = append(out, res.GetOutput()...)
	}
	if len(out) == 0 {
		t.Fatalf("output: %q, expected non-empty", out)
	}
}

func TestSwarmingExecuteCommandError(t *testing.T) {
	// This test will create a gomote instance and attempt to call TestExecuteCommand.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		gomoteID   string // Used iff overrideID is true.
		cmd        string
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
			wantCode:   codes.NotFound,
		},
		{
			desc:     "missing command",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			wantCode: codes.Aborted,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: true,
			gomoteID:   "chucky",
			cmd:        "ls",
			wantCode:   codes.NotFound,
		},
		{
			desc:       "wrong gomote id",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: false,
			cmd:        "ls",
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
			stream, err := client.ExecuteCommand(tc.ctx, &protos.ExecuteCommandRequest{
				GomoteId:          gomoteID,
				Command:           tc.cmd,
				SystemLevel:       false,
				Debug:             false,
				AppendEnvironment: nil,
				Path:              nil,
				Directory:         "/workdir",
				Args:              []string{"-alh"},
			})
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			res, err := stream.Recv()
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s", err)
			}
			if err == nil {
				t.Fatalf("client.ExecuteCommand(ctx, req) = %v, nil; want error", res)
			}
		})
	}
}

func TestSwarmingInstanceAlive(t *testing.T) {
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	req := &protos.InstanceAliveRequest{
		GomoteId: gomoteID,
	}
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	got, err := client.InstanceAlive(ctx, req)
	if err != nil {
		t.Fatalf("client.InstanceAlive(ctx, %v) = %v, %s; want no error", req, got, err)
	}
}

func TestSwarmingInstanceAliveError(t *testing.T) {
	// This test will create a gomote instance and attempt to call InstanceAlive.
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
			wantCode:   codes.InvalidArgument,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			overrideID: true,
			gomoteID:   "xyz",
			wantCode:   codes.NotFound,
		},
		{
			desc:     "gomote is not owned by caller",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("user-x", "email-y")),
			wantCode: codes.PermissionDenied,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
			gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
			if tc.overrideID {
				gomoteID = tc.gomoteID
			}
			req := &protos.InstanceAliveRequest{
				GomoteId: gomoteID,
			}
			got, err := client.InstanceAlive(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.InstanceAlive(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

// listDirectory calls either ListDirectoryStreaming or ListDirectory.
func listDirectory(ctx context.Context, streaming bool, client protos.GomoteServiceClient, req *protos.ListDirectoryRequest) ([]*protos.ListDirectoryResponse, error) {
	if streaming {
		stream, err := client.ListDirectoryStreaming(ctx, req)
		if err != nil {
			return nil, err
		}
		var resps []*protos.ListDirectoryResponse
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			resps = append(resps, resp)
		}
		return resps, nil
	} else {
		resp, err := client.ListDirectory(ctx, req)
		if err != nil {
			return nil, err
		}
		return []*protos.ListDirectoryResponse{resp}, nil
	}
}

func TestSwarmingListDirectory(t *testing.T) {
	t.Run("single", func(t *testing.T) { testSwarmingListDirectory(t, false) })
	t.Run("streaming", func(t *testing.T) { testSwarmingListDirectory(t, true) })
}
func testSwarmingListDirectory(t *testing.T, streaming bool) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	if _, err := listDirectory(ctx, streaming, client, &protos.ListDirectoryRequest{
		GomoteId:  gomoteID,
		Directory: "/foo",
	}); err != nil {
		t.Fatalf("client.ListDirectory(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingListDirectoryError(t *testing.T) {
	t.Run("single", func(t *testing.T) { testSwarmingListDirectoryError(t, false) })
	t.Run("streaming", func(t *testing.T) { testSwarmingListDirectoryError(t, true) })
}
func testSwarmingListDirectoryError(t *testing.T, streaming bool) {
	// This test will create a gomote instance and attempt to call ListDirectory.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		gomoteID   string // Used iff overrideID is true.
		directory  string
		recursive  bool
		skipFiles  []string
		digest     bool
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
			desc:     "missing directory",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			wantCode: codes.InvalidArgument,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: true,
			gomoteID:   "chucky",
			directory:  "/foo",
			wantCode:   codes.NotFound,
		},
		{
			desc:       "wrong gomote id",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: false,
			directory:  "/foo",
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
			req := &protos.ListDirectoryRequest{
				GomoteId:  gomoteID,
				Directory: tc.directory,
				Recursive: false,
				SkipFiles: []string{},
				Digest:    false,
			}
			got, err := listDirectory(tc.ctx, streaming, client, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.ListDirectory(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

func TestSwarmingListInstance(t *testing.T) {
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	var want []*protos.Instance
	for i := 0; i < 3; i++ {
		want = append(want, &protos.Instance{
			GomoteId:    mustCreateSwarmingInstance(t, client, fakeIAP()),
			BuilderType: "gotip-linux-amd64-boringcrypto",
		})
	}
	mustCreateSwarmingInstance(t, client, fakeIAPWithUser("user-x", "uuid-user-x"))
	response, err := client.ListInstances(ctx, &protos.ListInstancesRequest{})
	if err != nil {
		t.Fatalf("client.ListInstances = nil, %s; want no error", err)
	}
	got := response.GetInstances()
	if diff := cmp.Diff(want, got, protocmp.Transform(), protocmp.IgnoreFields(&protos.Instance{}, "expires", "host_type")); diff != "" {
		t.Errorf("ListInstances() mismatch (-want, +got):\n%s", diff)
	}
}

func TestSwarmingReadTGZToURLError(t *testing.T) {
	// This test will create a gomote instance and attempt to call ReadTGZToURL.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		gomoteID   string // Used iff overrideID is true.
		directory  string
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
			wantCode:   codes.NotFound,
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
			req := &protos.ReadTGZToURLRequest{
				GomoteId:  gomoteID,
				Directory: tc.directory,
			}
			got, err := client.ReadTGZToURL(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.ReadTGZToURL(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

func TestSwarmingRemoveFiles(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	if _, err := client.RemoveFiles(ctx, &protos.RemoveFilesRequest{
		GomoteId: gomoteID,
		Paths:    []string{"temp_file.log"},
	}); err != nil {
		t.Fatalf("client.RemoveFiles(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingSignSSHKey(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	if _, err := client.SignSSHKey(ctx, &protos.SignSSHKeyRequest{
		GomoteId:     gomoteID,
		PublicSshKey: []byte(devCertCAPublic),
	}); err != nil {
		t.Fatalf("client.SignSSHKey(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingSignSSHKeyError(t *testing.T) {
	// This test will create a gomote instance and attempt to call SignSSHKey.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc          string
		ctx           context.Context
		overrideID    bool
		gomoteID      string // Used iff overrideID is true.
		publickSSHKey []byte
		wantCode      codes.Code
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
			wantCode:   codes.NotFound,
		},
		{
			desc:     "missing public key",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			wantCode: codes.InvalidArgument,
		},
		{
			desc:          "gomote does not exist",
			ctx:           access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID:    true,
			gomoteID:      "chucky",
			publickSSHKey: []byte(devCertCAPublic),
			wantCode:      codes.NotFound,
		},
		{
			desc:          "wrong gomote id",
			ctx:           access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID:    false,
			publickSSHKey: []byte(devCertCAPublic),
			wantCode:      codes.PermissionDenied,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
			gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
			if tc.overrideID {
				gomoteID = tc.gomoteID
			}
			req := &protos.SignSSHKeyRequest{
				GomoteId:     gomoteID,
				PublicSshKey: tc.publickSSHKey,
			}
			got, err := client.SignSSHKey(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.SignSSHKey(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

func TestSwarmingUploadFile(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	_ = mustCreateSwarmingInstance(t, client, fakeIAP())
	if _, err := client.UploadFile(ctx, &protos.UploadFileRequest{}); err != nil {
		t.Fatalf("client.UploadFile(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingUploadFileError(t *testing.T) {
	// This test will create a gomote instance and attempt to call UploadFile.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		filename   string
		wantCode   codes.Code
	}{
		{
			desc:     "unauthenticated request",
			ctx:      context.Background(),
			wantCode: codes.Unauthenticated,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
			_ = mustCreateSwarmingInstance(t, client, fakeIAP())
			req := &protos.UploadFileRequest{}
			got, err := client.UploadFile(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.UploadFile(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

func TestSwarmingRemoveFilesError(t *testing.T) {
	// This test will create a gomote instance and attempt to call RemoveFiles.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		gomoteID   string // Used iff overrideID is true.
		paths      []string
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
			desc:     "missing paths",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			paths:    []string{},
			wantCode: codes.InvalidArgument,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: true,
			gomoteID:   "chucky",
			paths:      []string{"file.a"},
			wantCode:   codes.NotFound,
		},
		{
			desc:       "wrong gomote id",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: false,
			paths:      []string{"file.a"},
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
			req := &protos.RemoveFilesRequest{
				GomoteId: gomoteID,
				Paths:    tc.paths,
			}
			got, err := client.RemoveFiles(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.RemoveFiles(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

// TODO(go.dev/issue/48737) add test for files on GCS
func TestSwarmingWriteFileFromURL(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Go is an open source programming language")
	}))
	defer ts.Close()
	if _, err := client.WriteFileFromURL(ctx, &protos.WriteFileFromURLRequest{
		GomoteId: gomoteID,
		Url:      ts.URL,
		Filename: "foo",
		Mode:     0777,
	}); err != nil {
		t.Fatalf("client.WriteFileFromURL(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingWriteFileFromURLError(t *testing.T) {
	// This test will create a gomote instance and attempt to call TestWriteFileFromURL.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		gomoteID   string // Used iff overrideID is true.
		url        string
		filename   string
		mode       uint32
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
			wantCode:   codes.NotFound,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: true,
			gomoteID:   "chucky",
			url:        "go.dev/dl/1_14.tar.gz",
			wantCode:   codes.NotFound,
		},
		{
			desc:       "wrong gomote id",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: false,
			url:        "go.dev/dl/1_14.tar.gz",
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
			req := &protos.WriteFileFromURLRequest{
				GomoteId: gomoteID,
				Url:      tc.url,
				Filename: tc.filename,
				Mode:     0,
			}
			got, err := client.WriteFileFromURL(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.WriteFileFromURL(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

func TestSwarmingWriteTGZFromURL(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	if _, err := client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  gomoteID,
		Directory: "foo",
		Url:       `https://go.dev/dl/go1.17.6.linux-amd64.tar.gz`,
	}); err != nil {
		t.Fatalf("client.WriteTGZFromURL(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingWriteTGZFromURLGomoteStaging(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteSwarmingTest(t, context.Background(), mockSwarmClientSimple())
	gomoteID := mustCreateSwarmingInstance(t, client, fakeIAP())
	if _, err := client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  gomoteID,
		Directory: "foo",
		Url:       fmt.Sprintf("https://storage.googleapis.com/%s/go1.17.6.linux-amd64.tar.gz?field=x", testBucketName),
	}); err != nil {
		t.Fatalf("client.WriteTGZFromURL(ctx, req) = response, %s; want no error", err)
	}
}

func TestSwarmingWriteTGZFromURLError(t *testing.T) {
	// This test will create a gomote instance and attempt to call TestWriteTGZFromURL.
	// If overrideID is set to true, the test will use a different gomoteID than
	// the one created for the test.
	testCases := []struct {
		desc       string
		ctx        context.Context
		overrideID bool
		gomoteID   string // Used iff overrideID is true.
		url        string
		directory  string
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
			desc:     "missing URL",
			ctx:      access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP()),
			wantCode: codes.InvalidArgument,
		},
		{
			desc:       "gomote does not exist",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: true,
			gomoteID:   "chucky",
			url:        "go.dev/dl/1_14.tar.gz",
			wantCode:   codes.NotFound,
		},
		{
			desc:       "wrong gomote id",
			ctx:        access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAPWithUser("foo", "bar")),
			overrideID: false,
			url:        "go.dev/dl/1_14.tar.gz",
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
			req := &protos.WriteTGZFromURLRequest{
				GomoteId:  gomoteID,
				Url:       tc.url,
				Directory: tc.directory,
			}
			got, err := client.WriteTGZFromURL(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.WriteTGZFromURL(ctx, %v) = %v, nil; want error", req, got)
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
		bucket:                  nil,
		buildlets:               &remote.SessionPool{},
		gceBucketName:           "",
		sshCertificateAuthority: nil,
		rendezvous:              rdv,
		swarmingClient:          msc,
		buildersClient:          &FakeBuildersClient{},
	}
	id := "task-123"
	errCh := make(chan error, 2)
	if _, err := ss.startNewSwarmingTask(ctx, id, map[string]string{"cipd_platform": "linux-amd64"}, &configProperties{}, &SwarmOpts{
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
	}, false); err == nil || !strings.Contains(err.Error(), "revdial.Dialer closed") {
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

type FakeBuildersClient struct{}

func (fbc *FakeBuildersClient) GetBuilder(ctx context.Context, in *buildbucketpb.GetBuilderRequest, opts ...grpc.CallOption) (*buildbucketpb.BuilderItem, error) {
	builders := map[string]bool{
		"gotip-linux-amd64-boringcrypto-test_only": true,
	}
	name := in.GetId().GetBuilder()
	_, ok := builders[name]
	if !ok {
		return nil, errors.New("builder type not found")
	}
	return &buildbucketpb.BuilderItem{
		Id: &buildbucketpb.BuilderID{
			Project: "golang",
			Bucket:  "ci-workers",
			Builder: name,
		},
		Config: &buildbucketpb.BuilderConfig{
			Name: name,
			Dimensions: []string{
				"cipd_platform:linux-amd64",
			},
		},
	}, nil
}

func (fbc *FakeBuildersClient) ListBuilders(ctx context.Context, in *buildbucketpb.ListBuildersRequest, opts ...grpc.CallOption) (*buildbucketpb.ListBuildersResponse, error) {
	makeBuilderItem := func(bucket string, builders ...string) []*buildbucketpb.BuilderItem {
		out := make([]*buildbucketpb.BuilderItem, 0, len(builders))
		for _, b := range builders {
			out = append(out, &buildbucketpb.BuilderItem{
				Id: &buildbucketpb.BuilderID{
					Project: "golang",
					Bucket:  bucket,
					Builder: b,
				},
				Config: &buildbucketpb.BuilderConfig{
					Name: b,
					Dimensions: []string{
						"cipd_platform:linux-amd64",
					},
					Properties: `{"mode": 0, "bootstrap_version":"latest"}`,
				},
			})
		}
		return out
	}
	var builders []*buildbucketpb.BuilderItem
	switch bucket := in.GetBucket(); bucket {
	case "ci-workers":
		builders = makeBuilderItem(bucket, "gotip-linux-amd64-boringcrypto", "gotip-linux-amd64-boringcrypto-test_only")
	case "ci":
		builders = makeBuilderItem(bucket, "gotip-linux-arm", "gotip-linux-amd64")
	default:
		builders = []*buildbucketpb.BuilderItem{}
	}
	out := &buildbucketpb.ListBuildersResponse{
		Builders:      builders,
		NextPageToken: "",
	}
	return out, nil
}
