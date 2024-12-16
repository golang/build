// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package repos contains information about Go source repositories.
package repos

import "fmt"

type Repo struct {
	// GoGerritProject, if non-empty, is the repo's Gerrit project
	// name, such as "go", "net", or "sys".
	GoGerritProject string

	// ImportPath is the repo's import path.
	// It is empty for the main Go repo and other repos that do not
	// contain Go code.
	ImportPath string

	// MirrorToGitHub controls whether this repo is mirrored
	// from Gerrit to GitHub. If true, GoGerritProject and
	// gitHubRepo must both be defined.
	MirrorToGitHub bool

	// MirrorToCSRProject controls whether this repo is mirrored from
	// Gerrit to Cloud Source Repositories. If not empty, GoGerritProject
	// must be defined. It will be mirrored to a CSR repo in the given
	// project with the same name as the Gerrit repo.
	MirrorToCSRProject string

	// showOnDashboard is whether to show the repo on the bottom
	// of build.golang.org in the repo overview section.
	showOnDashboard bool

	// CoordinatorCanBuild reports whether this a repo that the
	// build coordinator knows how to build.
	CoordinatorCanBuild bool

	// GitHubRepo is the "org/repo" of where this repo exists on
	// GitHub. If MirrorToGitHub is true, this is the
	// destination.
	GitHubRepo string

	// WebsiteDesc is the description of the repo for showing on
	// https://golang.org/pkg/#subrepo.
	// It should be plain text. Hostnames may be auto-linkified.
	WebsiteDesc string
}

// ByGerritProject maps from a Gerrit project name ("go", "net", etc)
// to the Repo's information.
var ByGerritProject = map[string]*Repo{ /* initialized below */ }

// ByImportPath maps from an import path ("golang.org/x/net") to the
// Repo's information.
var ByImportPath = map[string]*Repo{ /* initialized below */ }

func init() {
	addMirrored("dl", importPath("golang.org/dl"), coordinatorCanBuild)
	addMirrored("gddo", importPath("github.com/golang/gddo"), archivedOnGitHub)
	addMirrored("go", coordinatorCanBuild, noDash, enableCSR("golang-org"))
	addMirrored("gofrontend")
	addMirrored("govulncheck-action")
	addMirrored("proposal")
	addMirrored("sublime-build")
	addMirrored("sublime-config")
	addMirrored("wiki")

	x("arch")
	x("benchmarks", desc("benchmarks to measure Go as it is developed"))
	x("blog", noDash)
	x("build", desc("build.golang.org's implementation"))
	x("crypto", desc("additional cryptography packages"))
	x("debug", desc("an experimental debugger for Go"))
	x("example", noDash)
	x("exp", desc("experimental and deprecated packages (handle with care; may change without warning)"))
	x("image", desc("additional imaging packages"))
	x("lint", noDash, archivedOnGitHub)
	x("mobile", desc("experimental support for Go on mobile platforms"))
	x("mod")
	x("net", desc("additional networking packages"))
	x("oauth2")
	x("oscar", desc("open source contributor agent architecture"))
	x("perf", desc("packages and tools for performance measurement, storage, and analysis"))
	x("pkgsite", desc("home of the pkg.go.dev website"), enableCSR("go-discovery"))
	x("pkgsite-metrics", desc("code for serving pkg.go.dev/metrics"), enableCSR("go-ecosystem"))
	x("playground", enableCSR("golang-org"))
	x("review", desc("a tool for working with Gerrit code reviews"))
	x("scratch", noDash)
	x("sync", desc("additional concurrency primitives"))
	x("sys", desc("packages for making system calls"))
	x("talks", noDash)
	x("telemetry", desc("telemetry server code and libraries"), enableCSR("go-telemetry"))
	x("term")
	x("text", desc("packages for working with text"))
	x("time", desc("additional time packages"))
	x("tools", desc("godoc, goimports, gorename, and other tools"))
	x("tour", noDash)
	x("vgo", noDash)
	x("vuln", desc("code for the Go Vulnerability Database"))
	x("vulndb", desc("reports for the Go Vulnerability Database"), enableCSR("go-vuln"))
	x("website", desc("home of the golang.org and go.dev websites"), enableCSR("golang-org"))
	x("xerrors", noDash)

	add(&Repo{GoGerritProject: "gollvm"})
	add(&Repo{GoGerritProject: "grpc-review"})

	add(&Repo{
		GoGerritProject: "open2opaque",
		MirrorToGitHub:  true,
		ImportPath:      "google.golang.org/open2opaque",
		GitHubRepo:      "golang/open2opaque",
	})

	add(&Repo{
		GoGerritProject: "protobuf",
		MirrorToGitHub:  true,
		ImportPath:      "google.golang.org/protobuf",
		GitHubRepo:      "protocolbuffers/protobuf-go",
	})

	add(&Repo{
		GoGerritProject:    "vscode-go",
		MirrorToGitHub:     true,
		GitHubRepo:         "golang/vscode-go",
		WebsiteDesc:        "Go extension for Visual Studio Code",
		MirrorToCSRProject: "go-vscode-go",
	})
}

