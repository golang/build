// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"
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
