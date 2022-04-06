// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"strings"
	"testing"
)

func TestSplitVersion(t *testing.T) {
	// Test splitVersion.
	for _, tt := range []struct {
		v                   string
		major, minor, patch int
	}{
		{"go1", 1, 0, 0},
		{"go1.34", 1, 34, 0},
		{"go1.34.7", 1, 34, 7},
	} {
		major, minor, patch := splitVersion(tt.v)
		if major != tt.major || minor != tt.minor || patch != tt.patch {
			t.Errorf("splitVersion(%q) = %v, %v, %v; want %v, %v, %v",
				tt.v, major, minor, patch, tt.major, tt.minor, tt.patch)
		}
	}
}

func TestSingleFile(t *testing.T) {
	files, err := ioutil.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name() == "releaselet.go" ||
			f.Name() == "README.md" ||
			strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		t.Errorf("releaselet should be a single file, found %v", f.Name())
	}
}
