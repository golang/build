// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package maintner mirrors, searches, syncs, and serves Git, Github,
// and Gerrit metadata.
//
// Maintner is short for "Maintainer". This package is intended for
// use by many tools. The name of the daemon that serves the maintner
// data to other tools is "maintnerd".
package maintner

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/google/go-github/github"
	"golang.org/x/build/maintner/maintpb"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
)

// Corpus holds all of a project's metadata.
//
// There are two main phases to the Corpus: the catch-up phase, when the Corpus
// is populated from a MutationSource (disk, database), and the polling phase,
// when the Corpus polls for new events and stores/writes them to disk. Call
// StartLogging between the catch-up phase and the polling phase.
type Corpus struct {
	// ... TODO

	mu           sync.RWMutex
	githubIssues map[githubRepo]map[int32]*githubIssue // repo -> num -> issue
	githubUsers  map[int64]*githubUser
	githubRepos  []repoObj
	// If true, log new commits
	shouldLog bool
}

type repoObj struct {
	name      githubRepo
	tokenFile string
}

func NewCorpus() *Corpus {
	return &Corpus{
		githubIssues: make(map[githubRepo]map[int32]*githubIssue),
		githubUsers:  make(map[int64]*githubUser),
		githubRepos:  []repoObj{},
	}
}

// StartLogging indicates that further changes should be written to the log.
func (c *Corpus) StartLogging() {
	c.mu.Lock()
	c.shouldLog = true
	c.mu.Unlock()
}

func (c *Corpus) AddGithub(owner, repo, tokenFile string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.githubRepos = append(c.githubRepos, repoObj{
		name:      githubRepo(owner + "/" + repo),
		tokenFile: tokenFile,
	})
}

// githubRepo is a github org & repo, lowercase, joined by a '/',
// such as "golang/go".
type githubRepo string

// Org finds "golang" in the githubRepo string "golang/go", or returns an empty
// string if it is malformed.
func (gr githubRepo) Org() string {
	sep := strings.IndexByte(string(gr), '/')
	if sep == -1 {
		return ""
	}
	return string(gr[:sep])
}

func (gr githubRepo) Repo() string {
	sep := strings.IndexByte(string(gr), '/')
	if sep == -1 || sep == len(gr)-1 {
		return ""
	}
	return string(gr[sep+1:])
}

// githubUser represents a github user.
// It is a subset of https://developer.github.com/v3/users/#get-a-single-user
type githubUser struct {
	ID    int64
	Login string
}

// githubIssue represents a github issue.
// See https://developer.github.com/v3/issues/#get-a-single-issue
type githubIssue struct {
	ID      int64
	Number  int32
	Closed  bool
	User    *githubUser
	Created time.Time
	Updated time.Time
	Body    string
	// TODO Comments ...
}

// A MutationSource yields a log of mutations that will catch a corpus
// back up to the present.
type MutationSource interface {
	// GetMutations returns a channel of mutations.
	// The channel should be closed at the end.
	// All sends on the returned channel should select
	// on the provided context.
	GetMutations(context.Context) <-chan *maintpb.Mutation
}

// Initialize populates the Corpus using the data from the MutationSource.
func (c *Corpus) Initialize(ctx context.Context, src MutationSource) error {
	return c.processMutations(ctx, src)
}

func (c *Corpus) processMutations(ctx context.Context, src MutationSource) error {
	ch := src.GetMutations(ctx)
	done := ctx.Done()

	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		select {
		case <-done:
			return ctx.Err()
		case m, ok := <-ch:
			if !ok {
				return nil
			}
			c.processMutationLocked(m)
		}
	}
}

// c.mu must be held.
func (c *Corpus) processMutationLocked(m *maintpb.Mutation) {
	if im := m.GithubIssue; im != nil {
		c.processGithubIssueMutation(im)
	}
	// TODO: more...
}

