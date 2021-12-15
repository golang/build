// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16 && (linux || darwin)
// +build go1.16
// +build linux darwin

package main

import (
	"testing"
)

// TestParseOutputAndBanner tests banner parsing by parseOutputAndBanner.
func TestParseOutputAndBanner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      []byte
		wantBanner string
		wantOut    []byte
	}{
		{
			name: "standard",
			input: []byte(`
XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantBanner: "Testing packages.",
			wantOut: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "banner only",
			input: []byte(`
XXXBANNERXXX:Testing packages.
`),
			wantBanner: "Testing packages.",
			wantOut:    []byte(``),
		},
		{
			// TODO(prattmic): This is likely not desirable behavior.
			name: "banner only missing trailing newline",
			input: []byte(`
XXXBANNERXXX:Testing packages.`),
			wantBanner: "",
			wantOut:    []byte(`Testing packages.`),
		},
		{
			name: "no banner",
			input: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantBanner: "",
			wantOut: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "no newline",
			input: []byte(`XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantBanner: "",
			wantOut: []byte(`XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "wrong banner",
			input: []byte(`
##### Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantBanner: "",
			wantOut: []byte(`
##### Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotBanner, gotOut := parseOutputAndBanner(tc.input)
			if gotBanner != tc.wantBanner {
				t.Errorf("parseOutputAndBanner(%q) got banner %q want banner %q", string(tc.input), gotBanner, tc.wantBanner)
			}
			if string(gotOut) != string(tc.wantOut) {
				t.Errorf("parseOutputAndBanner(%q) got out %q want out %q", string(tc.input), string(gotOut), string(tc.wantOut))
			}
		})
	}
}
