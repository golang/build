// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package pool

import (
	"testing"
	"time"

	"golang.org/x/build/dashboard"
)

func TestPoolDetermineDeleteTimeout(t *testing.T) {
	testCases := []struct {
		desc        string
		hostValue   time.Duration
		wantTimeout time.Duration
	}{
		{
			desc:        "default",
			wantTimeout: 2 * time.Hour,
		},
		{
			desc:        "from-host",
			hostValue:   8 * time.Hour,
			wantTimeout: 8 * time.Hour,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			h := &dashboard.HostConfig{
				CustomDeleteTimeout: tc.hostValue,
			}
			if got := determineDeleteTimeout(h); got != tc.wantTimeout {
				t.Errorf("determineDeleteTimeout(%+v) = %s; want %s", h, got, tc.wantTimeout)
			}
		})
	}
}

func TestPoolIsBuildlet(t *testing.T) {
	testCases := []struct {
		desc string
		name string
		want bool
	}{
		{"valid", "buildlet-gce-tinker", true},
		{"invalid", "gce-tinker", false},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := isBuildlet(tc.name); got != tc.want {
				t.Errorf("isBuildlet(%q) = %t; want %t", tc.name, got, tc.want)
			}
		})
	}
}
