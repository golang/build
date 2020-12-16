// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTargetSelectionPerGoVersion(t *testing.T) {
	targetNames := func(targets []Target) (names []string) {
		for _, t := range targets {
			names = append(names, t.Name)
		}
		return names
	}

	for _, tc := range []struct {
		goVer []string // Go versions to test.
		want  []string // Expected release targets.
	}{
		{
			goVer: []string{"go1.16beta1", "go1.16rc1", "go1.16", "go1.16.1"},
			want: []string{
				"src",
				"linux-386",
				"linux-armv6l",
				"linux-amd64",
				"linux-arm64",
				"freebsd-386",
				"freebsd-amd64",
				"windows-386",
				"windows-amd64",
				"darwin-amd64",
				"darwin-arm64", // New to Go 1.16.
				"linux-s390x",
				"linux-ppc64le",
				"linux-amd64-longtest",
				"windows-amd64-longtest",
			},
		},
		{
			goVer: []string{"go1.15.7", "go1.14.14"},
			want: []string{
				"src",
				"linux-386",
				"linux-armv6l",
				"linux-amd64",
				"linux-arm64",
				"freebsd-386",
				"freebsd-amd64",
				"windows-386",
				"windows-amd64",
				"darwin-amd64",
				"linux-s390x",
				"linux-ppc64le",
				"linux-amd64-longtest",
				"windows-amd64-longtest",
			},
		},
	} {
		for _, goVer := range tc.goVer {
			t.Run(goVer, func(t *testing.T) {
				got := matchTargets(goVer)
				if diff := cmp.Diff(tc.want, targetNames(got)); diff != "" {
					t.Errorf("release target mismatch (-want +got):\n%s", diff)
				}
			})
		}
	}
}

func TestAllQueriesSupported(t *testing.T) {
	for _, r := range releaseTargets {
		t.Run(r.Name, func(t *testing.T) {
			defer func() {
				if err := recover(); err != nil {
					t.Errorf("target %s uses an unsupported version query:\n%v", r.Name, err)
				}
			}()
			match(r.GoQuery, "go1.15.7") // Shouldn't panic for any r.GoQuery.
		})
	}
}
