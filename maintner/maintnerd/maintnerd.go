// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The maintnerd command serves project maintainer data from Git,
// Github, and/or Gerrit.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/build/maintner"
)

var (
	listen        = flag.String("listen", "localhost:6343", "listen address")
	watchGithub   = flag.String("watch-github", "", "Comma-separated list of owner/repo pairs to slurp")
	watchGoGit    = flag.Bool("watch-go-git", false, "Watch Go's main git repo.")
	dataDir       = flag.String("data-dir", "", "Local directory to write protobuf files to")
	debug         = flag.Bool("debug", false, "Print debug logging information")
	stopAfterLoad = flag.Bool("stop-after-load", false, "Debug: stop after loading all old data; don't poll for new data.")

	// Temporary:
	qTopFiles = flag.Bool("query-top-changed-files", false, "[temporary demo flag] If true, show the most modified git files and then exit instead of polling. This will be removed when this becomes a gRPC server.")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Now()
	if err := corpus.Initialize(ctx, logger); err != nil {
		// TODO: if Initialize only partially syncs the data, we need to delete
		// whatever files it created, since Github returns events newest first
		// and we use the issue updated dates to check whether we need to keep
		// syncing.
		log.Fatal(err)
	}
	initDur := time.Since(t0)

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	log.Printf("Loaded data in %v. Memory: %v MB", initDur, ms.HeapAlloc>>20)

	if *stopAfterLoad {
		return
	}

	if *qTopFiles {
		top := corpus.QueryFrequentlyModifiedFiles(25)
		for _, fc := range top {
			fmt.Printf(" %5d %s\n", fc.Count, fc.File)
		}
		return
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	ln.Close() // TODO: use

	corpus.StartLogging()
	if err := corpus.Poll(ctx); err != nil {
		log.Fatal(err)
	}
	log.Fatalf("Exiting.")
}
