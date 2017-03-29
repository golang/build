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
	"fmt"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/timestamp"

	"golang.org/x/build/maintner/maintpb"
	"golang.org/x/sync/errgroup"
)

// Corpus holds all of a project's metadata.
//
// There are two main phases to the Corpus: the catch-up phase, when the Corpus
// is populated from a MutationSource (disk, database), and the polling phase,
// when the Corpus polls for new events and stores/writes them to disk.
type Corpus struct {
	MutationLogger MutationLogger
	Verbose        bool

	mu sync.RWMutex // guards all following fields
	// corpus state:
	didInit   bool // true after Initialize completes successfully
	debug     bool
	strIntern map[string]string // interned strings, including binary githashes

	// github-specific
	github             *GitHub
	gerrit             *Gerrit
	watchedGithubRepos []watchedGithubRepo
	watchedGerritRepos []watchedGerritRepo
	// git-specific:
	lastGitCount  time.Time // last time of log spam about loading status
	pollGitDirs   []polledGitCommits
	gitPeople     map[string]*gitPerson
	gitCommit     map[gitHash]*gitCommit
	gitCommitTodo map[gitHash]bool          // -> true
	gitOfHg       map[string]gitHash        // hg hex hash -> git hash
	zoneCache     map[string]*time.Location // "+0530" => location
	dataDir       string
}

type polledGitCommits struct {
	repo *maintpb.GitRepo
	dir  string
}

// NewCorpus creates a new Corpus.
func NewCorpus(logger MutationLogger, dataDir string) *Corpus {
	return &Corpus{MutationLogger: logger, dataDir: dataDir}
}

// GitHub returns the corpus's github data.
func (c *Corpus) GitHub() *GitHub {
	if c.github != nil {
		return c.github
	}
	return new(GitHub)
}

// mustProtoFromTime turns a time.Time into a *timestamp.Timestamp or panics if
// in is invalid.
func mustProtoFromTime(in time.Time) *timestamp.Timestamp {
	tp, err := ptypes.TimestampProto(in)
	if err != nil {
		panic(err)
	}
	return tp
}

// requires c.mu be held for writing
func (c *Corpus) str(s string) string {
	if v, ok := c.strIntern[s]; ok {
		return v
	}
	if c.strIntern == nil {
		c.strIntern = make(map[string]string)
	}
	c.strIntern[s] = s
	return s
}

func (c *Corpus) strb(b []byte) string {
	if v, ok := c.strIntern[string(b)]; ok {
		return v
	}
	return c.str(string(b))
}

func (c *Corpus) SetDebug() {
	c.debug = true
}

func (c *Corpus) debugf(format string, v ...interface{}) {
	if c.debug {
		log.Printf(format, v...)
	}
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
	ch := src.GetMutations(ctx)
	done := ctx.Done()
	log.Printf("Reloading data from log %T ...", src)
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		select {
		case <-done:
			err := ctx.Err()
			log.Printf("Context expired while loading data from log %T: %v", src, err)
			return err
		case m, ok := <-ch:
			if !ok {
				log.Printf("Reloaded data from log %T.", src)
				c.didInit = true
				return nil
			}
			c.processMutationLocked(m)
		}
	}
}

// addMutation adds a mutation to the log and immediately processes it.
func (c *Corpus) addMutation(m *maintpb.Mutation) {
	if c.Verbose {
		log.Printf("mutation: %v", m)
	}
	c.mu.Lock()
	c.processMutationLocked(m)
	c.mu.Unlock()

	if c.MutationLogger == nil {
		return
	}
	err := c.MutationLogger.Log(m)
	if err != nil {
		// TODO: handle errors better? failing is only safe option.
		log.Fatalf("could not log mutation %v: %v\n", m, err)
	}
}

// c.mu must be held.
func (c *Corpus) processMutationLocked(m *maintpb.Mutation) {
	if im := m.GithubIssue; im != nil {
		c.processGithubIssueMutation(im)
	}
	if gm := m.Github; gm != nil {
		c.processGithubMutation(gm)
	}
	if gm := m.Git; gm != nil {
		c.processGitMutation(gm)
	}
	if gm := m.Gerrit; gm != nil {
		c.processGerritMutation(gm)
	}
}

// SyncLoop runs forever (until an error or context expiration) and
// updates the corpus as the tracked sources change.
func (c *Corpus) SyncLoop(ctx context.Context) error {
	return c.sync(ctx, true)
}

// Sync updates the corpus from its tracked sources.
func (c *Corpus) Sync(ctx context.Context) error {
	return c.sync(ctx, false)
}

func (c *Corpus) sync(ctx context.Context, loop bool) error {
	group, ctx := errgroup.WithContext(ctx)
	for _, w := range c.watchedGithubRepos {
		gr, tokenFile := w.gr, w.tokenFile
		group.Go(func() error {
			log.Printf("Polling %v ...", gr.id)
			err := gr.sync(ctx, tokenFile, loop)
			log.Printf("Polling %v: %v", gr.id, err)
			return err
		})
	}
	for _, rp := range c.pollGitDirs {
		rp := rp
		group.Go(func() error {
			return c.syncGitCommits(ctx, rp, loop)
		})
	}
	for _, w := range c.watchedGerritRepos {
		gp := w.project
		group.Go(func() error {
			log.Printf("Polling gerrit %v ...", gp.proj)
			err := gp.sync(ctx, loop)
			log.Printf("Polling gerrit %v: %v", gp.proj, err)
			return err
		})
	}
	return group.Wait()
}
