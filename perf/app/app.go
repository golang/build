// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package app implements the performance data analysis server.
package app

import (
	"embed"
	"net/http"

	"github.com/google/safehtml/template"
	"golang.org/x/build/perfdata"
)

var (
	//go:embed template/*
	tmplEmbedFS embed.FS
	tmplFS      = template.TrustedFSFromEmbed(tmplEmbedFS)
)

// App manages the analysis server logic.
// Construct an App instance and call RegisterOnMux to connect it with an HTTP server.
type App struct {
	// StorageClient is used to talk to the perfdata server.
	StorageClient *perfdata.Client

	// BaseDir is the directory containing the "template" directory.
	// If empty, the current directory will be used.
	BaseDir string

	// InfluxHost is the host URL of the perf InfluxDB server.
	InfluxHost string

	// InfluxToken is the Influx auth token for connecting to InfluxHost.
	//
	// If empty, we attempt to fetch the token from Secret Manager using
	// InfluxProject.
	InfluxToken string

	// InfluxProject is the GCP project ID containing the InfluxDB secrets.
	//
	// If empty, this defaults to the project this service is running as.
	//
	// Only used if InfluxToken is empty.
	InfluxProject string

	// AuthCronEmail is the service account email which requests to
	// /cron/syncinflux must contain an OICD authentication token for, with
	// audience "/cron/syncinflux".
	//
	// If empty, no authentication is required.
	AuthCronEmail string
}

// RegisterOnMux registers the app's URLs on mux.
func (a *App) RegisterOnMux(mux *http.ServeMux) {
	mux.HandleFunc("/", a.index)
	mux.HandleFunc("/search", a.search)
	mux.HandleFunc("/compare", a.compare)
	mux.HandleFunc("/cron/syncinflux", a.syncInflux)
	a.dashboardRegisterOnMux(mux)
}

// search handles /search.
// This currently just runs the compare handler, until more analysis methods are implemented.
func (a *App) search(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if r.Header.Get("Accept") == "text/plain" || r.Header.Get("X-Benchsave") == "1" {
		// TODO(quentin): Switch to real Accept negotiation when golang/go#19307 is resolved.
		// Benchsave sends both of these headers.
		a.textCompare(w, r)
		return
	}
	// TODO(quentin): Intelligently choose an analysis method
	// based on the results from the query, once there is more
	// than one analysis method.
	//q := r.Form.Get("q")
	a.compare(w, r)
}
