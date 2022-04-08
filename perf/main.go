// Copyright 2022 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// perf runs an HTTP server for benchmark analysis.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"golang.org/x/build/internal/https"
	"golang.org/x/build/perf/app"
	"golang.org/x/build/perfdata"
)

var perfdataURL = flag.String("perfdata", "https://perfdata.golang.org", "perfdata server base `url`")

func main() {
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()

	app := &app.App{
		StorageClient: &perfdata.Client{BaseURL: *perfdataURL},
	}
	mux := http.NewServeMux()
	app.RegisterOnMux(mux)

	ctx := context.Background()
	log.Fatal(https.ListenAndServe(ctx, mux))
}