func (c *Corpus) repoKey(owner, repo string) githubRepo {
	if owner == "" || repo == "" {
		return ""
	}
	// TODO: avoid garbage, use interned strings? profile later
	// once we have gigabytes of mutation logs to slurp at
	// start-up. (The same thing mattered for Camlistore start-up
	// time at least)
	return githubRepo(owner + "/" + repo)
}

func (c *Corpus) getGithubUser(pu *maintpb.GithubUser) *githubUser {
	if pu == nil {
		return nil
	}
	if u := c.githubUsers[pu.Id]; u != nil {
		if pu.Login != "" && pu.Login != u.Login {
			u.Login = pu.Login
		}
		return u
	}
	if c.githubUsers == nil {
		c.githubUsers = make(map[int64]*githubUser)
	}
	u := &githubUser{
		ID:    pu.Id,
		Login: pu.Login,
	}
	c.githubUsers[pu.Id] = u
	return u
}

var errNoChanges = errors.New("No changes in this github.Issue")

// newMutationFromIssue generates a GithubIssueMutation using the smallest
// possible diff between ci (a corpus Issue) and gi (an external github issue).
//
// If newMutationFromIssue returns nil, the provided github.Issue is no newer
// than the data we have in the corpus. ci may be nil.
func newMutationFromIssue(ci *githubIssue, gi *github.Issue, rp githubRepo) *maintpb.GithubIssueMutation {
	if gi == nil || gi.Number == nil {
		panic(fmt.Sprintf("github issue with nil number: %#v", gi))
	}
	owner, repo := rp.Org(), rp.Repo()
	// always need these fields to figure out which key to write to
	m := &maintpb.GithubIssueMutation{
		Owner:  owner,
		Repo:   repo,
		Number: int32(*gi.Number),
	}
	if ci == nil {
		// We don't know about this github issue, so populate all fields in one
		// mutation.
		if gi.CreatedAt != nil {
			tproto, err := ptypes.TimestampProto(*gi.CreatedAt)
			if err != nil {
				panic(err)
			}
			m.Created = tproto
		}
		if gi.UpdatedAt != nil {
			tproto, err := ptypes.TimestampProto(*gi.UpdatedAt)
			if err != nil {
				panic(err)
			}
			m.Updated = tproto
		}
		if gi.Body != nil {
			m.Body = *gi.Body
		}
		return m
	}
	if gi.UpdatedAt != nil {
		if gi.UpdatedAt.Before(ci.Updated) {
			// This data is stale, ignore it.
			return nil
		}
		tproto, err := ptypes.TimestampProto(*gi.UpdatedAt)
		if err != nil {
			panic(err)
		}
		m.Updated = tproto
	}
	if gi.Body != nil && *gi.Body != ci.Body {
		m.Body = *gi.Body
	}
	return m
}

// getIssue finds an issue in the Corpus or returns nil, false if it is not
// present.
func (c *Corpus) getIssue(rp githubRepo, number int32) (*githubIssue, bool) {
	issueMap, ok := c.githubIssues[rp]
	if !ok {
		return nil, false
	}
	gi, ok := issueMap[number]
	return gi, ok
}

