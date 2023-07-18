// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.23

package main_test

func init() {
	// By the time development begins for the Go 1.23 cycle, no matter how early
	// it happens, Go 1.19 will definitely be unsupported.
	go119Unsupported = true
}
