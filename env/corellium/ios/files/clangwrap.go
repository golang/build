// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore

// The clanghost binary is like clangwrap.sh but for self-hosted iOS.
//
// Use -ldflags="-X main.sdkpath=<path to iPhoneOS.sdk>" when building
// the wrapper.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

var sdkpath = ""

func main() {
	if sdkpath == "" {
		fmt.Fprintf(os.Stderr, "no SDK is set; use -ldflags=\"-X main.sdkpath=<sdk path>\" when building this wrapper.\n")
		os.Exit(1)
	}
	args := os.Args[1:]
	cmd := exec.Command("clang", "-isysroot", sdkpath, "-mios-version-min=6.0")
	cmd.Args = append(cmd.Args, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if err, ok := err.(*exec.ExitError); ok {
			os.Exit(err.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
}
