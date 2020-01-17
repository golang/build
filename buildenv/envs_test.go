// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildenv

import (
	"testing"
)

func TestEnvironmentNextZone(t *testing.T) {
	testCases := []struct {
		name      string
		env       Environment
		wantOneOf []string // desired zone should appear in this slice
	}{
		{
			name: "zones-not-set",
			env: Environment{
				ControlZone: "kentucky",
				VMZones:     []string{},
			},
			wantOneOf: []string{"kentucky"},
		},
		{
			name: "zone-and-zones-set",
			env: Environment{
				ControlZone: "kentucky",
				VMZones:     []string{"texas", "california", "washington"},
			},

			wantOneOf: []string{"texas", "california", "washington"},
		},
		{
			name: "zones-only-contains-one-entry",
			env: Environment{
				ControlZone: "kentucky",
				VMZones:     []string{"texas"},
			},
			wantOneOf: []string{"texas"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.env.RandomVMZone()
			if !containsString(got, tc.wantOneOf) {
				t.Errorf("got=%q; want %v", got, tc.wantOneOf)
			}
		})
	}
}

func containsString(item string, items []string) bool {
	for _, s := range items {
		if item == s {
			return true
		}
	}
	return false
}
