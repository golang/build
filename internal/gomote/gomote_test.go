// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package gomote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/coordinator/schedule"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
)

const testBucketName = "unit-testing-bucket"

func fakeGomoteServer(t *testing.T, ctx context.Context) protos.GomoteServiceServer {
	signer, err := ssh.ParsePrivateKey([]byte(devCertCAPrivate))
	if err != nil {
		t.Fatalf("unable to parse raw certificate authority private key into signer=%s", err)
	}
	return &Server{
		bucket:                  &fakeBucketHandler{bucketName: testBucketName},
		buildlets:               remote.NewSessionPool(ctx),
		gceBucketName:           testBucketName,
		scheduler:               schedule.NewFake(),
		sshCertificateAuthority: signer,
	}
}

func setupGomoteTest(t *testing.T, ctx context.Context) protos.GomoteServiceClient {
	lis, err := nettest.NewLocalListener("tcp")
	if err != nil {
		t.Fatalf("unable to create net listener: %s", err)
	}
	sopts := access.FakeIAPAuthInterceptorOptions()
	s := grpc.NewServer(sopts...)
	protos.RegisterGomoteServiceServer(s, fakeGomoteServer(t, ctx))
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

func TestAuthenticate(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	got, err := client.Authenticate(ctx, &protos.AuthenticateRequest{})
	if err != nil {
		t.Fatalf("client.Authenticate(ctx, request) = %v,  %s; want no error", got, err)
	}
}

func TestAuthenticateError(t *testing.T) {
	wantCode := codes.Unauthenticated
	client := setupGomoteTest(t, context.Background())
	_, err := client.Authenticate(context.Background(), &protos.AuthenticateRequest{})
	if status.Code(err) != wantCode {
		t.Fatalf("client.Authenticate(ctx, request) = _, %s; want %s", status.Code(err), wantCode)
	}
}

func TestAddBootstrap(t *testing.T) {
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	req := &protos.AddBootstrapRequest{
		GomoteId: gomoteID,
	}
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	got, err := client.AddBootstrap(ctx, req)
	if err != nil {
		t.Fatalf("client.AddBootstrap(ctx, %v) = %v, %s; want no error", req, got, err)
	}
}

func TestAddBootstrapError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestCreateInstance(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	req := &protos.CreateInstanceRequest{BuilderType: "linux-amd64"}
	client := setupGomoteTest(t, context.Background())
	stream, err := client.CreateInstance(ctx, req)
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

func TestCreateInstanceError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())

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

func TestInstanceAlive(t *testing.T) {
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	req := &protos.InstanceAliveRequest{
		GomoteId: gomoteID,
	}
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	got, err := client.InstanceAlive(ctx, req)
	if err != nil {
		t.Fatalf("client.InstanceAlive(ctx, %v) = %v, %s; want no error", req, got, err)
	}
}

func TestInstanceAliveError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestListDirectory(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	if _, err := client.ListDirectory(ctx, &protos.ListDirectoryRequest{
		GomoteId:  gomoteID,
		Directory: "/foo",
	}); err != nil {
		t.Fatalf("client.ListDirectory(ctx, req) = response, %s; want no error", err)
	}
}

func TestListDirectoryStreaming(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	stream, err := client.ListDirectoryStreaming(ctx, &protos.ListDirectoryRequest{
		GomoteId:  gomoteID,
		Directory: "/foo",
	})
	if err != nil {
		t.Fatalf("client.ListDirectoryStreaming(ctx, req) = response, %s; want no error", err)
	}
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv() = response, %s; want no error", err)
		}
	}
}

func TestListDirectoryError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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
			got, err := client.ListDirectory(tc.ctx, req)
			if err != nil && status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected error: %s; want %s", err, tc.wantCode)
			}
			if err == nil {
				t.Fatalf("client.RemoveFiles(ctx, %v) = %v, nil; want error", req, got)
			}
		})
	}
}

func TestListInstance(t *testing.T) {
	client := setupGomoteTest(t, context.Background())
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	var want []*protos.Instance
	for i := 0; i < 3; i++ {
		want = append(want, &protos.Instance{
			GomoteId:    mustCreateInstance(t, client, fakeIAP()),
			BuilderType: "linux-amd64",
		})
	}
	mustCreateInstance(t, client, fakeIAPWithUser("user-x", "uuid-user-x"))
	response, err := client.ListInstances(ctx, &protos.ListInstancesRequest{})
	if err != nil {
		t.Fatalf("client.ListInstances = nil, %s; want no error", err)
	}
	got := response.GetInstances()
	if diff := cmp.Diff(want, got, protocmp.Transform(), protocmp.IgnoreFields(&protos.Instance{}, "expires", "host_type")); diff != "" {
		t.Errorf("ListInstances() mismatch (-want, +got):\n%s", diff)
	}
}

