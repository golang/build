// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildenv

import (
	"slices"
	"testing"
)

func TestEnvironmentNextZone(t *testing.T) {
	env := Environment{
		VMZones: []string{"texas", "california", "washington"},
	}
	wantOneOf := []string{"texas", "california", "washington"}
	got := env.RandomVMZone()
	if !slices.Contains(wantOneOf, got) {
		t.Errorf("got=%q; want %v", got, wantOneOf)
	}
}
