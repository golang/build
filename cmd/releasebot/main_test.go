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
			goVer: []string{
				"go1.17beta1", "go1.17rc1", "go1.17", "go1.17.1",
				"go1.16.3",
			},
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
				"linux-386-longtest",
				"linux-amd64-longtest",
				"windows-amd64-longtest",
			},
		},
		{
			goVer: []string{"go1.15.11"},
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
				"linux-386-longtest",
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

func TestSplitLogMessage(t *testing.T) {
	testCases := []struct {
		desc   string
		str    string
		maxLen int
		want   []string
	}{
		{
			desc:   "string matches max size",
			str:    "the quicks",
			maxLen: 10,
			want:   []string{"the quicks"},
		},
		{
			desc:   "string greater than max size",
			str:    "the quick brown fox",
			maxLen: 10,
			want:   []string{"the quick ", "brown fox"},
		},
		{
			desc:   "string smaller than max size",
			str:    "the quick",
			maxLen: 20,
			want:   []string{"the quick"},
		},
		{
			desc:   "string matches max size with return",
			str:    "the quick\n",
			maxLen: 10,
			want:   []string{"the quick\n"},
		},
		{
			desc:   "string greater than max size with return",
			str:    "the quick\n brown fox",
			maxLen: 10,
			want:   []string{"the quick", " brown fox"},
		},
		{
			desc:   "string smaller than max size with return",
			str:    "the \nquick",
			maxLen: 20,
			want:   []string{"the \nquick"},
		},
		{
			desc:   "string is multiples of max size",
			str:    "000000000011111111112222222222",
			maxLen: 10,
			want:   []string{"0000000000", "1111111111", "2222222222"},
		},
		{
			desc:   "string is multiples of max size with return",
			str:    "000000000\n111111111\n222222222\n",
			maxLen: 10,
			want:   []string{"000000000", "111111111", "222222222\n"},
		},
		{
			desc:   "string is multiples of max size with extra return",
			str:    "000000000\n111111111\n222222222\n\n",
			maxLen: 10,
			want:   []string{"000000000", "111111111", "222222222", "\n"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			got := splitLogMessage(tc.str, tc.maxLen)
			if !cmp.Equal(tc.want, got) {
				t.Errorf("splitStringToSlice(%q, %d) =\ngot  \t %#v\nwant \t %#v", tc.str, tc.maxLen, got, tc.want)
			}
		})
	}
}
