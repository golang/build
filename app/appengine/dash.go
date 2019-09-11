// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/gob"
	"net/http"
	"sort"
	"strings"

	"google.golang.org/appengine"
)

func main() {
	gob.Register(&Commit{}) // needed for google.golang.org/appengine/delay

	// admin handlers
	handleFunc("/init", initHandler)
	handleFunc("/key", keyHandler)

	// authenticated handlers
	handleFunc("/building", AuthHandler(buildingHandler))
	handleFunc("/clear-results", AuthHandler(clearResultsHandler))
	handleFunc("/commit", AuthHandler(commitHandler))
	handleFunc("/packages", AuthHandler(packagesHandler))
	handleFunc("/perf-result", AuthHandler(perfResultHandler))
	handleFunc("/result", AuthHandler(resultHandler))
	handleFunc("/tag", AuthHandler(tagHandler))
	handleFunc("/todo", AuthHandler(todoHandler))

	// public handlers
	handleFunc("/", uiHandler)
	handleFunc("/log/", logHandler)
	handleFunc("/perf", perfChangesHandler)
	handleFunc("/perfdetail", perfDetailUIHandler)
	handleFunc("/perfgraph", perfGraphHandler)
	handleFunc("/updatebenchmark", updateBenchmark)
	handleFunc("/buildtest", testHandler)
	handleFunc("/perflearn", perfLearnHandler)

	appengine.Main()
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
type Dashboard struct {
	Name      string     // This dashboard's name (eg, "Go")
	Namespace string     // This dashboard's namespace (eg, "" (default), "Git")
	Prefix    string     // The path prefix (no trailing /)
	Packages  []*Package // The project's packages to build
}

// Context returns a namespaced context for this dashboard, or panics if it
// fails to create a new context.
func (d *Dashboard) Context(ctx context.Context) context.Context {
	if d.Namespace == "" {
		return ctx
	}
	n, err := appengine.Namespace(ctx, d.Namespace)
	if err != nil {
		panic(err)
	}
	return n
}

// goDash is the dashboard for the main go repository.
var goDash = &Dashboard{
	Name:      "Go",
	Namespace: "Git",
	Prefix:    "",
	Packages:  goPackages,
}

// goPackages is a list of all of the packages built by the main go repository.
var goPackages = []*Package{
	{
		Kind: "go",
		Name: "Go",
	},
	{
		Kind: "subrepo",
		Name: "arch",
		Path: "golang.org/x/arch",
	},
	{
		Kind: "subrepo",
		Name: "benchmarks",
		Path: "golang.org/x/benchmarks",
	},
	{
		Kind: "subrepo",
		Name: "blog",
		Path: "golang.org/x/blog",
	},
	{
		Kind: "subrepo",
		Name: "crypto",
		Path: "golang.org/x/crypto",
	},
	{
		Kind: "subrepo",
		Name: "debug",
		Path: "golang.org/x/debug",
	},
	{
		Kind: "subrepo",
		Name: "exp",
		Path: "golang.org/x/exp",
	},
	{
		Kind: "subrepo",
		Name: "image",
		Path: "golang.org/x/image",
	},
	{
		Kind: "subrepo",
		Name: "mobile",
		Path: "golang.org/x/mobile",
	},
	{
		Kind: "subrepo",
		Name: "net",
		Path: "golang.org/x/net",
	},
	{
		Kind: "subrepo",
		Name: "oauth2",
		Path: "golang.org/x/oauth2",
	},
	{
		Kind: "subrepo",
		Name: "perf",
		Path: "golang.org/x/perf",
	},
	{
		Kind: "subrepo",
		Name: "review",
		Path: "golang.org/x/review",
	},
	{
		Kind: "subrepo",
		Name: "sync",
		Path: "golang.org/x/sync",
	},
	{
		Kind: "subrepo",
		Name: "sys",
		Path: "golang.org/x/sys",
	},
	{
		Kind: "subrepo",
		Name: "talks",
		Path: "golang.org/x/talks",
	},
	{
		Kind: "subrepo",
		Name: "term",
		Path: "golang.org/x/term",
	},
	{
		Kind: "subrepo",
		Name: "text",
		Path: "golang.org/x/text",
	},
	{
		Kind: "subrepo",
		Name: "time",
		Path: "golang.org/x/time",
	},
	{
		Kind: "subrepo",
		Name: "tools",
		Path: "golang.org/x/tools",
	},
	{
		Kind: "subrepo",
		Name: "tour",
		Path: "golang.org/x/tour",
	},
	{
		Kind: "subrepo",
		Name: "website",
		Path: "golang.org/x/website",
	},
}

// supportedReleaseBranches returns a slice containing the most recent two non-security release branches
// contained in branches.
func supportedReleaseBranches(branches []string) (supported []string) {
	for _, b := range branches {
		if !strings.HasPrefix(b, "release-branch.go1.") ||
			len(b) != len("release-branch.go1.nn") { // assumes nn in range [10, 99]
			continue
		}
		supported = append(supported, b)
	}
	sort.Strings(supported)
	if len(supported) > 2 {
		supported = supported[len(supported)-2:]
	}
	return supported
}
