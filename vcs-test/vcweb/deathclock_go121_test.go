// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.22

package main_test

func init() {
	// When development begins for the Go 1.22 cycle, the supported Go
	// releases will be Go 1.20 and 1.21.
	go119Unsupported = true
}
