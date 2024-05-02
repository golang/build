// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"testing"
	"time"
)

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		desc     string
		duration string
		want     string
	}{
		{
			desc:     "format days",
			duration: "99h2m1s",
			want:     "4d3h2m1s",
		},
		{
			desc:     "handle tiny durations",
			duration: "1ns",
			want:     "0s",
		},
		{
			desc:     "handle seconds",
			duration: "3s",
			want:     "3s",
		},
	}
	for _, c := range cases {
		t.Run(c.duration, func(t *testing.T) {
			d, err := time.ParseDuration(c.duration)
			if err != nil {
				t.Fatalf("time.ParseDuration(%q) = %q, %q, wanted no error", c.duration, d, err)
			}
			if got := humanDuration(d); got != c.want {
				t.Errorf("humanDuration(%v) = %q, wanted %q", d, got, c.want)
			}
		})
	}
}
