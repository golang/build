// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"strings"
	"testing"
)

func TestSplitVersion(t *testing.T) {
	// Test splitVersion.
	for _, tt := range []struct {
		v            string
		minor, patch int
	}{
		{"go1", 0, 0},
		{"go1.34", 34, 0},
		{"go1.34.7", 34, 7},
	} {
		minor, patch := splitVersion(tt.v)
		if minor != tt.minor || patch != tt.patch {
			t.Errorf("splitVersion(%q) = %v, %v; want %v, %v",
				tt.v, minor, patch, tt.minor, tt.patch)
		}
	}
}

func TestSingleFile(t *testing.T) {
	files, err := os.ReadDir(".")
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
