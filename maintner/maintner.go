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
	"regexp"
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
	MutationLogger MutationLogger

	mu               sync.RWMutex
	githubIssues     map[githubRepo]map[int32]*githubIssue // repo -> num -> issue
	githubUsers      map[int64]*githubUser
	pollGithubIssues []polledGithubIssues
	pollGitDirs      []polledGitCommits
	// If true, log new commits
	shouldLog bool
	debug     bool

	// TODO
}

type polledGithubIssues struct {
	name      githubRepo
	tokenFile string
}

type polledGitCommits struct {
	repo *maintpb.GitRepo
	dir  string
}

func NewCorpus(logger MutationLogger) *Corpus {
	return &Corpus{
		githubIssues:   make(map[githubRepo]map[int32]*githubIssue),
		githubUsers:    make(map[int64]*githubUser),
		MutationLogger: logger,
	}
}

// StartLogging indicates that further changes should be written to the log.
func (c *Corpus) StartLogging() {
	c.mu.Lock()
	c.shouldLog = true
	c.mu.Unlock()
}

func (c *Corpus) SetDebug() {
	c.debug = true
}

func (c *Corpus) debugf(format string, v ...interface{}) {
	if c.debug {
		log.Printf(format, v...)
	}
}

func (c *Corpus) AddGithub(owner, repo, tokenFile string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pollGithubIssues = append(c.pollGithubIssues, polledGithubIssues{
		name:      githubRepo(owner + "/" + repo),
		tokenFile: tokenFile,
	})
}

// gerritProjNameRx is the pattern describing a Gerrit project name.
// TODO: figure out if this is accurate.
var gerritProjNameRx = regexp.MustCompile(`^[a-z0-9]+[a-z0-9\-\_]*$`)

