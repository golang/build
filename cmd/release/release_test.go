// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"golang.org/x/build/dashboard"
)

func TestBuildersExist(t *testing.T) {
	for _, b := range builds {
		_, ok := dashboard.Builders[b.Builder]
		if !ok {
			t.Errorf("missing builder: %q", b.Builder)
		}
	}
}

func TestTestOnlyBuildsDontSkipTests(t *testing.T) {
	for _, b := range builds {
		if b.TestOnly && b.SkipTests {
			t.Errorf("build %s is configured to run tests only, but also to skip tests; is that intentional?", b)
		}
	}
}

func TestMinSupportedMacOSVersion(t *testing.T) {
	testCases := []struct {
		desc      string
		goVer     string
		wantMacOS string
	}{
		{"minor_release_13", "go1.13", "10.11"},
		{"minor_release_14", "go1.14", "10.11"},
		{"rc_release_13", "go1.13rc1", "10.11"},
		{"beta_release_13", "go1.13beta1", "10.11"},
		{"minor_release_15", "go1.15", "10.12"},
		{"patch_release_15", "go1.15.1", "10.12"},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			got := minSupportedMacOSVersion(tc.goVer)
			if got != tc.wantMacOS {
				t.Errorf("got %s; want %s", got, tc.wantMacOS)
			}
		})
	}
}
