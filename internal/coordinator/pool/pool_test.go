// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package pool

import (
	"context"
	"testing"
	"time"

	"golang.org/x/build/dashboard"
)

func TestPoolDetermineDeleteTimeout(t *testing.T) {
	testCases := []struct {
		desc        string
		ctxValue    interface{}
		hostValue   time.Duration
		timeout     time.Duration
		wantTimeout time.Duration
	}{
		{
			desc:        "from-context",
			ctxValue:    time.Hour,
			hostValue:   time.Minute,
			timeout:     time.Second,
			wantTimeout: time.Hour,
		},
		{
			desc:        "from-host",
			ctxValue:    nil,
			hostValue:   time.Minute,
			timeout:     time.Second,
			wantTimeout: time.Minute,
		},
		{
			desc:        "from-argument",
			ctxValue:    nil,
			hostValue:   0,
			timeout:     time.Second,
			wantTimeout: time.Second,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctxValue != nil {
				ctx = context.WithValue(ctx, BuildletTimeoutOpt{}, tc.ctxValue)
			}
			h := &dashboard.HostConfig{
				DeleteTimeout: tc.hostValue,
			}
			if got := determineDeleteTimeout(ctx, h, tc.timeout); got != tc.wantTimeout {
				t.Errorf("determineDeleteTimeout(%+v, %+v, %s) = %s; want %s", ctx, h, tc.timeout, got, tc.wantTimeout)
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
