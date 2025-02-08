// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"testing"

	gorebuild "golang.org/x/build/cmd/gorebuild"
)

func TestDiffArchive(t *testing.T) {
	type match bool // A named type for readability of test cases below.
	for _, tc := range [...]struct {
		name string
		a, b map[string]string
		want match
	}{
		{
			name: "empty",
			a:    map[string]string{},
			b:    map[string]string{},
			want: match(true),
		},
		{
			name: "equal",
			a:    map[string]string{"file1": "content 1", "file2": "content 2"},
			b:    map[string]string{"file1": "content 1", "file2": "content 2"},
			want: match(true),
		},
		{
			name: "different content",
			a:    map[string]string{"file1": "content 1", "file2": "content 2"},
			b:    map[string]string{"file1": "content 3", "file2": "content 4"},
			want: match(false),
		},
		{
			name: "missing file", // file2 in a, but missing in b.
			a:    map[string]string{"file1": "", "file2": ""},
			b:    map[string]string{"file1": ""},
			want: match(false),
		},
		{
			name: "unexpected file", // file3 not in a, but unexpectedly there in b.
			a:    map[string]string{"file1": "", "file2": ""},
			b:    map[string]string{"file1": "", "file2": "", "file3": ""},
			want: match(false),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log gorebuild.Log
			got := gorebuild.DiffArchive(&log, tc.a, tc.b, func(_ *gorebuild.Log, a, b string) bool { return a == b })
			if got != bool(tc.want) {
				t.Errorf("got match = %v, want %v", got, tc.want)
			}
		})
	}
}
