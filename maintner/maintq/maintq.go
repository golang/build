// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The maintq command queries a maintnerd gRPC server.
// This tool is mostly for debugging.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/build/maintner/maintnerd/apipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/prototext"
)

var (
	server = flag.String("server", "maintner.golang.org:443", "maintnerd server")
)

var (
	mc  apipb.MaintnerServiceClient
	ctx = context.Background()
)

func main() {
	flag.Parse()

	c := credentials.NewTLS(&tls.Config{
		NextProtos:         []string{"h2"},
		InsecureSkipVerify: strings.HasPrefix(*server, "localhost:"),
	})
	opts := []grpc.DialOption{
		grpc.WithDisableRetry(),
		grpc.WithBlock(),
		grpc.WithTimeout(5 * time.Second),
		grpc.WithTransportCredentials(c),
	}

	cc, err := grpc.Dial(*server, opts...)
	if err != nil {
		log.Fatalf("unable to grpc.Dial(%q) = %s", *server, err)
	}
	mc = apipb.NewMaintnerServiceClient(cc)

	cmdFunc := map[string]func(args []string) error{
		"has-ancestor":  callHasAncestor,
		"get-ref":       callGetRef,
		"try-work":      callTryWork,
		"list-releases": callListReleases,
		"get-dashboard": callGetDashboard,
	}
	log.SetFlags(0)
	if flag.NArg() == 0 || cmdFunc[flag.Arg(0)] == nil {
		var cmds []string
		for cmd := range cmdFunc {
			cmds = append(cmds, cmd)
		}
		sort.Strings(cmds)
		log.Fatalf(`Usage: maintq %v ...`, cmds)
	}
	if err := cmdFunc[flag.Arg(0)](flag.Args()[1:]); err != nil {
		log.Fatal(err)
	}
}

func callHasAncestor(args []string) error {
	if len(args) != 2 {
		return errors.New("Usage: maintq has-ancestor <commit> <ancestor>")
	}
	res, err := mc.HasAncestor(ctx, &apipb.HasAncestorRequest{
		Commit:   args[0],
		Ancestor: args[1],
	})
	if err != nil {
		return err
	}
	fmt.Println(res)
	return nil
}

func callGetRef(args []string) error {
	if len(args) != 2 {
		return errors.New("Usage: maintq get-ref <project> <ref>")
	}
	res, err := mc.GetRef(ctx, &apipb.GetRefRequest{
		GerritServer:  "go.googlesource.com",
		GerritProject: args[0],
		Ref:           args[1],
	})
	if err != nil {
		return err
	}
	if res.Value == "" {
		return errors.New("ref not found")
	}
	fmt.Println(res.Value)
	return nil
}

func callTryWork(args []string) error {
	staging := len(args) == 1 && args[0] == "staging"
	if !staging && len(args) > 0 {
		return errors.New(`Usage: maintq try-work ["staging"]  # prod is default`)
	}
	res, err := mc.GoFindTryWork(ctx, &apipb.GoFindTryWorkRequest{ForStaging: staging})
	if err != nil {
		return err
	}
	fmt.Print(prototext.Format(res))
	return nil
}

func callListReleases(args []string) error {
	if len(args) != 0 {
		return errors.New("Usage: maintq list-releases")
	}
	res, err := mc.ListGoReleases(ctx, &apipb.ListGoReleasesRequest{})
	if err != nil {
		return err
	}
	fmt.Print(prototext.Format(res))
	return nil
}

func callGetDashboard(args []string) error {
	req := &apipb.DashboardRequest{}

	fs := flag.NewFlagSet("get-dash-commits", flag.ExitOnError)
	var page int
	fs.IntVar(&page, "page", 0, "0-based page number")
	fs.StringVar(&req.Branch, "branch", "", "branch name; empty means master")
	fs.StringVar(&req.Repo, "repo", "", "repo name; empty means the main repo, otherwise \"golang.org/*\"")
	fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}

	req.Page = int32(page)

	res, err := mc.GetDashboard(ctx, req)
	if err != nil {
		return err
	}
	fmt.Print(prototext.Format(res))
	return nil
}
