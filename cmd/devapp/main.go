// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Devapp generates the dashboard that powers dev.golang.org.
//
// Usage:
//
//	devapp --port=8081
//
// By default devapp listens on port 8081.
//
// Github issues and Gerrit CL's are stored in memory in the running process.
// To trigger an initial download, visit http://localhost:8081/update or
// http://localhost:8081/update/stats in your browser.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	_ "golang.org/x/build/devapp"
)

func init() {
	flag.Usage = func() {
		os.Stderr.WriteString(`usage: devapp [-port=port]

Devapp generates the dashboard that powers dev.golang.org.
`)
		os.Exit(2)
	}
}

func main() {
	port := flag.Uint("port", 8081, "Port to listen on")
	flag.Parse()
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "Listening on port %d\n", *port)
	log.Fatal(http.Serve(ln, nil))
}
