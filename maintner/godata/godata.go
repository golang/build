// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package godata loads the Go project's corpus of Git, Github, and Gerrit activity.
package godata

import (
	"context"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"golang.org/x/build/maintner"
)

// Get returns the Go project's corpus.
func Get(ctx context.Context) (*maintner.Corpus, error) {
	targetDir := filepath.Join(xdgCacheDir(), "golang-maintner")
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return nil, err
	}
	mutSrc := maintner.NewNetworkMutationSource("https://maintner.golang.org/logs", targetDir)
	corpus := new(maintner.Corpus)
	if err := corpus.Initialize(ctx, mutSrc); err != nil {
		return nil, err
	}
	return corpus, nil
}

// xdgCacheDir returns the XDG Base Directory Specification cache
// directory.
func xdgCacheDir() string {
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache != "" {
		return cache
	}
	home := homeDir()
	// Not XDG but standard for OS X.
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library/Caches")
	}
	return filepath.Join(home, ".cache")
}

func homeDir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
	}
	home := os.Getenv("HOME")
	if home != "" {
		return home
	}
	u, err := user.Current()
	if err != nil {
		log.Fatalf("failed to get home directory or current user: %v", err)
	}
	return u.HomeDir
}
