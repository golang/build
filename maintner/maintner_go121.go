// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.21

package maintner

import "slices"

func slicesContains[S ~[]E, E comparable](s S, v E) bool {
	return slices.Contains(s, v)
}

func slicesDeleteFunc[S ~[]E, E any](s S, del func(E) bool) S {
	return slices.DeleteFunc(s, del)
}
