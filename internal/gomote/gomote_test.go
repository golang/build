// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package gomote

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/coordinator/schedule"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func fakeGomoteServer(ctx context.Context) protos.GomoteServiceServer {
	return &Server{
		buildlets: remote.NewSessionPool(ctx),
		scheduler: schedule.NewFake(),
	}
}

func setupGomoteTest(t *testing.T, ctx context.Context) protos.GomoteServiceClient {
	lis, err := nettest.NewLocalListener("tcp")
	if err != nil {
		t.Fatalf("unable to create net listener: %s", err)
	}
	sopts := access.FakeIAPAuthInterceptorOptions()
	s := grpc.NewServer(sopts...)
	protos.RegisterGomoteServiceServer(s, fakeGomoteServer(ctx))
	go s.Serve(lis)

	// create GRPC client
	copts := []grpc.DialOption{
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithTimeout(time.Second),
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
					t.Fatalf("unexpected error: %s", err)
				}
				return
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
