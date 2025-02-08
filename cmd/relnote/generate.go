// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/build/relnote"
	"rsc.io/markdown"
)

const prefixFormat = `---
title: Go 1.%[1]s Release Notes
template: false
---

`

// generate takes the root of the Go repo.
// It generates release notes by combining the fragments in the doc/next directory
// of the repo.
func generate(version, goRoot string) error {
	if goRoot == "" {
		goRoot = runtime.GOROOT()
	}
	dir := filepath.Join(goRoot, "doc", "next")
	doc, err := relnote.Merge(os.DirFS(dir))
	if err != nil {
		return fmt.Errorf("merging %s: %v", dir, err)
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
