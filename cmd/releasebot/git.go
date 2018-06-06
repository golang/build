// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gitCheckout sets up a fresh git checkout in which to work,
// in $HOME/go-releasebot-work/<release>/gitwork
// (where <release> is a string like go1.8.5).
// The first time it is run for a particular release,
// gitCheckout also creates a clean checkout in
// $HOME/go-releasebot-work/<release>/gitmirror,
// to use as an object cache to speed future checkouts.
// On return, w.runDir has been set to gitwork/src,
// to allow commands like "./make.bash".
func (w *Work) gitCheckout() {
	shortRel := strings.ToLower(w.Milestone.Title)
	shortRel = shortRel[:strings.LastIndex(shortRel, ".")]
	w.ReleaseBranch = "release-branch." + shortRel

	w.Dir = filepath.Join(os.Getenv("HOME"), "go-releasebot-work/"+strings.ToLower(w.Version))
	w.log.Printf("working in %s\n", w.Dir)
	if err := os.MkdirAll(w.Dir, 0777); err != nil {
		w.log.Panic(err)
	}

	// Check out a local mirror to work-mirror, to speed future checkouts for this point release.
	mirror := filepath.Join(w.Dir, "gitmirror")
	if _, err := os.Stat(mirror); err != nil {
		w.run("git", "clone", "https://go.googlesource.com/go", mirror)
		w.runDir = mirror
		w.run("git", "config", "gc.auto", "0") // don't throw away refs we fetch
	} else {
		w.runDir = mirror
		w.run("git", "fetch", "origin", "master")
	}
	w.run("git", "fetch", "origin", w.ReleaseBranch)

	// Clone real Gerrit, but using local mirror for most objects.
	gitDir := filepath.Join(w.Dir, "gitwork")
	if err := os.RemoveAll(gitDir); err != nil {
		w.log.Panic(err)
	}
	w.run("git", "clone", "--reference", mirror, "-b", w.ReleaseBranch, "https://go.googlesource.com/go", gitDir)
	w.runDir = gitDir
	w.run("git", "codereview", "change", "relwork")
	w.run("git", "config", "gc.auto", "0") // don't throw away refs we fetch
	w.runDir = filepath.Join(gitDir, "src")

	_, err := w.runErr("git", "rev-parse", w.Version)
	if err == nil {
		w.logError("%s tag already exists in Go repository!", w.Version)
		w.log.Panic("already released")
	}
}

// gitTagVersion tags the release candidate or release in Git.
func (w *Work) gitTagVersion() {
	w.runDir = filepath.Join(w.Dir, "gitwork")
	out := w.runOut("git", "rev-parse", "HEAD")
	w.VersionCommit = strings.TrimSpace(string(out))
	out = w.runOut("git", "show", w.VersionCommit)
	fmt.Printf("About to tag the following commit as %s:\n\n%s\n\nOk? (y/n) ", w.Version, out)
	if dryRun {
		return
	}
	var response string
	_, err := fmt.Scanln(&response)
	if err != nil {
		w.log.Panic(err)
	}
	if response != "y" {
		w.log.Panic("stopped")
	}
	out, err = w.runErr("git", "tag", w.Version, w.VersionCommit)
	if err != nil {
		w.logError("git tag failed: %s\n%s", err, out)
		return
	}
	w.run("git", "push", "origin", w.Version)
}