// AddGoGitRepo registers a git directory to have its metadata slurped into the corpus.
// The goRepo is a name like "go" or "net". The dir is a path on disk.
//
// TODO(bradfitz): this whole interface is temporary. Make this
// support any git repo and make this (optionally?) use the gitmirror
// service later instead of a separate copy on disk.
func (c *Corpus) AddGoGitRepo(goRepo, dir string) {
	if !gerritProjNameRx.MatchString(goRepo) {
		panic(fmt.Sprintf("bogus goRepo value %q", goRepo))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pollGitDirs = append(c.pollGitDirs, polledGitCommits{
		repo: &maintpb.GitRepo{GoRepo: goRepo},
		dir:  dir,
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
	ID        int64
	Number    int32
	Closed    bool
	User      *githubUser
	Assignees []*githubUser
	Created   time.Time
	Updated   time.Time
	Title     string
	Body      string
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

func (c *Corpus) processMutation(m *maintpb.Mutation) {
	c.mu.Lock()
	c.processMutationLocked(m)
	c.mu.Unlock()
	if c.MutationLogger != nil && c.shouldLog {
		err := c.MutationLogger.Log(m)
		if err != nil {
			// TODO: handle errors better
			fmt.Printf("could not log mutation %v: %v\n", m, err)
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

// c.mu must be held
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

// newGithubUserProto creates a GithubUser with the minimum diff between
// existing and g. The return value is nil if there were no changes. existing
// may also be nil.
func newGithubUserProto(existing *maintpb.GithubUser, g *github.User) *maintpb.GithubUser {
	if g == nil {
		return nil
	}
	id := int64(g.GetID())
	if existing == nil {
		return &maintpb.GithubUser{
			Id:    id,
			Login: g.GetLogin(),
		}
	}
	hasChanges := false
	u := &maintpb.GithubUser{Id: id}
	if login := g.GetLogin(); existing.Login != login {
		u.Login = login
		hasChanges = true
	}
	// Add more fields here
	if hasChanges {
		return u
	}
	return nil
}

// deletedAssignees returns an array of user ID's that are present in existing
// but not present in new.
func deletedAssignees(existing []*githubUser, new []*github.User) []int64 {
	mp := make(map[int64]bool, len(existing))
	for _, u := range new {
		id := int64(u.GetID())
		mp[id] = true
	}
	toDelete := []int64{}
	for _, u := range existing {
		if _, ok := mp[u.ID]; !ok {
			toDelete = append(toDelete, u.ID)
		}
	}
	return toDelete
}

// newAssignees returns an array of diffs between existing and new. New users in
// new will be present in the returned array in their entirety. Modified users
// will appear containing only the ID field and changed fields. Unmodified users
// will not appear in the returned array.
func newAssignees(existing []*githubUser, new []*github.User) []*maintpb.GithubUser {
	mp := make(map[int64]*githubUser, len(existing))
	for _, u := range existing {
		mp[u.ID] = u
	}
	changes := []*maintpb.GithubUser{}
	for _, u := range new {
		if existingUser, ok := mp[int64(u.GetID())]; ok {
			diffUser := &maintpb.GithubUser{
				Id: int64(u.GetID()),
			}
			hasDiff := false
			if login := u.GetLogin(); existingUser.Login != login {
				diffUser.Login = login
				hasDiff = true
			}
			// check more User fields for diffs here, as we add them to the proto

			if hasDiff {
				changes = append(changes, diffUser)
			}
		} else {
			changes = append(changes, &maintpb.GithubUser{
				Id:    int64(u.GetID()),
				Login: u.GetLogin(),
			})
		}
	}
	return changes
}

// setAssigneesFromProto returns a new array of assignees according to the
// instructions in new (adds or modifies users in existing ), and toDelete
// (deletes them). c.mu must be held.
func (c *Corpus) setAssigneesFromProto(existing []*githubUser, new []*maintpb.GithubUser, toDelete []int64) ([]*githubUser, bool) {
	mp := make(map[int64]*githubUser)
	for _, u := range existing {
		mp[u.ID] = u
	}
	for _, u := range new {
		if existingUser, ok := mp[u.Id]; ok {
			if u.Login != "" {
				existingUser.Login = u.Login
			}
			// TODO: add other fields here when we add them for user.
		} else {
			c.debugf("adding assignee %q", u.Login)
			existing = append(existing, c.getGithubUser(u))
		}
	}
	// IDs to delete, in descending order
	idxsToDelete := []int{}
	// this is quadratic but the number of assignees is very unlikely to exceed,
	// say, 5.
	for _, id := range toDelete {
		for i, u := range existing {
			if u.ID == id {
				idxsToDelete = append([]int{i}, idxsToDelete...)
			}
		}
	}
	for _, idx := range idxsToDelete {
		c.debugf("deleting assignee %q", existing[idx].Login)
		existing = append(existing[:idx], existing[idx+1:]...)
	}
	return existing, len(toDelete) > 0 || len(new) > 0
}

// newMutationFromIssue generates a GithubIssueMutation using the smallest
// possible diff between ci (a corpus Issue) and gi (an external github issue).
//
// If newMutationFromIssue returns nil, the provided github.Issue is no newer
// than the data we have in the corpus. ci may be nil.
func newMutationFromIssue(ci *githubIssue, gi *github.Issue, rp githubRepo) *maintpb.Mutation {
	if gi == nil || gi.Number == nil {
		panic(fmt.Sprintf("github issue with nil number: %#v", gi))
	}
	owner, repo := rp.Org(), rp.Repo()
	// always need these fields to figure out which key to write to
	m := &maintpb.GithubIssueMutation{
		Owner:  owner,
		Repo:   repo,
		Number: int32(gi.GetNumber()),
	}
	if ci == nil {
		// We don't know about this github issue, so populate all fields in one
		// mutation.
		if gi.CreatedAt != nil {
			tproto, err := ptypes.TimestampProto(gi.GetCreatedAt())
			if err != nil {
				panic(err)
			}
			m.Created = tproto
		}
		if gi.UpdatedAt != nil {
			tproto, err := ptypes.TimestampProto(gi.GetUpdatedAt())
			if err != nil {
				panic(err)
			}
			m.Updated = tproto
		}
		m.Body = gi.GetBody()
		m.Title = gi.GetTitle()
		if gi.User != nil {
			m.User = newGithubUserProto(nil, gi.User)
		}
		m.Assignees = newAssignees(nil, gi.Assignees)
		// no deleted assignees on first run
		return &maintpb.Mutation{GithubIssue: m}
	}
	if gi.UpdatedAt != nil {
		if !gi.UpdatedAt.After(ci.Updated) {
			// This data is stale, ignore it.
			return nil
		}
		tproto, err := ptypes.TimestampProto(gi.GetUpdatedAt())
		if err != nil {
			panic(err)
		}
		m.Updated = tproto
	}
	if body := gi.GetBody(); body != ci.Body {
		m.Body = body
	}
	if title := gi.GetTitle(); title != ci.Title {
		m.Title = title
	}
	if gi.User != nil {
		m.User = newGithubUserProto(m.User, gi.User)
	}
	m.Assignees = newAssignees(ci.Assignees, gi.Assignees)
	m.DeletedAssignees = deletedAssignees(ci.Assignees, gi.Assignees)
	return &maintpb.Mutation{GithubIssue: m}
}

// getIssue finds an issue in the Corpus or returns nil, false if it is not
// present.
func (c *Corpus) getIssue(rp githubRepo, number int32) (*githubIssue, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
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
			// User added below
			Number:    m.Number,
			ID:        m.Id,
			Created:   created,
			Assignees: []*githubUser{},
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
		changed = changed || updated.After(gi.Updated)
		gi.Updated = updated
	}
	if m.User != nil {
		gi.User = c.getGithubUser(m.User)
	}

	gi.Assignees, ok = c.setAssigneesFromProto(gi.Assignees, m.Assignees, m.DeletedAssignees)
	changed = changed || ok

	if m.Body != "" {
		changed = changed || m.Body != gi.Body
		gi.Body = m.Body
	}
	if m.Title != "" {
		changed = changed || m.Title != gi.Title
		gi.Title = m.Title
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
	for _, rp := range c.pollGithubIssues {
		rp := rp
		group.Go(func() error {
			return c.PollGithubLoop(ctx, rp.name, rp.tokenFile)
		})
	}
	for _, rp := range c.pollGitDirs {
		rp := rp
		group.Go(func() error {
			return c.PollGitCommits(ctx, rp)
		})
	}
	return group.Wait()
}

// PollGithubCommits polls for git commits in a directory.
func (c *Corpus) PollGitCommits(ctx context.Context, conf polledGitCommits) error {
	log.Printf("TODO: poll %v from %v", conf.repo, conf.dir)
	select {} // TODO(bradfitz): actuall poll
	return nil
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
	tc := oauth2.NewClient(ctx, ts)
	ghc := github.NewClient(tc)
	for {
		err := c.pollGithub(ctx, rp, ghc)
		if err == context.Canceled {
			return err
		}
		log.Printf("Polled github for %s; err = %v. Sleeping.", rp, err)
		// TODO: select and listen for context errors
		select {
		case <-time.After(30 * time.Second):
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Corpus) pollGithub(ctx context.Context, rp githubRepo, ghc *github.Client) error {
	log.Printf("Polling github for %s ...", rp)
	page := 1
	seen := make(map[int64]bool)
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
		for _, is := range issues {
			id := int64(is.GetID())
			if seen[id] {
				// If an issue gets updated (and bumped to the top) while we
				// are paging, it's possible the last issue from page N can
				// appear as the first issue on page N+1. Don't process that
				// issue twice.
				// https://github.com/google/go-github/issues/566
				continue
			}
			seen[id] = true
			gi, _ := c.getIssue(rp, int32(*is.Number))
			mp := newMutationFromIssue(gi, is, rp)
			if mp == nil {
				keepGoing = false
				break
			}
			fmt.Printf("modifying %s, issue %d: %s\n", rp, is.GetNumber(), is.GetTitle())
			c.processMutation(mp)
		}
		page++
	}
	return nil
}
