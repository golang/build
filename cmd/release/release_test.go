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
		{"go1.16beta1", "10.12"},
		{"go1.16rc1", "10.12"},
		{"go1.16", "10.12"},
		{"go1.16.1", "10.12"},
		{"go1.17beta1", "10.13"},
		{"go1.17rc1", "10.13"},
		{"go1.17", "10.13"},
		{"go1.17.2", "10.13"},
		{"go1.18beta1", "10.13"},
		{"go1.18rc1", "10.13"},
		{"go1.18", "10.13"},
		{"go1.18.2", "10.13"},
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
		// linux
		// Go 1.16 use the the Stretch builders.
		{"go1.16", "linux-amd64", "linux-amd64-stretch"},
		{"go1.16", "linux-386", "linux-386-stretch"},
		// Go 1.17 use the the Stretch builders.
		{"go1.17", "linux-amd64", "linux-amd64-stretch"},
		{"go1.17", "linux-386", "linux-386-stretch"},
		// Go 1.18 use the the Stretch builders.
		{"go1.18", "linux-amd64", "linux-amd64-stretch"},
		{"go1.18", "linux-386", "linux-386-stretch"},

		// linux-arm
		{"go1.16", "linux-arm64", "linux-arm64-aws"},
		{"go1.16", "linux-armv6l", "linux-arm-aws"},
		{"go1.17", "linux-arm64", "linux-arm64-aws"},
		{"go1.17", "linux-armv6l", "linux-arm-aws"},
		{"go1.18", "linux-arm64", "linux-arm64-aws"},
		{"go1.18", "linux-armv6l", "linux-arm-aws"},

		// FreeBSD
		{"go1.16rc2", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.16rc2", "freebsd-386", "freebsd-386-11_2"},
		{"go1.16", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.16", "freebsd-386", "freebsd-386-11_2"},
		{"go1.16.1", "freebsd-amd64", "freebsd-amd64-11_2"},
		{"go1.16.1", "freebsd-386", "freebsd-386-11_2"},
		// Go 1.17 continues to use the the FreeBSD 11.4 builder.
		{"go1.17rc2", "freebsd-amd64", "freebsd-amd64-11_4"},
		{"go1.17rc2", "freebsd-386", "freebsd-386-11_4"},
		{"go1.17", "freebsd-amd64", "freebsd-amd64-11_4"},
		{"go1.17", "freebsd-386", "freebsd-386-11_4"},
		// Go 1.18 use the the FreeBSD 12.2 builder.
		{"go1.18", "freebsd-amd64", "freebsd-amd64-12_2"},
		{"go1.18", "freebsd-386", "freebsd-386-12_2"},

		// macOS
		// Go 1.16 uses MacOS 10.15.
		{"go1.16", "darwin-amd64", "darwin-amd64-10_15"},
		// Go 1.17 uses MacOS 11.0.
		{"go1.17", "darwin-amd64", "darwin-amd64-11_0"},
		// Go 1.18 uses MacOS 11.0.
		{"go1.18", "darwin-amd64", "darwin-amd64-11_0"},

		// Windows
		// Go 1.16 & 1.17 & 1.18 use Windows 2008.
		{"go1.16", "windows-386", "windows-386-2008"},
		{"go1.16", "windows-amd64", "windows-amd64-2008"},
		{"go1.17", "windows-386", "windows-386-2008"},
		{"go1.17", "windows-amd64", "windows-amd64-2008"},
		{"go1.18", "windows-386", "windows-386-2008"},
		{"go1.18", "windows-amd64", "windows-amd64-2008"},
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