func TestDestroyInstance(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	if _, err := client.DestroyInstance(ctx, &protos.DestroyInstanceRequest{
		GomoteId: gomoteID,
	}); err != nil {
		t.Fatalf("client.DestroyInstance(ctx, req) = response, %s; want no error", err)
	}
}

func TestDestroyInstanceError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestExecuteCommand(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestExecuteCommandError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestReadTGZToURLError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestRemoveFiles(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	if _, err := client.RemoveFiles(ctx, &protos.RemoveFilesRequest{
		GomoteId: gomoteID,
		Paths:    []string{"temp_file.log"},
	}); err != nil {
		t.Fatalf("client.RemoveFiles(ctx, req) = response, %s; want no error", err)
	}
}

func TestRemoveFilesError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestSignSSHKey(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	if _, err := client.SignSSHKey(ctx, &protos.SignSSHKeyRequest{
		GomoteId:     gomoteID,
		PublicSshKey: []byte(devCertCAPublic),
	}); err != nil {
		t.Fatalf("client.SignSSHKey(ctx, req) = response, %s; want no error", err)
	}
}

func TestSignSSHKeyError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestUploadFile(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	_ = mustCreateInstance(t, client, fakeIAP())
	if _, err := client.UploadFile(ctx, &protos.UploadFileRequest{}); err != nil {
		t.Fatalf("client.UploadFile(ctx, req) = response, %s; want no error", err)
	}
}

func TestUploadFileError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			_ = mustCreateInstance(t, client, fakeIAP())
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

// TODO(go.dev/issue/48737) add test for files on GCS
func TestWriteFileFromURL(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestWriteFileFromURLError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestWriteTGZFromURL(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	if _, err := client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  gomoteID,
		Directory: "foo",
		Url:       `https://go.dev/dl/go1.17.6.linux-amd64.tar.gz`,
	}); err != nil {
		t.Fatalf("client.WriteTGZFromURL(ctx, req) = response, %s; want no error", err)
	}
}

func TestWriteTGZFromURLGomoteStaging(t *testing.T) {
	ctx := access.FakeContextWithOutgoingIAPAuth(context.Background(), fakeIAP())
	client := setupGomoteTest(t, context.Background())
	gomoteID := mustCreateInstance(t, client, fakeIAP())
	if _, err := client.WriteTGZFromURL(ctx, &protos.WriteTGZFromURLRequest{
		GomoteId:  gomoteID,
		Directory: "foo",
		Url:       fmt.Sprintf("https://storage.googleapis.com/%s/go1.17.6.linux-amd64.tar.gz?field=x", testBucketName),
	}); err != nil {
		t.Fatalf("client.WriteTGZFromURL(ctx, req) = response, %s; want no error", err)
	}
}

func TestWriteTGZFromURLError(t *testing.T) {
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
			client := setupGomoteTest(t, context.Background())
			gomoteID := mustCreateInstance(t, client, fakeIAP())
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

func TestIsPrivilegedUser(t *testing.T) {
	in := "accounts.google.com:example@google.com"
	if !isPrivilegedUser(in) {
		t.Errorf("isPrivilagedUser(%q) = false; want true", in)
	}
}

func TestObjectFromURL(t *testing.T) {
	url := `https://storage.googleapis.com/example-bucket/cat.jpeg`
	bucket := "example-bucket"
	wantObject := "cat.jpeg"
	object, err := objectFromURL(bucket, url)
	if err != nil {
		t.Fatalf("urlToBucketObject(%q) = %q, %s; want %q, no error", url, object, err, wantObject)
	}
	if object != wantObject {
		t.Fatalf("urlToBucketObject(%q) = %q; want %q", url, object, wantObject)
	}
}

func TestObjectFromURLError(t *testing.T) {
	bucket := "example-bucket"
	object := "cat.jpeg"
	url := fmt.Sprintf("https://bunker.googleapis.com/%s/%s", bucket, object)
	got, err := objectFromURL(bucket, url)
	if err == nil {
		t.Fatalf("urlToBucketObject(url) = %q, nil; want \"\", error", got)
	}
}

func TestEmailToUser(t *testing.T) {
	testCases := []struct {
		desc  string
		email string
		want  string
	}{
		{"valid email", "accounts.google.com:example@gmail.com", "example"},
		{"valid email", "accounts.google.com:mary@google.com", "mary"},
		{"valid email", "accounts.google.com:george@funky.com", "george"},
		{"single digit local", "accounts.google.com:g@funky.com", "g"},
		{"single digit domain", "accounts.google.com:g@funky.com", "g"},
		{"multiple colon", "accounts.google.com:example@gmail.com:more-info", "example"},                        // while not desired, wont lead to a panic
		{"multiple at", "accounts.google.com:example@gmail.com:example@gmail.com", "example@gmail.com:example"}, // while not desired, wont lead to a panic
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got, err := emailToUser(tc.email); got != tc.want || err != nil {
				t.Errorf("emailToUser(%q) = %q, %s; want %q, no error", tc.email, got, err, tc.want)
			}
		})
	}
}