type modifyRepo func(*Repo)

// noDash is an option to the x func that marks the repo as hidden on
// the https://build.golang.org/ dashboard.
func noDash(r *Repo) { r.showOnDashboard = false }

func coordinatorCanBuild(r *Repo) { r.CoordinatorCanBuild = true }

func archivedOnGitHub(r *Repo) {
	// When a repository is archived on GitHub, trying to push
	// to it will fail. So don't mirror.
	r.MirrorToGitHub = false
}

func enableCSR(p string) modifyRepo { return func(r *Repo) { r.MirrorToCSRProject = p } }

func importPath(v string) modifyRepo { return func(r *Repo) { r.ImportPath = v } }

func desc(v string) modifyRepo { return func(r *Repo) { r.WebsiteDesc = v } }

// addMirrored adds a repo that's on Gerrit and mirrored to GitHub.
func addMirrored(proj string, opts ...modifyRepo) {
	repo := &Repo{
		GoGerritProject: proj,
		MirrorToGitHub:  true,
		GitHubRepo:      "golang/" + proj,
	}
	for _, o := range opts {
		o(repo)
	}
	add(repo)
}

// x adds a golang.org/x repo.
func x(proj string, opts ...modifyRepo) {
	repo := &Repo{
		GoGerritProject:     proj,
		MirrorToGitHub:      true,
		CoordinatorCanBuild: true,
		ImportPath:          "golang.org/x/" + proj,
		GitHubRepo:          "golang/" + proj,
		showOnDashboard:     true,
	}
	for _, o := range opts {
		o(repo)
	}
	add(repo)
}

func add(r *Repo) {
	if (r.MirrorToCSRProject != "" || r.MirrorToGitHub || r.showOnDashboard) && r.GoGerritProject == "" {
		panic(fmt.Sprintf("project %+v sets feature(s) that require a GoGerritProject, but has none", r))
	}
	if r.MirrorToGitHub && r.GitHubRepo == "" {
		panic(fmt.Sprintf("project %+v has MirrorToGitHub but no gitHubRepo", r))
	}
	if r.showOnDashboard && !r.CoordinatorCanBuild {
		panic(fmt.Sprintf("project %+v is showOnDashboard but not marked buildable by coordinator", r))
	}

	if p := r.GoGerritProject; p != "" {
		if _, dup := ByGerritProject[p]; dup {
			panic(fmt.Sprintf("duplicate Gerrit project %q in %+v", p, r))
		}
		ByGerritProject[p] = r
	}
	if p := r.ImportPath; p != "" {
		if _, dup := ByImportPath[p]; dup {
			panic(fmt.Sprintf("duplicate import path %q in %+v", p, r))
		}
		ByImportPath[p] = r
	}
}

// ShowOnDashboard reports whether this repo should show up on build.golang.org
// in the list of repos at bottom.
//
// When this returns true, r.GoGerritProject is guaranteed to be non-empty.
func (r *Repo) ShowOnDashboard() bool { return r.showOnDashboard }
