// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"strings"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"go.chromium.org/luci/auth"
	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/grpc/prpc"
	"go.chromium.org/luci/hardcoded/chromeinfra"
	"go.chromium.org/luci/swarming/client/swarming"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/internal/access"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/gomote"
	gomotepb "golang.org/x/build/internal/gomote/protos"
	"golang.org/x/build/internal/gomoteserver/ui"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/rendezvous"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/revdial/v2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
)

var (
	sshAddr      = flag.String("ssh_addr", ":2222", "Address the gomote SSH server should listen on")
	buildEnvName = flag.String("env", "", "The build environment configuration to use. Not required if running in dev mode locally or prod mode on GCE.")
	mode         = flag.String("mode", "", "Valid modes are 'dev', 'prod', or '' for auto-detect. dev means localhost development, not be confused with staging on go-dashboard-dev, which is still the 'prod' mode.")
)

var Version string // set by linker -X

const (
	gomoteHost    = "gomote.golang.org"
	gomoteSSHHost = "gomotessh.golang.org"
)

func main() {
	https.RegisterFlags(flag.CommandLine)
	if err := secret.InitFlagSupport(context.Background()); err != nil {
		log.Fatalln(err)
	}
	hostKey := secret.Flag("private-host-key", "Gomote SSH Server host private key")
	pubKey := secret.Flag("public-host-key", "Gomote SSH Server host public key")
	flag.Parse()

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
		luciEnv := buildenv.ByProjectID("golang-ci-luci")
		env := buildenv.ByProjectID(projectID)
		gomoteBucket = luciEnv.GomoteTransferBucket
		var coordinatorBackend, serviceID = "coordinator-internal-iap", ""
		if serviceID = env.IAPServiceID(coordinatorBackend); serviceID == "" {
			log.Fatalf("unable to retrieve Service ID for backend service=%q", coordinatorBackend)
		}
		opts = append(opts, grpc.UnaryInterceptor(access.RequireIAPAuthUnaryInterceptor(access.IAPSkipAudienceValidation)))
		opts = append(opts, grpc.StreamInterceptor(access.RequireIAPAuthStreamInterceptor(access.IAPSkipAudienceValidation)))
	}
	grpcServer := grpc.NewServer(opts...)
	rdv := rendezvous.New(ctx)
	gomoteServer, err := gomote.NewSwarming(sp, sshCA, gomoteBucket, mustStorageClient(), rdv, mustSwarmingClient(ctx), mustBuildersClient(ctx))
	if err != nil {
		log.Fatalf("unable to create gomote server: %s", err)
	}
	gomotepb.RegisterGomoteServiceServer(grpcServer, gomoteServer)

	mux := http.NewServeMux()
	mux.HandleFunc("/reverse", rdv.HandleReverse)
	mux.Handle("/revdial", revdial.ConnHandler())
	mux.HandleFunc("/style.css", ui.Redirect(ui.HandleStyleCSS, gomoteSSHHost, gomoteHost))
	mux.HandleFunc("/", ui.Redirect(grpcHandlerFunc(grpcServer, ui.HandleStatusFunc(sp, Version)), gomoteSSHHost, gomoteHost)) // Serve a status page.

	sshServ, err := remote.NewSSHServer(*sshAddr, []byte(*hostKey), []byte(*pubKey), sshCA, sp, remote.EnableLUCIOption())
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

func mustSwarmingClient(ctx context.Context) swarming.Client {
	c, err := swarming.NewClient(ctx, swarming.ClientOptions{
		ServiceURL: "https://chromium-swarm.appspot.com",
		UserAgent:  "go-gomoteserver",
		Auth:       auth.Options{Method: auth.GCEMetadataMethod},
	})
	if err != nil {
		log.Fatalf("unable to create swarming client: %s", err)
	}
	return c
}

func mustBuildersClient(ctx context.Context) buildbucketpb.BuildersClient {
	httpC, err := auth.NewAuthenticator(ctx, auth.SilentLogin, auth.Options{Method: auth.GCEMetadataMethod}).Client()
	if err != nil {
		log.Fatalf("unable to create buildbucket authenticator: %s", err)
	}
	prpcC := prpc.Client{C: httpC, Host: chromeinfra.BuildbucketHost}
	return buildbucketpb.NewBuildersClient(&prpcC)
}