// processGithubIssueMutation updates the corpus with the information in m, and
// returns true if the Corpus was modified.
func (c *Corpus) processGithubIssueMutation(m *maintpb.GithubIssueMutation) (changed bool) {
	if c == nil {
		panic("nil corpus")
	}
	k := c.repoKey(m.Owner, m.Repo)
	if k == "" {
		// TODO: errors? return false? skip for now.
		return
	}
	if m.Number == 0 {
		return
	}
	issueMap, ok := c.githubIssues[k]
	if !ok {
		if c.githubIssues == nil {
			c.githubIssues = make(map[githubRepo]map[int32]*githubIssue)
		}
		issueMap = make(map[int32]*githubIssue)
		c.githubIssues[k] = issueMap
	}
	gi, ok := issueMap[m.Number]
	if !ok {
		created, err := ptypes.Timestamp(m.Created)
		if err != nil {
			panic(err)
		}
		gi = &githubIssue{
			Number:  m.Number,
			User:    c.getGithubUser(m.User),
			Created: created,
		}
		issueMap[m.Number] = gi
		changed = true
	}
	// Check Updated before all other fields so they don't update if this
	// Mutation is stale
	if m.Updated != nil {
		updated, err := ptypes.Timestamp(m.Updated)
		if err != nil {
			panic(err)
		}
		if !updated.IsZero() && updated.Before(gi.Updated) {
			// this mutation represents data older than the data we have in
			// the corpus; ignore it.
			return false
		}
		gi.Updated = updated
		changed = changed || updated.After(gi.Updated)
	}
	if m.Body != "" {
		gi.Body = m.Body
		changed = changed || m.Body != gi.Body
	}
	// ignoring Created since it *should* never update
	return changed
}

// PopulateFromServer populates the corpus from a maintnerd server.
func (c *Corpus) PopulateFromServer(ctx context.Context, serverURL string) error {
	panic("TODO")
}

// PopulateFromDisk populates the corpus from a set of mutation logs
// in a local directory.
func (c *Corpus) PopulateFromDisk(ctx context.Context, dir string) error {
	panic("TODO")
}

// PopulateFromAPIs populates the corpus using API calls to
// the upstream Git, Github, and/or Gerrit servers.
func (c *Corpus) PopulateFromAPIs(ctx context.Context) error {
	panic("TODO")
}

// Poll checks for new changes on all repositories being tracked by the Corpus.
func (c *Corpus) Poll(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)
	for _, rp := range c.githubRepos {
		rp := rp
		group.Go(func() error {
			return c.PollGithubLoop(ctx, rp.name, rp.tokenFile)
		})
	}
	return group.Wait()
}

// PollGithubLoop checks for new changes on a single Github repository and
// updates the Corpus with any changes.
func (c *Corpus) PollGithubLoop(ctx context.Context, rp githubRepo, tokenFile string) error {
	slurp, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		return err
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return fmt.Errorf("Expected token file %s to be of form <username>:<token>", tokenFile)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: f[1]})
	tc := oauth2.NewClient(oauth2.NoContext, ts)
	ghc := github.NewClient(tc)
	for {
		err := c.pollGithub(ctx, rp, ghc)
		if err == context.Canceled {
			return err
		}
		log.Printf("Polled github for %s; err = %v. Sleeping.", rp, err)
		time.Sleep(30 * time.Second)
	}
}

func (c *Corpus) pollGithub(ctx context.Context, rp githubRepo, ghc *github.Client) error {
	log.Printf("Polling github for %s ...", rp)
	page := 1
	keepGoing := true
	owner, repo := rp.Org(), rp.Repo()
	for keepGoing {
		// TODO: use https://godoc.org/github.com/google/go-github/github#ActivityService.ListIssueEventsForRepository probably
		issues, _, err := ghc.Issues.ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
			State:     "all",
			Sort:      "updated",
			Direction: "desc",
			// TODO: if an issue gets updated while we are paging, we might
			// process the same issue twice - as item 100 on page 1 and then
			// again as item 1 on page 2.
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: 100,
			},
		})
		if err != nil {
			return err
		}
		log.Printf("github %s/%s: page %d, num issues %d", owner, repo, page, len(issues))
		if len(issues) == 0 {
			break
		}
		c.mu.Lock()
		for _, is := range issues {
			fmt.Printf("issue %d: %s\n", is.ID, *is.Title)
			gi, _ := c.getIssue(rp, int32(*is.Number))
			mp := newMutationFromIssue(gi, is, rp)
			if mp == nil {
				keepGoing = false
				break
			}
			c.processGithubIssueMutation(mp)
		}
		c.mu.Unlock()
		page++
	}
	return nil
}
