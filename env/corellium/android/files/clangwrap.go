// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"os/exec"
)

func main() {
	args := os.Args[1:]
	cmd := exec.Command("clang")
	if os.Getenv("GOARCH") == "arm" {
		pref := os.Getenv("PREFIX")
		cmd.Args = append(cmd.Args, "-target", "armv7a-linux-androideabi", "-Qunused-arguments", "-Wl,-rpath-link="+pref+"/../home/arm-linux-androideabi/lib", "-L"+pref+"/../home/arm-linux-androideabi/lib", "-B"+pref+"/../home/arm-linux-androideabi/lib")
	} else {
		cmd.Args = append(cmd.Args, "-Qunused-arguments", "-fuse-ld=lld")
	}
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
