// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package gomote

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/swarmclient"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testSwarmingBucketName = "unit-testing-bucket-swarming"

func fakeGomoteSwarmingServer(t *testing.T, ctx context.Context, configClient *swarmclient.ConfigClient) protos.GomoteServiceServer {
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
	}
}

func setupGomoteSwarmingTest(t *testing.T, ctx context.Context) protos.GomoteServiceClient {
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
	sopts := access.FakeIAPAuthInterceptorOptions()
	s := grpc.NewServer(sopts...)
	protos.RegisterGomoteServiceServer(s, fakeGomoteSwarmingServer(t, ctx, configClient))
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
	client := setupGomoteSwarmingTest(t, context.Background())
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
	client := setupGomoteSwarmingTest(t, context.Background())
	req := &protos.ListSwarmingBuildersRequest{}
	got, err := client.ListSwarmingBuilders(context.Background(), req)
	if err != nil && status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unexpected error: %s; want %s", err, codes.Unauthenticated)
	}
	if err == nil {
		t.Fatalf("client.ListSwarmingBuilder(ctx, %v) = %v, nil; want error", req, got)
	}
}
