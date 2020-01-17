// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore

// The arhost binary is a wrapper for llvm-ar, designed to be called by
// cmd/link. The -extar flag is not enough because llvm-ar and ar don't
// use the same flags.
//
// It is useful when the standard ar is broken, such as on self-hosted
// iOS.
package main

import (
	"os"
	"os/exec"
)

func main() {
	args := os.Args[1:]
	// cmd/link invokes ar with -q -c -s. Replace with
	// just q.
	for i := len(args) - 1; i >= 0; i-- {
		switch args[i] {
		case "-q":
			args[i] = "q"
		case "-c", "-s":
			args = append(args[:i], args[i+1:]...)
		}
	}
	cmd := exec.Command("llvm-ar", args...)
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
