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
	"path/filepath"
	"runtime"
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
	watchGithub = flag.String("watch-github", "", "Comma-separated list of owner/repo pairs to slurp")
	watchGoGit  = flag.Bool("watch-go-git", false, "Watch Go's main git repo.")
	dataDir     = flag.String("data-dir", "", "Local directory to write protobuf files to")
	debug       = flag.Bool("debug", false, "Print debug logging information")
)

func main() {
	flag.Parse()
	if *dataDir == "" {
		*dataDir = filepath.Join(os.Getenv("HOME"), "var", "maintnerd")
		if err := os.MkdirAll(*dataDir, 0755); err != nil {
			log.Fatal(err)
		}
		log.Printf("Storing data in implicit directory %s", *dataDir)
	}
	// TODO switch based on flags, for now only local file sync works
	logger := maintner.NewDiskMutationLogger(*dataDir)
	corpus := maintner.NewCorpus(logger)
	if *debug {
		corpus.SetDebug()
	}
	if *watchGithub != "" {
		for _, pair := range strings.Split(*watchGithub, ",") {
			splits := strings.SplitN(pair, "/", 2)
			if len(splits) != 2 || splits[1] == "" {
				log.Fatalf("Invalid github repo: %s. Should be 'owner/repo,owner2/repo2'", pair)
			}
			corpus.AddGithub(splits[0], splits[1], path.Join(os.Getenv("HOME"), ".github-issue-token"))
		}
	}
	if *watchGoGit {
		// Assumes GOROOT is a git checkout. Good enough for now for development.
		corpus.AddGoGitRepo("go", runtime.GOROOT())
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	ln.Close() // TODO: use
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := corpus.Initialize(ctx, logger); err != nil {
		// TODO: if Initialize only partially syncs the data, we need to delete
		// whatever files it created, since Github returns events newest first
		// and we use the issue updated dates to check whether we need to keep
		// syncing.
		log.Fatal(err)
	}
	corpus.StartLogging()
	if err := corpus.Poll(ctx); err != nil {
		log.Fatal(err)
	}
	log.Fatalf("Exiting.")
}
