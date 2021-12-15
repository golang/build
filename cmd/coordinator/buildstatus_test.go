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

// TestParseOutputAndHeader tests header parsing by parseOutputAndHeader.
func TestParseOutputAndHeader(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      []byte
		wantHeader string
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
			wantHeader: "##### Testing packages.",
			wantOut: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "header only",
			input: []byte(`
XXXBANNERXXX:Testing packages.
`),
			wantHeader: "##### Testing packages.",
			wantOut:    []byte(``),
		},
		{
			name: "header only missing trailing newline",
			input: []byte(`
XXXBANNERXXX:Testing packages.`),
			wantHeader: "##### Testing packages.",
			wantOut:    []byte(``),
		},
		{
			name: "no banner",
			input: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantHeader: "",
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
			wantHeader: "",
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
			wantHeader: "",
			wantOut: []byte(`
##### Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotHeader, gotOut := parseOutputAndHeader(tc.input)
			if gotHeader != tc.wantHeader {
				t.Errorf("parseOutputAndBanner(%q) got banner %q want banner %q", string(tc.input), gotHeader, tc.wantHeader)
			}
			if string(gotOut) != string(tc.wantOut) {
				t.Errorf("parseOutputAndBanner(%q) got out %q want out %q", string(tc.input), string(gotOut), string(tc.wantOut))
			}
		})
	}
}
