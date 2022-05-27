// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command broken lists the current Go builders with known issues.
//
// To test this program, cd to its directory and run:
//
//	go mod init
//	go get golang.org/x/build/dashboard@HEAD
//	go run .
//	rm go.mod go.sum
package main

import (
	"fmt"

	"golang.org/x/build/dashboard"
)

func main() {
	for _, b := range dashboard.Builders {
		if len(b.KnownIssues) > 0 {
			fmt.Println(b.Name)
		}
	}
}
