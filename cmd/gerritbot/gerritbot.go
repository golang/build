// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gerritbot binary converts GitHub Pull Requests to Gerrit Changes,
// updating the PR and Gerrit Change as appropriate.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/build/internal/https"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
)

func main() {
	var (
		listen         = flag.String("listen", "localhost:6343", "listen address")
		autocertBucket = flag.String("autocert-bucket", "", "if non-empty, listen on port 443 and serve a LetsEncrypt TLS cert using this Google Cloud Storage bucket as a cache")
	)
	flag.Parse()

	ctx := context.Background()
	initCorpus(ctx)
	go corpusUpdateLoop(ctx)

	https.ListenAndServe(http.HandlerFunc(handleIndex), &https.Options{
		Addr:                *listen,
		AutocertCacheBucket: *autocertBucket,
	})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, "Hello, GerritBot! 🤖")
}

var (
	mu     sync.RWMutex
	corpus *maintner.Corpus
)

// initCorpus fetches a full maintner corpus, overwriting any existing data.
func initCorpus(ctx context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	var err error
	corpus, err = godata.Get(ctx)
	if err != nil {
		return fmt.Errorf("godata.Get: %v", err)
	}
	return nil
}

// corpusUpdateLoop continuously updates the server’s corpus until ctx’s Done
// channel is closed.
func corpusUpdateLoop(ctx context.Context) {
	log.Println("Starting corpus update loop ...")
	for {
		checkPullRequests()
		err := corpus.UpdateWithLocker(ctx, &mu)
		if err != nil {
			if err == maintner.ErrSplit {
				log.Println("Corpus out of sync. Re-fetching corpus.")
				initCorpus(ctx)
			} else {
				log.Printf("corpus.Update: %v; sleeping 15s", err)
				time.Sleep(15 * time.Second)
				continue
			}
		}

		select {
		case <-ctx.Done():
			return
		default:
			continue
		}
	}
}

func checkPullRequests() {
	mu.RLock()
	defer mu.RUnlock()
	repo := corpus.GitHub().Repo("golang", "scratch")
	repo.ForeachIssue(func(issue *maintner.GitHubIssue) error {
		if issue.Closed || !issue.PullRequest {
			return nil
		}
		return processPullRequest(issue)
	})
}

// processPullRequest requires mu to be held.
func processPullRequest(pr *maintner.GitHubIssue) error {
	return nil
}
