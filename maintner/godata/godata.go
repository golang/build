// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package godata loads the Go project's corpus of Git, Github, and Gerrit activity.
package godata

import (
	"context"
	"os"
	"path/filepath"

	"golang.org/x/build/maintner"
)

// Get returns the Go project's corpus.
func Get(ctx context.Context) (*maintner.Corpus, error) {
	// TODO: this is a dummy implementation for now.  It should
	// really create a cache dir, and slurp as-needed from the
	// network (once we run a server), and then load it. For now
	// we assume it's already on disk.
	dir := filepath.Join(os.Getenv("HOME"), "var", "maintnerd")
	logger := maintner.NewDiskMutationLogger(dir)
	corpus := maintner.NewCorpus(logger, dir)
	if err := corpus.Initialize(ctx, logger); err != nil {
		return nil, err
	}
	return corpus, nil
}
