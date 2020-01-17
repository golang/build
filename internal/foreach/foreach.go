// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package foreach provides allocation-conscious helpers
// for iterating over lines of text.
//
// They're factored out into a separate small package primarily
// to allow them to have allocation-measuring tests that need
// to run without interference from other goroutine-leaking tests.
package foreach

import (
	"bytes"
	"strings"
)

// Line calls f on each line in v, without the trailing '\n'.
// The final line need not include a trailing '\n'.
// Returns first non-nil error returned by f.
func Line(v []byte, f func([]byte) error) error {
	for len(v) > 0 {
		i := bytes.IndexByte(v, '\n')
		if i < 0 {
			return f(v)
		}
		if err := f(v[:i]); err != nil {
			return err
		}
		v = v[i+1:]
	}
	return nil
}

// LineStr calls f on each line in s, without the trailing '\n'.
// The final line need not include a trailing '\n'.
// Returns first non-nil error returned by f.
//
// LineStr is the string variant of Line.
func LineStr(s string, f func(string) error) error {
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return f(s)
		}
		if err := f(s[:i]); err != nil {
			return err
		}
		s = s[i+1:]
	}
	return nil
}
