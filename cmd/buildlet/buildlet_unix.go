// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux
// (We only care about Linux on GKE for now)

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func init() {
	registerSignal = registerSignalUnix
}

func registerSignalUnix(c chan<- os.Signal) {
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
}
