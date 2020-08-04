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

func TestAllQueriesSupported(t *testing.T) {
	for _, b := range builds {
		t.Run(b.String(), func(t *testing.T) {
			defer func() {
				if err := recover(); err != nil {
					t.Errorf("build %v uses an unsupported version query:\n%v", b, err)
				}
			}()
			match(b.GoQuery, "go1.14.6") // Shouldn't panic for any b.GoQuery.
		})
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

func TestFreeBSDBuilder(t *testing.T) {
	matchBuilds := func(target, goVer string) (matched []*Build) {
		for _, b := range builds {
			if b.String() != target || !match(b.GoQuery, goVer) {
				continue
			}
			matched = append(matched, b)
		}
		return matched
	}

	testCases := []struct {
		goVer       string
		target      string
		wantBuilder string
	}{
		// Go 1.14.x and 1.13.x still use the FreeBSD 11.1 builder.
		{"go1.13.55", "freebsd-amd64", "freebsd-amd64-11_1"},
		{"go1.13.55", "freebsd-386", "freebsd-386-11_1"},
		{"go1.14.55", "freebsd-amd64", "freebsd-amd64-11_1"},
		{"go1.14.55", "freebsd-386", "freebsd-386-11_1"},

		// Go 1.15 RC 2+ starts to use the the FreeBSD 11.2 builder.
		{"go1.15rc2", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.15rc2", "freebsd-386", "freebsd-386-11_2"},
		{"go1.15", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.15", "freebsd-386", "freebsd-386-11_2"},
		{"go1.15.1", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.15.1", "freebsd-386", "freebsd-386-11_2"},

		// May change further during the 1.16 dev cycle,
		// but expect same builder as 1.15 for now.
		{"go1.16", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.16", "freebsd-386", "freebsd-386-11_2"},
	}
	for _, tc := range testCases {
		t.Run(tc.goVer, func(t *testing.T) {
			builds := matchBuilds(tc.target, tc.goVer)
			if len(builds) != 1 {
				t.Fatalf("got %d matching builds; want 1", len(builds))
			}
			if got, want := builds[0].Builder, tc.wantBuilder; got != want {
				t.Errorf("got %s; want %s", got, want)
			}
		})
	}
}
