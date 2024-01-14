// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/build/relnote"
	"rsc.io/markdown"
)

const prefixFormat = `
---
path: /doc/go1.%s
template: false
title: Go 1.%[1]s Release Notes
---

`

// generate takes the root of the Go repo.
// It generates release notes by combining the fragments in the repo.
func generate(version string) error {
	repoRoot := flag.Arg(1)
	if repoRoot == "" {
		return errors.New("missing Go repo root")
	}
	dir := filepath.Join(repoRoot, "doc", "next")
	doc, err := relnote.Merge(os.DirFS(dir))
	if err != nil {
		return err
	}
	out := markdown.ToMarkdown(doc)
	out = fmt.Sprintf(prefixFormat, version) + out
	outFile := fmt.Sprintf("go1.%s.md", version)
	if err := os.WriteFile(outFile, []byte(out), 0644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", outFile)
	return nil
}
