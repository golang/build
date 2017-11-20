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
	workdir         = flag.String("workdir", defaultWorkdir(), "where git repos and temporary worktrees are created")
	githubTokenFile = flag.String("github-token-file", filepath.Join(defaultWorkdir(), "github-token"), "file to load GitHub token from; should only contain the token text")
	gerritTokenFile = flag.String("gerrit-token-file", filepath.Join(defaultWorkdir(), "gerrit-token"), "file to load Gerrit token from; should be of form <git-email>:<token>")
)

func main() {
	flag.Parse()

	ctx := context.Background()
	b := newBot()
	b.initCorpus(ctx)
	go b.corpusUpdateLoop(ctx)

	https.ListenAndServe(http.HandlerFunc(handleIndex), &https.Options{
		Addr:                *listen,
		AutocertCacheBucket: *autocertBucket,
	})
}

func defaultWorkdir() string {
	// TODO(andybons): Use os.UserCacheDir (issue 22536) when it's available.
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
	// Footer that contains the last revision from GitHub that was successfully
	// imported to Gerrit.
	prefixGitFooterLastRev = "GitHub-Last-Rev"

	// Footer containing the GitHub PR associated with the Gerrit Change.
	prefixGitFooterPR = "GitHub-Pull-Request"
)

var (
	// GitHub repos we accept PRs for, mirroring them into Gerrit CLs.
	githubRepoWhitelist = map[string]bool{
		"golang/scratch": true,
	}
	// Gerrit projects we accept PRs for.
	gerritProjectWhitelist = map[string]bool{
		"scratch": true,
	}
)

type bot struct {
	sync.RWMutex
	corpus      *maintner.Corpus
	importedPRs map[string]*maintner.GerritCL // GitHub owner/repo#n -> Gerrit CL
}

func newBot() *bot {
	return &bot{importedPRs: map[string]*maintner.GerritCL{}}
}

// initCorpus fetches a full maintner corpus, overwriting any existing data.
func (b *bot) initCorpus(ctx context.Context) error {
	b.Lock()
	defer b.Unlock()
	var err error
	b.corpus, err = godata.Get(ctx)
	if err != nil {
		return fmt.Errorf("godata.Get: %v", err)
	}
	return nil
}

// corpusUpdateLoop continuously updates the server’s corpus until ctx’s Done
// channel is closed.
func (b *bot) corpusUpdateLoop(ctx context.Context) {
	log.Println("Starting corpus update loop ...")
	for {
		b.checkPullRequests()
		err := b.corpus.UpdateWithLocker(ctx, &b.RWMutex)
		if err != nil {
			if err == maintner.ErrSplit {
				log.Println("Corpus out of sync. Re-fetching corpus.")
				b.initCorpus(ctx)
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

func (b *bot) checkPullRequests() {
	b.Lock()
	defer b.Unlock()
	b.corpus.Gerrit().ForeachProjectUnsorted(func(p *maintner.GerritProject) error {
		pname := p.Project()
		if !gerritProjectWhitelist[pname] {
			return nil
		}
		return p.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			prv := cl.Footer(prefixGitFooterPR)
			if prv == "" {
				return nil
			}
			b.importedPRs[prv] = cl
			return nil
		})
	})

	if err := b.corpus.GitHub().ForeachRepo(func(ghr *maintner.GitHubRepo) error {
		id := ghr.ID()
		if !githubRepoWhitelist[id.Owner+"/"+id.Repo] {
			return nil
		}
		return ghr.ForeachIssue(func(issue *maintner.GitHubIssue) error {
			if issue.Closed || !issue.PullRequest {
				return nil
			}
			return b.processPullRequest(&prInfo{
				owner:  id.Owner,
				repo:   id.Repo,
				number: int(issue.Number),
			})
		})
	}); err != nil {
		log.Printf("corpus.GitHub().ForeachRepo(...): %v", err)
	}
}

type prInfo struct {
	owner, repo string
	number      int
}

// String returns text referencing a Pull Request that will be automatically
// converted into a link by GitHub.
func (i *prInfo) String() string {
	return fmt.Sprintf("%s/%s#%d", i.owner, i.repo, i.number)
}

// processPullRequest is the entry point to the state machine of mirroring a PR
// with Gerrit. PRs that are up to date with their respective Gerrit changes are
// skipped, and any with a squashed commit SHA unequal to its Gerrit equivalent
// are imported. Those that have no associated Gerrit changes will result in one
// being created.
// TODO(andybons): Document comment mirroring once that is implemented.
// b's RWMutex read-write lock must be held.
func (b *bot) processPullRequest(info *prInfo) error {
	ctx := context.Background()
	log.Printf("Processing PR %s...", info)
	pr, err := getFullPR(ctx, info)
	if err != nil {
		return fmt.Errorf("getFullPR(ctx, %q): %v", info, err)
	}
	cl := b.importedPRs[info.String()]
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

func getFullPR(ctx context.Context, info *prInfo) (*github.PullRequest, error) {
	ghc, err := githubClient()
	if err != nil {
		return nil, fmt.Errorf("githubClient(): %v", err)
	}
	pr, _, err := ghc.PullRequests.Get(ctx, info.owner, info.repo, info.number)
	if err != nil {
		return nil, fmt.Errorf("ghc.PullRequests().Get(ctc, %q, %q, %d): %v",
			info.owner, info.repo, info.number, err)
	}
	return pr, nil
}

func postGitHubMessage(ctx context.Context, owner, repo string, issueNum int, msg string) error {
	c, err := githubClient()
	if err != nil {
		return fmt.Errorf("getGitHubClient(): %v", err)
	}
	cmt := &github.IssueComment{Body: github.String(msg)}
	if _, _, err := c.Issues.CreateComment(ctx, owner, repo, issueNum, cmt); err != nil {
		return fmt.Errorf("c.Issues.CreateComment(ctx, %q, %q, %d, %+v): %v", owner, repo, issueNum, cmt, err)
	}
	return nil
}

func githubClient() (*github.Client, error) {
	token, err := githubToken()
	if err != nil {
		return nil, err
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return github.NewClient(tc), nil
}

func githubToken() (string, error) {
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
