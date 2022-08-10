// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import "testing"

func TestQueryName(t *testing.T) {
	cases := []struct {
		desc  string
		query string
		want  string
	}{
		{
			desc:  "named query",
			query: "-- name: SelectOne :one\nSELECT 1;",
			want:  "SelectOne",
		},
		{
			desc:  "empty string",
			query: "",
			want:  "Unknown",
		},
		{
			desc:  "missing name comment",
			query: "SELECT 1\nLIMIT 1;",
			want:  "Unknown",
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := queryName(c.query)
			if got != c.want {
				t.Errorf("queryName(%q) = %q, wanted %q", c.query, got, c.want)
			}
		})
	}
}
