// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"
)

func TestSupportedReleaseBranches(t *testing.T) {
	tests := []struct {
		in, want []string
	}{
		{
			in:   []string{"master", "release-branch.go1.12", "release-branch.go1.5", "release-branch.go1.14"},
			want: []string{"release-branch.go1.12", "release-branch.go1.14"},
		},
		{
			in:   []string{"master", "release-branch.go1.12", "release-branch.go1.5", "release-branch.go1.14", "release-branch.go1.15-security", "release-branch.go1.15"},
			want: []string{"release-branch.go1.14", "release-branch.go1.15"},
		},
		{
			in:   []string{"master", "release-branch.go1.12", "release-branch.go1.5"},
			want: []string{"release-branch.go1.12"},
		},
		{
			in:   []string{"master", "release-branch.go1.12-security"},
			want: nil,
		},
	}
	for _, tt := range tests {
		got := supportedReleaseBranches(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("supportedReleaseBranches(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}
