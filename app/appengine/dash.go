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

	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/repos"
	"golang.org/x/net/http2"
	"google.golang.org/appengine"
	"grpc.go4.org" // simpler, uses x/net/http2.Transport; we use this elsewhere in x/build
)

var maintnerClient = createMaintnerClient()

func main() {
	// authenticated handlers
	handleFunc("/building", AuthHandler(buildingHandler))          // called by coordinator during builds
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

	appengine.Main()
}

func staticDir() string {
	if pwd, _ := os.Getwd(); strings.HasSuffix(pwd, "app/appengine") {
		return "static"
	}
	return "app/appengine/static"
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

// Context returns a namespaced context for this dashboard, or panics if it
// fails to create a new context.
func (d *Dashboard) Context(ctx context.Context) context.Context {
	n, err := appengine.Namespace(ctx, "Git")
	if err != nil {
		panic(err)
	}
	return n
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
