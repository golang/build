// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package version

import (
	"strconv"
	"testing"
	"testing/quick"
)

func TestParseTag(t *testing.T) {
	tests := []struct {
		tagName                         string
		wantMajor, wantMinor, wantPatch int
		wantOK                          bool
	}{
		{"go1", 1, 0, 0, true},
		{"go1.2", 1, 2, 0, true},
		{"go1.2.3", 1, 2, 3, true},
		{"go23.45.67", 23, 45, 67, true},
		{"not-go", 0, 0, 0, false},
		{"go", 0, 0, 0, false},
		{"go.", 0, 0, 0, false},
		{"go1.", 0, 0, 0, false},
		{"go1-bad", 0, 0, 0, false},
		{"go1.2.", 0, 0, 0, false},
		{"go1.2-bad", 0, 0, 0, false},
		{"go1.2.3.", 0, 0, 0, false},
		{"go1.2.3-bad", 0, 0, 0, false},
		{"go1.2.3.4", 0, 0, 0, false},
		{"go0.0.0", 0, 0, 0, false},
		{"go-1", 0, 0, 0, false},
		{"go1.-2", 0, 0, 0, false},
		{"go1.2.-3", 0, 0, 0, false},
		{"go+1", 0, 0, 0, false},
		{"go01", 0, 0, 0, false},
		{"go001", 0, 0, 0, false},
		{"go1000", 0, 0, 0, false},
		{"go1.0", 0, 0, 0, false},
		{"go1.0.0", 0, 0, 0, false},
		{"go1.2.0", 0, 0, 0, false},
		{"go00", 0, 0, 0, false},
		{"go00.2", 0, 0, 0, false},
		{"go00.2.3", 0, 0, 0, false},
		{"go1.00", 0, 0, 0, false},
		{"go1.00.0", 0, 0, 0, false},
		{"go1.00.3", 0, 0, 0, false},
		{"go1.2.00", 0, 0, 0, false},
	}
	for i, tt := range tests {
		major, minor, patch, ok := ParseTag(tt.tagName)
		if got, want := ok, tt.wantOK; got != want {
			t.Errorf("#%d %q: got ok = %v; want %v", i, tt.tagName, got, want)
			continue
		}
		if !tt.wantOK {
			continue
		}
		if got, want := major, tt.wantMajor; got != want {
			t.Errorf("#%d %q: got major = %d; want %d", i, tt.tagName, got, want)
		}
		if got, want := minor, tt.wantMinor; got != want {
			t.Errorf("#%d %q: got minor = %d; want %d", i, tt.tagName, got, want)
		}
		if got, want := patch, tt.wantPatch; got != want {
			t.Errorf("#%d %q: got patch = %d; want %d", i, tt.tagName, got, want)
		}
	}
}

func TestParseReleaseBranch(t *testing.T) {
	tests := []struct {
		branchName           string
		wantMajor, wantMinor int
		wantOK               bool
	}{
		{"release-branch.go1", 1, 0, true},
		{"release-branch.go1.2", 1, 2, true},
		{"release-branch.go23.45", 23, 45, true},
		{"not-release-branch", 0, 0, false},
		{"release-branch.go", 0, 0, false},
		{"release-branch.go.", 0, 0, false},
		{"release-branch.go1.", 0, 0, false},
		{"release-branch.go1-bad", 0, 0, false},
		{"release-branch.go1.2.", 0, 0, false},
		{"release-branch.go1.2-bad", 0, 0, false},
		{"release-branch.go1.2.3", 0, 0, false},
		{"release-branch.go0.0", 0, 0, false},
		{"release-branch.go-1", 0, 0, false},
		{"release-branch.go1.-2", 0, 0, false},
		{"release-branch.go+1", 0, 0, false},
		{"release-branch.go01", 0, 0, false},
		{"release-branch.go001", 0, 0, false},
		{"release-branch.go1000", 0, 0, false},
		{"release-branch.go1.0", 0, 0, false},
		{"release-branch.go00", 0, 0, false},
		{"release-branch.go00.2", 0, 0, false},
		{"release-branch.go1.00", 0, 0, false},
	}
	for i, tt := range tests {
		major, minor, ok := ParseReleaseBranch(tt.branchName)
		if got, want := ok, tt.wantOK; got != want {
			t.Errorf("#%d %q: got ok = %v; want %v", i, tt.branchName, got, want)
			continue
		}
		if !tt.wantOK {
			continue
		}
		if got, want := major, tt.wantMajor; got != want {
			t.Errorf("#%d %q: got major = %d; want %d", i, tt.branchName, got, want)
		}
		if got, want := minor, tt.wantMinor; got != want {
			t.Errorf("#%d %q: got minor = %d; want %d", i, tt.branchName, got, want)
		}
	}
}

func TestParse0To999(t *testing.T) {
	// The only accepted inputs are numbers in [0, 999] range
	// in canonical string form. All other input should be rejected.
	// Build a complete map of inputs to answers.
	var golden = make(map[string]int) // input -> output
	for n := 0; n <= 999; n++ {
		golden[strconv.Itoa(n)] = n
	}

	// Numbers in [0, 999] range should be accepted.
	for in, want := range golden {
		got, ok := parse0To999(in)
		if !ok {
			t.Errorf("parse0To999(%q): got ok = false; want true", in)
			continue
		}
		if got != want {
			t.Errorf("parse0To999(%q): got n = %d; want %d", in, got, want)
		}
	}

	// All other numbers should be rejected.
	ints := func(x int) bool {
		gotN, gotOK := parse0To999(strconv.Itoa(x))
		wantN, wantOK := golden[strconv.Itoa(x)]
		return gotOK == wantOK && gotN == wantN
	}
	if err := quick.Check(ints, nil); err != nil {
		t.Error(err)
	}

	// All other strings should be rejected.
	strings := func(x string) bool {
		gotN, gotOK := parse0To999(x)
		wantN, wantOK := golden[x]
		return gotOK == wantOK && gotN == wantN
	}
	if err := quick.Check(strings, nil); err != nil {
		t.Error(err)
	}
}

func TestAllocs(t *testing.T) {
	got := testing.AllocsPerRun(1000, func() {
		ParseReleaseBranch("release-branch.go1.5")
	})
	if got > 0 {
		t.Fatalf("unexpected %v allocation(s)", got)
	}
}
