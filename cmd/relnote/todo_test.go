// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.21

package main

import (
	"bytes"
	"testing"
	"testing/fstest"
	"time"
)

func TestToDo(t *testing.T) {
	files := map[string]string{
		"a.md": "TODO: write something",
		"b.md": "nothing to do",
		"c":    "has a TODO but not a .md file",
	}

	dir := fstest.MapFS{}
	for name, contents := range files {
		dir[name] = &fstest.MapFile{Data: []byte(contents)}
	}
	var buf bytes.Buffer
	if err := todo(&buf, dir, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := `TODO: write something (from a.md:1)
`
	if got != want {
		t.Errorf("\ngot:\n%s\nwant:\n%s", got, want)
	}
}
