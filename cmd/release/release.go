// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command release was used to build Go releases before relui fully replaced its functionality.
//
// Deprecated: Use relui instead.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Command golang.org/x/build/cmd/release is deprecated.")
		fmt.Fprintln(os.Stderr, "Use golang.org/x/build/cmd/relui instead.")
		flag.PrintDefaults()
	}
	flag.Parse()

	flag.Usage()
	os.Exit(1)
}
