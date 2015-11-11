// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package main

import (
	"os"
	"os/user"
	"path/filepath"
)

// xdgCacheDir returns the XDG Base Directory Specification cache
// directory.
func xdgCacheDir() string {
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home := os.Getenv("HOME")
		if home == "" {
			u, err := user.Current()
			if err != nil {
				home = u.HomeDir
			}
		}
		cache = filepath.Join(home, ".cache")
	}
	return cache
}

// xdgCreateDir creates a directory and its parents in accordance with
// the XDG Base Directory Specification.
func xdgCreateDir(path string) error {
	return os.MkdirAll(path, 0700)
}
