// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package gomote

import (
	"context"
	"fmt"
	"log"
	"strings"

	"cloud.google.com/go/storage"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/swarmclient"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SwarmingServer is a gomote server implementation which supports LUCI swarming bots.
type SwarmingServer struct {
	// embed the unimplemented server.
	protos.UnimplementedGomoteServiceServer

	bucket                  bucketHandle
	buildlets               *remote.SessionPool
	gceBucketName           string
	luciConfigClient        *swarmclient.ConfigClient
	sshCertificateAuthority ssh.Signer
}

// NewSwarming creates a gomote server. If the rawCAPriKey is invalid, the program will exit.
func NewSwarming(rsp *remote.SessionPool, rawCAPriKey []byte, gomoteGCSBucket string, storageClient *storage.Client, configClient *swarmclient.ConfigClient) (*SwarmingServer, error) {
	signer, err := ssh.ParsePrivateKey(rawCAPriKey)
	if err != nil {
		return nil, fmt.Errorf("unable to parse raw certificate authority private key into signer=%w", err)
	}
	return &SwarmingServer{
		bucket:                  storageClient.Bucket(gomoteGCSBucket),
		buildlets:               rsp,
		gceBucketName:           gomoteGCSBucket,
		luciConfigClient:        configClient,
		sshCertificateAuthority: signer,
	}, nil
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
