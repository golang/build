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
	"sync"
	"time"

	"golang.org/x/build/maintner/maintpb"
)

// Corpus holds all of a project's metadata.
type Corpus struct {
	// ... TODO

	mu           sync.RWMutex
	githubIssues map[githubRepo]map[int]*githubIssue // repo -> num -> issue
	githubUsers  map[int64]*githubUser
}

// githubRepo is a github org & repo, lowercase, joined by a '/',
// such as "golang/go".
type githubRepo string

// githubUser represents a github user.
// It is a subset of https://developer.github.com/v3/users/#get-a-single-user
type githubUser struct {
	ID       int64
	Username string // what Github calls "Login"
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

func (c *Corpus) processGithubIssueMutation(m *maintpb.GithubIssueMutation) {
	// TODO: ...
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
