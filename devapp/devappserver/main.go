// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Devapp generates the dashboard that powers dev.golang.org. This binary is
// designed to be run outside of an App Engine context.
//
// Usage:
//
//	devappserver --port=8081
//
// By default devapp listens on port 8081. You can also configure the port by
// setting the PORT environment variable (but not both).
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
		os.Stderr.WriteString(`usage: devapp [-port=port]

Devapp generates the dashboard that powers dev.golang.org.
	`)
	}
}

const defaultPort = "8081"

func main() {
	portFlag := flag.String("port", "", "Port to listen on")
	flag.Parse()
	if *portFlag != "" && os.Getenv("PORT") != "" {
		os.Stderr.WriteString("cannot set both $PORT and --port flags\n")
		os.Exit(2)
	}
	var port string
	if p := os.Getenv("PORT"); p != "" {
		port = p
	} else if *portFlag != "" {
		port = *portFlag
	} else {
		port = defaultPort
	}
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listening on %s: %v\n", port, err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "Listening on port %s\n", port)
	log.Fatal(http.Serve(ln, nil))
}
