// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"golang.org/x/build/internal/releasetargets"
)

func TestMinSupportedMacOSVersion(t *testing.T) {
	testCases := []struct {
		goVer     string
		wantMacOS string
	}{
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
	matchBuilds := func(t *testing.T, target, goVer string) (matched []*Build) {
		targets, ok := releasetargets.TargetsForVersion(goVer)
		if !ok {
			t.Fatalf("failed to parse %q", goVer)
		}
		for _, b := range targetsToBuilds(targets) {
			if b.String() != target {
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
		// Go 1.17 use the the Stretch builders.
		{"go1.17", "linux-amd64", "linux-amd64-stretch"},
		{"go1.17", "linux-386", "linux-386-stretch"},
		// Go 1.18 use the the Stretch builders.
		{"go1.18", "linux-amd64", "linux-amd64-stretch"},
		{"go1.18", "linux-386", "linux-386-stretch"},

		// linux-arm
		{"go1.17", "linux-arm64", "linux-arm64-aws"},
		{"go1.17", "linux-armv6l", "linux-arm-aws"},
		{"go1.18", "linux-arm64", "linux-arm64-aws"},
		{"go1.18", "linux-armv6l", "linux-arm-aws"},

		// FreeBSD
		// Go 1.17 continues to use the the FreeBSD 11.4 builder.
		{"go1.17rc2", "freebsd-amd64", "freebsd-amd64-11_4"},
		{"go1.17rc2", "freebsd-386", "freebsd-386-11_4"},
		{"go1.17", "freebsd-amd64", "freebsd-amd64-11_4"},
		{"go1.17", "freebsd-386", "freebsd-386-11_4"},
		// Go 1.18 use the the FreeBSD 12.3 builder.
		{"go1.18", "freebsd-amd64", "freebsd-amd64-12_3"},
		{"go1.18", "freebsd-386", "freebsd-386-12_3"},

		// macOS (amd64)
		// Go 1.17 uses MacOS 11.0.
		{"go1.17", "darwin-amd64", "darwin-amd64-11_0"},
		// Go 1.18 starts using a macOS 12 releaselet.
		{"go1.18", "darwin-amd64", "darwin-amd64-12_0"},
		// macOS (arm64)
		// Go 1.16 and 1.17 use macOS 11.
		{"go1.17", "darwin-arm64", "darwin-arm64-11_0-toothrot"},
		// Go 1.18 starts using a macOS 12 releaselet.
		{"go1.18", "darwin-arm64", "darwin-arm64-12_0-toothrot"},

		// Windows
		// Go 1.17 & 1.18 use Windows 2008.
		{"go1.17", "windows-386", "windows-386-2008"},
		{"go1.17", "windows-amd64", "windows-amd64-2008"},
		{"go1.18", "windows-386", "windows-386-2008"},
		{"go1.18", "windows-amd64", "windows-amd64-2008"},
	}
	for _, tc := range testCases {
		t.Run(tc.target+"@"+tc.goVer, func(t *testing.T) {
			builds := matchBuilds(t, tc.target, tc.goVer)
			if len(builds) != 1 {
				t.Fatalf("got %d matching builds; want 1", len(builds))
			}
			if got, want := builds[0].Builder, tc.wantBuilder; got != want {
				t.Errorf("got %s; want %s", got, want)
			}
		})
	}
}
