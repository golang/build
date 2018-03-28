// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The 1.10.1 command runs the go command from 1.10.1.
//
// To install, run:
//
//     $ go get golang.org/x/build/version/1.10.1
//     $ 1.10.1 download
//
// And then use the 1.10.1 command as if it were your normal go
// command.
//
// See the release notes at https://golang.org/doc/1.10.1
//
// File bugs at https://golang.org/issues/new
package main

import "golang.org/x/build/version"

func main() {
	version.Run("1.10.1")
}
