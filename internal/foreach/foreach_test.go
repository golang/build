// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package foreach_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/build/internal/foreach"
)

func ExampleLine() {
	v := []byte(`line 1
line 2
line 3


after two blank lines
last line`)
	foreach.Line(v, func(b []byte) error {
		fmt.Printf("%q\n", b)
		return nil
	})

	// Output:
	// "line 1"
	// "line 2"
	// "line 3"
	// ""
	// ""
	// "after two blank lines"
	// "last line"
}

func TestLineAllocs(t *testing.T) {
	v := bytes.Repeat([]byte(`waefaweflk
awfkjlawfe
wfaweflkjawfewfawef

awefwaejflk
awfljwfael
afjwewefk`), 1000)
	allocs := testing.AllocsPerRun(1000, func() {
		foreach.Line(v, func([]byte) error { return nil })
	})
	if allocs > 0.1 {
		t.Errorf("got allocs = %v; want zero", allocs)
	}
}

func TestLineStrAllocs(t *testing.T) {
	s := strings.Repeat(`waefaweflk
awfkjlawfe
wfaweflkjawfewfawef

awefwaejflk
awfljwfael
afjwewefk`, 1000)
	allocs := testing.AllocsPerRun(1000, func() {
		foreach.LineStr(s, func(string) error { return nil })
	})
	if allocs > 0.1 {
		t.Errorf("got allocs = %v; want zero", allocs)
	}
}
