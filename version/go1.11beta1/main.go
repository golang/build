// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The go1.11beta1 command runs the go command from go1.11beta1.
//
// To install, run:
//
//     $ go get golang.org/x/build/version/go1.11beta1
//     $ go1.11beta1 download
//
// And then use the go1.11beta1 command as if it were your normal go
// command.
//
// See the release notes at https://tip.golang.org/doc/go1.11
//
// File bugs at https://golang.org/issues/new
package main

import "golang.org/x/build/version"

func main() {
	version.Run("go1.11beta1")
}
