// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.20 && (linux || darwin)

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote"
	gomotepb "golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/rendezvous"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/swarmclient"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
)

var (
	sshAddr      = flag.String("ssh_addr", ":2222", "Address the gomote SSH server should listen on")
	buildEnvName = flag.String("env", "", "The build environment configuration to use. Not required if running in dev mode locally or prod mode on GCE.")
	mode         = flag.String("mode", "", "Valid modes are 'dev', 'prod', or '' for auto-detect. dev means localhost development, not be confused with staging on go-dashboard-dev, which is still the 'prod' mode.")
)

func main() {
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()
	if err := secret.InitFlagSupport(context.Background()); err != nil {
		log.Fatalln(err)
	}
	log.Println("starting gomote server")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sp := remote.NewSessionPool(context.Background())
	sshCA := mustRetrieveSSHCertificateAuthority()

	var gomoteBucket string
	var opts []grpc.ServerOption
	if *buildEnvName == "" && *mode != "dev" && metadata.OnGCE() {
		projectID, err := metadata.ProjectID()
		if err != nil {
			log.Fatalf("metadata.ProjectID() = %v", err)
		}
		env := buildenv.ByProjectID(projectID)
		gomoteBucket = env.GomoteTransferBucket
		var coordinatorBackend, serviceID = "coordinator-internal-iap", ""
		if serviceID = env.IAPServiceID(coordinatorBackend); serviceID == "" {
			log.Fatalf("unable to retrieve Service ID for backend service=%q", coordinatorBackend)
		}
		opts = append(opts, grpc.UnaryInterceptor(access.RequireIAPAuthUnaryInterceptor(access.IAPSkipAudienceValidation)))
		opts = append(opts, grpc.StreamInterceptor(access.RequireIAPAuthStreamInterceptor(access.IAPSkipAudienceValidation)))
	}
	grpcServer := grpc.NewServer(opts...)
	gomoteServer, err := gomote.NewSwarming(sp, sshCA, gomoteBucket, mustStorageClient(), mustLUCIConfigClient())
	if err != nil {
		log.Fatalf("unable to create gomote server: %s", err)
	}
	gomotepb.RegisterGomoteServiceServer(grpcServer, gomoteServer)
	rdv := rendezvous.New(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/reverse", rdv.HandleReverse)
	mux.HandleFunc("/", grpcHandlerFunc(grpcServer, handleStatus)) // Serve a status page.

	configureSSHServer := func() (*remote.SSHServer, error) {
		// Always generate a gomote SSH key pair. If the server is restarted then the existing
		// instances will be destroyed and a new pair of keys can be used.
		priKey, pubKey, err := remote.SSHKeyPair()
		if err != nil {
			return nil, fmt.Errorf("unable to generate development SSH key pair: %s", err)
		}
		return remote.NewSSHServer(*sshAddr, priKey, pubKey, sshCA, sp)
	}
	sshServ, err := configureSSHServer()
	if err != nil {
		log.Printf("unable to configure SSH server: %s", err)
	} else {
		go func() {
			log.Printf("running SSH server on %s", *sshAddr)
			err := sshServ.ListenAndServe()
			log.Printf("SSH server ended with error: %v", err)
		}()
		defer func() {
			err := sshServ.Close()
			if err != nil {
				log.Printf("unable to close SSH server: %s", err)
			}
		}()
	}
	log.Fatalln(https.ListenAndServe(context.Background(), mux))
}

func mustLUCIConfigClient() *swarmclient.ConfigClient {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := swarmclient.NewConfigClient(ctx)
	if err != nil {
		log.Fatalf("unable to create LUCI config client: %s", err)
	}
	return c
}

func mustRetrieveSSHCertificateAuthority() (privateKey []byte) {
	privateKey, _, err := remote.SSHKeyPair()
	if err != nil {
		log.Fatalf("unable to create SSH CA cert: %s", err)
	}
	return privateKey
}

func mustStorageClient() *storage.Client {
	if metadata.OnGCE() {
		sc, err := pool.StorageClient(context.Background())
		if err != nil {
			log.Fatalf("unable to create authenticated storage client: %v", err)
		}
		return sc
	}
	sc, err := storage.NewClient(context.Background(), option.WithoutAuthentication())
	if err != nil {
		log.Fatalf("unable to create unauthenticated storage client: %s", err)
	}
	return sc
}

func fromSecret(ctx context.Context, sc *secret.Client, secretName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sc.Retrieve(ctx, secretName)
}

// grpcHandlerFunc creates handler which intercepts requests intended for a GRPC server and directs the calls to the server.
// All other requests are directed toward the passed in handler.
func grpcHandlerFunc(gs *grpc.Server, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gs.ServeHTTP(w, r)
			return
		}
		h(w, r)
	}
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "gomote status page placeholder")
}
