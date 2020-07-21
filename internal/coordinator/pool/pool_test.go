// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pool

import (
	"context"
	"testing"
	"time"
)

func TestPoolDeleteTimeoutFromContextOrValue(t *testing.T) {
	testCases := []struct {
		desc        string
		ctxValue    interface{}
		timeout     time.Duration
		wantTimeout time.Duration
	}{
		{
			desc:        "from-context",
			ctxValue:    time.Hour,
			timeout:     time.Minute,
			wantTimeout: time.Hour,
		},
		{
			desc:        "from-argument",
			ctxValue:    nil,
			timeout:     time.Minute,
			wantTimeout: time.Minute,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctxValue != nil {
				ctx = context.WithValue(ctx, BuildletTimeoutOpt{}, tc.ctxValue)
			}
			if got := deleteTimeoutFromContextOrValue(ctx, tc.timeout); got != tc.wantTimeout {
				t.Errorf("deleteTimeoutFromContextOrValue(%+v, %s) = %s; want %s", ctx, tc.timeout, got, tc.wantTimeout)
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
