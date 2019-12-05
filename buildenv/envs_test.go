// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildenv

import (
	"testing"
)

func TestEnvironmentNextZone(t *testing.T) {
	testCases := []struct {
		name     string
		env      Environment
		wantZone []string // desired zone should appear in this slice
	}{
		{
			name: "zones-not-set",
			env: Environment{
				Zone:    "kentucky",
				VMZones: []string{},
			},
			wantZone: []string{"kentucky"},
		},
		{
			name: "zone-and-zones-set",
			env: Environment{
				Zone:    "kentucky",
				VMZones: []string{"texas", "california", "washington"},
			},

			wantZone: []string{"texas", "california", "washington"},
		},
		{
			name: "zones-only-contains-one-entry",
			env: Environment{
				Zone:    "kentucky",
				VMZones: []string{"texas"},
			},
			wantZone: []string{"texas"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			e := Environment{
				Zone:    tc.env.Zone,
				VMZones: tc.env.VMZones,
			}
			got := e.RandomVMZone()
			if !containsString(got, tc.wantZone) {
				t.Errorf("got=%q; want %v", got, tc.wantZone)
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
