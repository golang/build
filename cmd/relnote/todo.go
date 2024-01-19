// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.21

package main

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// todo prints a report  to w on which release notes need to be written.
// It takes the repo root.
func todo(w io.Writer, fsys fs.FS) error {
	// At present, just look for TODOs. (This is essentially doing a grep.)
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			if err := todoFile(w, fsys, path); err != nil {
				return err
			}
		}
		return nil
	})
}

func todoFile(w io.Writer, dir fs.FS, filename string) error {
	f, err := dir.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	ln := 0
	for scan.Scan() {
		ln++
		if line := scan.Text(); strings.Contains(line, "TODO") {
			fmt.Fprintf(w, "%s:%d: %s\n", filename, ln, line)
		}
	}
	return scan.Err()
}
