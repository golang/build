// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"slices"
	"testing"
	"testing/fstest"
)

func TestInfoFromDocFiles(t *testing.T) {
	files := map[string]string{
		"a.md": "TODO: write something",
		"b.md": "nothing to do",
		"c":    "has a TODO but not a .md file",
	}

	dir := fstest.MapFS{}
	for name, contents := range files {
		dir[name] = &fstest.MapFile{Data: []byte(contents)}
	}
	var got []ToDo
	addToDo := func(td ToDo) { got = append(got, td) }
	if err := infoFromDocFiles(dir, mentioned{}, addToDo); err != nil {
		t.Fatal(err)
	}
	want := []ToDo{
		{
			message:    "TODO: write something",
			provenance: "a.md:1",
		},
	}

	if !slices.Equal(got, want) {
		t.Errorf("\ngot:\n%+v\nwant:\n%+v", got, want)
	}
}