func TestEmailToUserError(t *testing.T) {
	testCases := []struct {
		desc  string
		email string
	}{
		{"no local", "accounts.google.com:@funky.com"},
		{"incorrect authority", "accountsxgoogleycom:george@funky.com"},
		{"hyphens authority", "accounts-google-com:george@funky.com"},
		{"no domain", "accounts.google.com:george@"},
		{"missing colon", "accounts.google.comxgeorge@a.b"},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got, err := emailToUser(tc.email); err == nil {
				t.Errorf("emailToUser(%q) = %q, no error; want error", tc.email, got)
			}
		})
	}
}

func fakeAuthContext(ctx context.Context, privileged bool) context.Context {
	iap := access.IAPFields{
		Email: "accounts.google.com:example@gmail.com",
		ID:    "accounts.google.com:randomuuidstuff",
	}
	if privileged {
		iap.Email = "accounts.google.com:test@google.com"
	}
	return access.ContextWithIAP(ctx, iap)
}

func fakeIAP() access.IAPFields {
	return fakeIAPWithUser("example", "randomuuidstuff")
}

func fakeIAPWithUser(user string, id string) access.IAPFields {
	return access.IAPFields{
		Email: fmt.Sprintf("accounts.google.com:%s@gmail.com", user),
		ID:    fmt.Sprintf("accounts.google.com:%s", id),
	}
}

func mustCreateInstance(t *testing.T, client protos.GomoteServiceClient, iap access.IAPFields) string {
	req := &protos.CreateInstanceRequest{
		BuilderType: "linux-amd64",
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

const (
	// devCertCAPrivate is a private SSH CA certificate to be used for development.
	devCertCAPrivate = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACCVd2FJ3Db/oV53iRDt1RLscTn41hYXbunuCWIlXze2WAAAAJhjy3ePY8t3
jwAAAAtzc2gtZWQyNTUxOQAAACCVd2FJ3Db/oV53iRDt1RLscTn41hYXbunuCWIlXze2WA
AAAEALuUJMb/rEaFNa+vn5RejeoBiiViyda7djgEvMnQ8fRJV3YUncNv+hXneJEO3VEuxx
OfjWFhdu6e4JYiVfN7ZYAAAAE3Rlc3R1c2VyQGdvbGFuZy5vcmcBAg==
-----END OPENSSH PRIVATE KEY-----`

	// devCertCAPublic is a public SSH CA certificate to be used for development.
	devCertCAPublic = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJV3YUncNv+hXneJEO3VEuxxOfjWFhdu6e4JYiVfN7ZY testuser@golang.org`
)

type fakeBucketHandler struct{ bucketName string }

func (fbc *fakeBucketHandler) GenerateSignedPostPolicyV4(object string, opts *storage.PostPolicyV4Options) (*storage.PostPolicyV4, error) {
	if object == "" || opts == nil {
		return nil, errors.New("invalid arguments")
	}
	return &storage.PostPolicyV4{
		URL: fmt.Sprintf("https://localhost/%s/%s", fbc.bucketName, object),
		Fields: map[string]string{
			"x-permission-to-post": "granted",
		},
	}, nil
}

func (fbc *fakeBucketHandler) SignedURL(object string, opts *storage.SignedURLOptions) (string, error) {
	if object == "" || opts == nil {
		return "", errors.New("invalid arguments")
	}
	return fmt.Sprintf("https://localhost/%s?X-Goog-Algorithm=GOOG4-yyy", object), nil
}

func (fbc *fakeBucketHandler) Object(name string) *storage.ObjectHandle {
	return &storage.ObjectHandle{}
}
