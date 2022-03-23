// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This binary contains an App Engine app for perf.golang.org
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/build/perf/app"
	"golang.org/x/build/perfdata"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
)

func mustGetenv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Panicf("%s environment variable not set.", k)
	}
	return v
}

// appHandler is the default handler, registered to serve "/".
// It creates a new App instance using the appengine Context and then
// dispatches the request to the App. The environment variable
// STORAGE_URL_BASE must be set in app.yaml with the name of the bucket to
// write to.
func appHandler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	// urlfetch defaults to 5s timeout if the context has no timeout.
	// The underlying request has a 60 second timeout, so we might as well propagate that here.
	// (Why doesn't appengine do that for us?)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	app := &app.App{
		BaseDir: "analysis/appengine", // relative to module root
		StorageClient: &perfdata.Client{
			BaseURL:    mustGetenv("STORAGE_URL_BASE"),
			HTTPClient: http.DefaultClient,
		},
	}
	mux := http.NewServeMux()
	app.RegisterOnMux(mux)
	mux.ServeHTTP(w, r)
}

func main() {
	http.HandleFunc("/", appHandler)
	appengine.Main()
}
