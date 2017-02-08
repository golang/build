// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The maintnerd command serves project maintainer data from Git,
// Github, and/or Gerrit.
package main

import (
	"flag"
	"log"
	"net"
)

var (
	listen = flag.String("listen", ":6343", "listen address")
)

func main() {
	flag.Parse()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	ln.Close() // TODO: use
	log.Fatal("TODO")
}
