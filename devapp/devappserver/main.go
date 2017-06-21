// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Devapp generates the dashboard that powers dev.golang.org. This binary is
// designed to be run outside of an App Engine context.
//
// Usage:
//
//	devappserver --http=:8081
//
// By default devappserver listens on port 80.
//
// For the moment, Github issues and Gerrit CL's are stored in memory
// in the running process. To trigger an initial download, visit
// http://localhost:8081/update and/or http://localhost:8081/update/stats in
// your browser.

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	_ "golang.org/x/build/devapp" // registers HTTP handlers
)

func init() {
	flag.Usage = func() {
		os.Stderr.WriteString(`usage: devappserver [-http=addr]

devappserver generates the dashboard that powers dev.golang.org.
	`)
	}
}

func main() {
	httpAddr := flag.String("http", ":80", "HTTP service address (e.g., ':8080')")
	flag.Parse()
	ln, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listening on %s: %v\n", *httpAddr, err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "Serving at %s\n", ln.Addr().String())
	log.Fatal(http.Serve(ln, nil))
}
