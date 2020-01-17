// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"reflect"
	"testing"

	devapp "golang.org/x/build/devapp"
)

func TestParsePrefixedChangeTitle(t *testing.T) {
	tests := []struct {
		inRoot    string
		in        string
		wantPaths []string
		wantTitle string
	}{
		{
			in:        "import/path: Change title.",
			wantPaths: []string{"import/path"}, wantTitle: "Change title.",
		},
		{
			inRoot:    "root",
			in:        "import/path: Change title.",
			wantPaths: []string{"root/import/path"}, wantTitle: "Change title.",
		},
		{
			inRoot:    "root",
			in:        "[release-branch.go1.11] import/path: Change title.",
			wantPaths: []string{"root/import/path"}, wantTitle: "[release-branch.go1.11] Change title.",
		},

		// Multiple comma-separated paths.
		{
			in:        "path1, path2: Change title.",
			wantPaths: []string{"path1", "path2"}, wantTitle: "Change title.",
		},
		{
			inRoot:    "root",
			in:        "path1, path2: Change title.",
			wantPaths: []string{"root/path1", "root/path2"}, wantTitle: "Change title.",
		},
		{
			inRoot:    "root",
			in:        "[release-branch.go1.11] path1, path2: Change title.",
			wantPaths: []string{"root/path1", "root/path2"}, wantTitle: "[release-branch.go1.11] Change title.",
		},

		// No path prefix.
		{
			in:        "Change title.",
			wantPaths: []string{""}, wantTitle: "Change title.",
		},
		{
			inRoot:    "root",
			in:        "Change title.",
			wantPaths: []string{"root"}, wantTitle: "Change title.",
		},
		{
			inRoot:    "root",
			in:        "[release-branch.go1.11] Change title.",
			wantPaths: []string{"root"}, wantTitle: "[release-branch.go1.11] Change title.",
		},
	}
	for i, tc := range tests {
		gotPaths, gotTitle := devapp.ParsePrefixedChangeTitle(tc.inRoot, tc.in)
		if !reflect.DeepEqual(gotPaths, tc.wantPaths) {
			t.Errorf("%d: got paths: %q, want: %q", i, gotPaths, tc.wantPaths)
		}
		if gotTitle != tc.wantTitle {
			t.Errorf("%d: got title: %q, want: %q", i, gotTitle, tc.wantTitle)
		}
	}
}
