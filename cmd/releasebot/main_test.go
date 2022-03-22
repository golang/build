// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

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
