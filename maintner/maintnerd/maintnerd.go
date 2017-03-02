// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The maintnerd command serves project maintainer data from Git,
// Github, and/or Gerrit.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"path"
	"strings"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/maintpb"
)

// TODO add a real mutation source
type nullMutation struct{}

func (n *nullMutation) GetMutations(ctx context.Context) <-chan *maintpb.Mutation {
	ch := make(chan *maintpb.Mutation)
	go func() {
		close(ch)
	}()
	return ch
}

var (
	listen      = flag.String("listen", ":6343", "listen address")
	watchGithub = flag.String("watch-github", "", "Comma separated list of owner/repo pairs to slurp")
	dataDir     = flag.String("data-dir", "", "Local directory to write protobuf files to")
)

func main() {
	flag.Parse()
	pairs := strings.Split(*watchGithub, ",")
	// TODO switch based on flags, for now only local file sync works
	logger := maintner.NewDiskMutationLogger(*dataDir)
	corpus := maintner.NewCorpus(logger)
	for _, pair := range pairs {
		splits := strings.SplitN(pair, "/", 2)
		if len(splits) != 2 || splits[1] == "" {
			log.Fatalf("Invalid github repo: %s. Should be 'owner/repo,owner2/repo2'", pair)
		}
		corpus.AddGithub(splits[0], splits[1], path.Join(os.Getenv("HOME"), ".github-issue-token"))
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	ln.Close() // TODO: use
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := corpus.Initialize(ctx, logger); err != nil {
		log.Fatal(err)
	}
	corpus.StartLogging()
	if err := corpus.Poll(ctx); err != nil {
		log.Fatal(err)
	}
}
