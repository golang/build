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

import "context"

// Corpus holds all of a project's metadata.
type Corpus struct {
	// ... TODO
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
