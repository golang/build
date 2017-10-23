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
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/google/go-github/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/oauth2"
)

var (
	listen          = flag.String("listen", "localhost:6343", "listen address")
	autocertBucket  = flag.String("autocert-bucket", "", "if non-empty, listen on port 443 and serve a LetsEncrypt TLS cert using this Google Cloud Storage bucket as a cache")
	githubTokenFile = flag.String("github-token-file", filepath.Join(os.Getenv("HOME"), ".gerritbot", "github-token"), `File to load GitHub token from. File should only contain the token text`)
	gerritTokenFile = flag.String("gerrit-token-file", filepath.Join(os.Getenv("HOME"), ".gerritbot", "gerrit-token"), `File to load Gerrit token from. File should be of form <git-email>:<token>`)
)

func main() {
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
	if err := repo.ForeachIssue(func(issue *maintner.GitHubIssue) error {
		if issue.Closed || !issue.PullRequest {
			return nil
		}
		// TODO(andybons): Process Pull Request.
		return nil
	}); err != nil {
		log.Printf("repo.ForeachIssue(...): %v", err)
	}
}

func postGitHubMessage(ctx context.Context, owner, repo string, issueNum int, msg string) error {
	c, err := gitHubClient()
	if err != nil {
		return fmt.Errorf("getGitHubClient(): %v", err)
	}
	cmt := &github.IssueComment{Body: github.String(msg)}
	if _, _, err := c.Issues.CreateComment(ctx, owner, repo, issueNum, cmt); err != nil {
		return fmt.Errorf("c.Issues.CreateComment(ctx, %q, %q, %d, %+v): %v", owner, repo, issueNum, cmt, err)
	}
	return nil
}

func gitHubClient() (*github.Client, error) {
	token, err := gitHubToken()
	if err != nil {
		return nil, err
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return github.NewClient(tc), nil
}

func gitHubToken() (string, error) {
	if metadata.OnGCE() {
		token, err := metadata.ProjectAttributeValue("maintner-github-token")
		if err == nil {
			return token, nil
		}
	}
	slurp, err := ioutil.ReadFile(*githubTokenFile)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(slurp))
	if len(tok) == 0 {
		return "", fmt.Errorf("token from file %q cannot be empty", *githubTokenFile)
	}
	return tok, nil
}

// downloadRef calls the Gerrit API to retrieve the ref of the most recent
// patch set of the change with changeID.
func downloadRef(ctx context.Context, changeID string) (string, error) {
	c, err := gerritClient()
	if err != nil {
		return "", fmt.Errorf("getGerritClient(): %v", err)
	}
	opt := gerrit.QueryChangesOpt{Fields: []string{"CURRENT_REVISION"}}
	ch, err := c.GetChange(ctx, changeID, opt)
	if err != nil {
		return "", fmt.Errorf("c.GetChange(ctx, %q, %+v): %v", changeID, opt, err)
	}
	rev, ok := ch.Revisions[ch.CurrentRevision]
	if !ok {
		return "", fmt.Errorf("revisions[current_revision] is not present in %+v", ch)
	}
	return rev.Ref, nil
}

func gerritClient() (*gerrit.Client, error) {
	username, token, err := gerritAuth()
	if err != nil {
		return nil, err
	}
	c := gerrit.NewClient("https://go-review.googlesource.com", gerrit.BasicAuth(username, token))
	return c, nil
}

func gerritAuth() (string, string, error) {
	var slurp string
	if metadata.OnGCE() {
		var err error
		slurp, err = metadata.ProjectAttributeValue("gobot-password")
		if err != nil {
			log.Printf(`Error retrieving Project Metadata "gobot-password": %v`, err)
		}
	}
	if len(slurp) == 0 {
		slurpBytes, err := ioutil.ReadFile(*gerritTokenFile)
		if err != nil {
			return "", "", err
		}
		slurp = string(slurpBytes)
	}
	f := strings.SplitN(strings.TrimSpace(slurp), ":", 2)
	if len(f) == 1 {
		// Assume the whole thing is the token.
		return "git-gobot.golang.org", f[0], nil
	}
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", "", fmt.Errorf("expected Gerrit token to be of form <git-email>:<token>")
	}
	return f[0], f[1], nil
}
