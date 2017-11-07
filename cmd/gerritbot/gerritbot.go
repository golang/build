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
	"os/user"
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
	workspace       = flag.String("workspace", defaultWorkspace(), "where git repos and temporary worktrees are created")
	githubTokenFile = flag.String("github-token-file", filepath.Join(defaultWorkspace(), "github-token"), "file to load GitHub token from; should only contain the token text")
	gerritTokenFile = flag.String("gerrit-token-file", filepath.Join(defaultWorkspace(), "gerrit-token"), "file to load Gerrit token from; should be of form <git-email>:<token>")
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

func defaultWorkspace() string {
	return filepath.Join(home(), ".gerritbot")
}

func home() string {
	h := os.Getenv("HOME")
	if h != "" {
		return h
	}
	u, err := user.Current()
	if err != nil {
		log.Fatalf("user.Current(): %v", err)
	}
	return u.HomeDir
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintln(w, "Hello, GerritBot! 🤖")
}

const (
	prefixGitFooterLastRev = "GitHub-Last-Rev"
	prefixGitFooterPR      = "GitHub-Pull-Request"
)

var (
	gitHubRepoWhitelist = map[string]bool{
		"golang/scratch": true,
	}
	gerritProjectWhitelist = map[string]bool{
		"scratch": true,
	}

	// mu protects the vars below it.
	mu          sync.RWMutex
	corpus      *maintner.Corpus
	importedPRs = map[string]*maintner.GerritCL{} // GitHub owner/repo#n -> Gerrit CL
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
	mu.Lock()
	defer mu.Unlock()
	corpus.Gerrit().ForeachProjectUnsorted(func(p *maintner.GerritProject) error {
		pname := p.Project()
		if !gerritProjectWhitelist[pname] {
			return nil
		}
		return p.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			prv := cl.Footer(prefixGitFooterPR)
			if prv == "" {
				return nil
			}
			importedPRs[prv] = cl
			return nil
		})
	})

	if err := corpus.GitHub().ForeachRepo(func(ghr *maintner.GitHubRepo) error {
		id := ghr.ID()
		if !gitHubRepoWhitelist[id.Owner+"/"+id.Repo] {
			return nil
		}
		return ghr.ForeachIssue(func(issue *maintner.GitHubIssue) error {
			if issue.Closed || !issue.PullRequest {
				return nil
			}
			return processPullRequest(id, issue)
		})
	}); err != nil {
		log.Printf("corpus.GitHub().ForeachRepo(...): %v", err)
	}
}

func processPullRequest(id maintner.GitHubRepoID, issue *maintner.GitHubIssue) error {
	log.Printf("Processing PR %s/%s#%d...", id.Owner, id.Repo, issue.Number)
	ctx := context.Background()
	issueNum := int(issue.Number)
	pr, err := pullRequestInfo(ctx, id, issueNum)
	if err != nil {
		return fmt.Errorf("pullRequestInfo(ctx, %q, %d): %v", id, issue.Number, err)
	}
	cl := importedPRs[gitHubPRValue(id, issueNum)]
	if cl == nil {
		// Import PR to a new Gerrit Change.
		return nil
	}
	if pr.GetCommits() != 1 {
		// Post message to GitHub (only once) saying to squash.
		return nil
	}
	lastRev := cl.Footer(prefixGitFooterLastRev)
	if lastRev == "" {
		log.Printf("Imported CL https://go-review.googlesource.com/q/%s does not have %s footer; skipping",
			cl.ChangeID(), prefixGitFooterLastRev)
		return nil
	}
	log.Printf("%+v", pr.Head.GetSHA())
	if pr.Head.GetSHA() == lastRev {
		log.Printf("Change https://go-review.googlesource.com/q/%s is up to date; nothing to do.",
			cl.ChangeID())
		// Nothing to do. Change is up to date.
		return nil
	}
	// Import PR to existing Gerrit Change.
	return nil
}

func pullRequestInfo(ctx context.Context, id maintner.GitHubRepoID, number int) (*github.PullRequest, error) {
	ghc, err := gitHubClient()
	if err != nil {
		return nil, fmt.Errorf("gitHubClient(): %v", err)
	}
	pr, _, err := ghc.PullRequests.Get(ctx, id.Owner, id.Repo, number)
	if err != nil {
		return nil, fmt.Errorf("ghc.PullRequests().Get(ctc, %q, %q, %d): %v",
			id.Owner, id.Repo, number, err)
	}
	return pr, nil
}

func gitHubPRValue(id maintner.GitHubRepoID, number int) string {
	return fmt.Sprintf("%s/%s#%d", id.Owner, id.Repo, number)
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
