// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"cloud.google.com/go/datastore"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/repos"
	"golang.org/x/net/http2"
	"grpc.go4.org" // simpler, uses x/net/http2.Transport; we use this elsewhere in x/build
)

var (
	maintnerClient  = createMaintnerClient()
	datastoreClient = createDatastoreClient()
)

func main() {
	// authenticated handlers
	handleFunc("/clear-results", AuthHandler(clearResultsHandler)) // called by x/build/cmd/retrybuilds
	handleFunc("/result", AuthHandler(resultHandler))              // called by coordinator after build

	// public handlers
	handleFunc("/", uiHandler)
	handleFunc("/log/", logHandler)

	// We used to use App Engine's static file handling support, declared in app.yaml,
	// but it's currently broken with dev_appserver.py with the go111 runtime we use.
	// So just do it ourselves. It doesn't buy us enough to be worth it.
	fs := http.StripPrefix("/static", http.FileServer(http.Dir(staticDir())))
	handleFunc("/static/", fs.ServeHTTP)
	handleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://golang.org/favicon.ico", http.StatusFound)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

func staticDir() string {
	if pwd, _ := os.Getwd(); strings.HasSuffix(pwd, "app/appengine") {
		return "static"
	}
	return "app/appengine/static"
}

func createDatastoreClient() *datastore.Client {
	// First try with an empty project ID, so $DATASTORE_PROJECT_ID will be respected
	// if set.
	c, err := datastore.NewClient(context.Background(), "")
	if err == nil {
		return c
	}
	// Otherwise auto-detect it from the environment (that is,
	// work automatically in prod).
	c, err = datastore.NewClient(context.Background(), datastore.DetectProjectID)
	if err != nil {
		log.Fatalf("datastore.NewClient: %v", err)
	}
	return c
}

func createMaintnerClient() apipb.MaintnerServiceClient {
	addr := os.Getenv("MAINTNER_ADDR") // host[:port]
	if addr == "" {
		addr = "maintner.golang.org"
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			NextProtos:         []string{"h2"},
			InsecureSkipVerify: strings.HasPrefix(addr, "localhost:"),
		},
	}
	hc := &http.Client{Transport: tr}
	http2.ConfigureTransport(tr)

	cc, err := grpc.NewClient(hc, "https://"+addr)
	if err != nil {
		log.Fatal(err)
	}
	return apipb.NewMaintnerServiceClient(cc)
}

func handleFunc(path string, h http.HandlerFunc) {
	http.Handle(path, hstsHandler(h))
}

// hstsHandler wraps an http.HandlerFunc such that it sets the HSTS header.
func hstsHandler(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; preload")
		fn(w, r)
	})
}

// Dashboard describes a unique build dashboard.
//
// (There used to be more than one dashboard, so this is now somewhat
// less important than it once was.)
type Dashboard struct {
	Name     string     // This dashboard's name (always "Go" nowadays)
	Packages []*Package // The project's packages to build
}

// packageWithPath returns the Package in d with the provided importPath,
// or nil if none is found.
func (d *Dashboard) packageWithPath(importPath string) *Package {
	for _, p := range d.Packages {
		if p.Path == importPath {
			return p
		}
	}
	return nil
}

// goDash is the dashboard for the main go repository.
var goDash = &Dashboard{
	Name: "Go",
	Packages: []*Package{
		{Name: "Go"},
	},
}

func init() {
	var add []*Package
	for _, r := range repos.ByGerritProject {
		if !r.ShowOnDashboard() {
			continue
		}
		add = append(add, &Package{
			Name: r.GoGerritProject,
			Path: r.ImportPath,
		})
	}
	sort.Slice(add, func(i, j int) bool {
		return add[i].Name < add[j].Name
	})
	goDash.Packages = append(goDash.Packages, add...)
}
