// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

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
		timeout     time.Duration
		wantTimeout time.Duration
	}{
		{
			desc:        "from-host",
			hostValue:   time.Minute,
			timeout:     time.Second,
			wantTimeout: time.Minute,
		},
		{
			desc:        "from-argument",
			hostValue:   0,
			timeout:     time.Second,
			wantTimeout: time.Second,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			h := &dashboard.HostConfig{
				DeleteTimeout: tc.hostValue,
			}
			if got := determineDeleteTimeout(h, tc.timeout); got != tc.wantTimeout {
				t.Errorf("determineDeleteTimeout(%+v, %s) = %s; want %s", h, tc.timeout, got, tc.wantTimeout)
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
