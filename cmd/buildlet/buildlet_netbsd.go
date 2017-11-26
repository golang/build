// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func init() {
	setOSRlimit = setNetBSDRlimit
}

// See https://github.com/golang/go/issues/22871#issuecomment-346888363
func setNetBSDRlimit() error {
	limit := unix.Rlimit{
		Cur: unix.RLIM_INFINITY,
		Max: unix.RLIM_INFINITY,
	}
	if err := unix.Setrlimit(unix.RLIMIT_DATA, &limit); err != nil && os.Getuid() == 0 {
		return err
	}
	return nil
}
