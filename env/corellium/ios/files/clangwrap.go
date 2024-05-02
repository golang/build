// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

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
	// Intercept requests for the path of the "ar" tool and instead
	// always return "ar", so that the "ar" wrapper is used instead of
	// /usr/bin/ar. See issue https://go.dev/issue/59221 and CL
	// https://go.dev/cl/479775 for more detail.
	if len(args) != 0 && args[0] == "--print-prog-name=ar" {
		fmt.Printf("ar\n")
		os.Exit(0)
	}
	cmd := exec.Command("clang", "-isysroot", sdkpath, "-mios-version-min=12.0")
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
