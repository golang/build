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
			match(b.GoQuery, "go1.15.6") // Shouldn't panic for any b.GoQuery.
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
		goVer     string
		wantMacOS string
	}{
		{"go1.15", "10.12"},
		{"go1.15.7", "10.12"},
		{"go1.16beta1", "10.12"},
		{"go1.16rc1", "10.12"},
		{"go1.16", "10.12"},
		{"go1.16.1", "10.12"},
		{"go1.17beta1", "10.13"},
		{"go1.17rc1", "10.13"},
		{"go1.17", "10.13"},
		{"go1.17.2", "10.13"},
	}
	for _, tc := range testCases {
		t.Run(tc.goVer, func(t *testing.T) {
			got := minSupportedMacOSVersion(tc.goVer)
			if got != tc.wantMacOS {
				t.Errorf("got %s; want %s", got, tc.wantMacOS)
			}
		})
	}
}

func TestBuilderSelectionPerGoVersion(t *testing.T) {
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
		// Go 1.15.x still uses the Jessie builders.
		{"go1.15.55", "linux-386", "linux-386-jessie"},
		// Go 1.16 starts to use the the Stretch builders.
		{"go1.16", "linux-amd64", "linux-amd64-stretch"},
		{"go1.16", "linux-386", "linux-386-stretch"},

		// Go 1.15.x still uses the Packet and Scaleway builders.
		{"go1.15.55", "linux-armv6l", "linux-arm"},
		// Go 1.16 starts to use the the AWS builders.
		{"go1.16", "linux-arm64", "linux-arm64-aws"},
		{"go1.16", "linux-armv6l", "linux-arm-aws"},

		// Go 1.15 RC 2+ starts to use the the FreeBSD 11.2 builder.
		{"go1.15rc2", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.15rc2", "freebsd-386", "freebsd-386-11_2"},
		{"go1.15", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.15", "freebsd-386", "freebsd-386-11_2"},
		{"go1.15.1", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.15.1", "freebsd-386", "freebsd-386-11_2"},
		// Go 1.16 continues to use the the FreeBSD 11.2 builder.
		{"go1.16", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.16", "freebsd-386", "freebsd-386-11_2"},
	}
	for _, tc := range testCases {
		t.Run(tc.target+"@"+tc.goVer, func(t *testing.T) {
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
