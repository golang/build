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

	// showOnDashboard is whether to show the repo on the bottom
	// of build.golang.org in the repo overview section.
	showOnDashboard bool

	// CoordinatorCanBuild reports whether this a repo that the
	// build coordinator knows how to build.
	CoordinatorCanBuild bool

	// gitHubRepo is the "org/repo" of where this repo exists on
	// GitHub. If MirrorToGitHub is true, this is the
	// destination.
	gitHubRepo string

	// WebsiteDesc is the description of the repo for showing on
	// https://golang.org/pkg/#subrepo.
	// It should be plain text. Hostnames may be auto-linkified.
	WebsiteDesc string

	// usePkgGoDev controls whether the repo has opted-in to use
	// pkg.go.dev for displaying documentation for Go packages.
	usePkgGoDev bool
}

// ByGerritProject maps from a Gerrit project name ("go", "net", etc)
// to the Repo's information.
var ByGerritProject = map[string]*Repo{ /* initialized below */ }

// ByImportPath maps from an import path ("golang.org/x/net") to the
// Repo's information.
var ByImportPath = map[string]*Repo{ /* initialized below */ }

func init() {
	addMirrored("go", coordinatorCanBuild, noDash)
	addMirrored("dl", importPath("golang.org/dl"), coordinatorCanBuild)
	addMirrored("gddo", importPath("github.com/golang/gddo"))
	addMirrored("gofrontend")
	addMirrored("proposal")
	addMirrored("sublime-build")
	addMirrored("sublime-config")

	x("arch")
	x("benchmarks", desc("benchmarks to measure Go as it is developed"))
	x("blog", desc("blog.golang.org's implementation"))
	x("build", desc("build.golang.org's implementation"))
	x("crypto", desc("additional cryptography packages"))
	x("debug", desc("an experimental debugger for Go"))
	x("example", noDash)
	x("exp", desc("experimental and deprecated packages (handle with care; may change without warning)"))
	x("image", desc("additional imaging packages"))
	x("lint", noDash)
	x("mobile", desc("experimental support for Go on mobile platforms"))
	x("mod")
	x("net", desc("additional networking packages"))
	x("oauth2")
	x("perf", desc("packages and tools for performance measurement, storage, and analysis"))
	x("pkgsite", desc("home of the pkg.go.dev website"), usePkgGoDev(), noBuildAndNoDash)
	x("playground", noDash)
	x("review", desc("a tool for working with Gerrit code reviews"))
	x("scratch", noDash)
	x("sync", desc("additional concurrency primitives"))
	x("sys", desc("packages for making system calls"))
	x("talks")
	x("term")
	x("text", desc("packages for working with text"))
	x("time", desc("additional time packages"))
	x("tools", desc("godoc, goimports, gorename, and other tools"))
	x("tour", noDash, desc("tour.golang.org's implementation"))
	x("vgo", noDash)
	x("website")
	x("xerrors", noDash)

	add(&Repo{GoGerritProject: "gollvm"})
	add(&Repo{GoGerritProject: "grpc-review"})

	add(&Repo{
		GoGerritProject: "protobuf",
		MirrorToGitHub:  true,
		ImportPath:      "google.golang.org/protobuf",
		gitHubRepo:      "protocolbuffers/protobuf-go",
	})

	add(&Repo{
		GoGerritProject: "vscode-go",
		MirrorToGitHub:  true,
		gitHubRepo:      "golang/vscode-go",
	})
}

type modifyRepo func(*Repo)

// noDash is an option to the x func that marks the repo as hidden on
// the https://build.golang.org/ dashboard.
func noDash(r *Repo) { r.showOnDashboard = false }

func noBuildAndNoDash(r *Repo) { r.CoordinatorCanBuild, r.showOnDashboard = false, false }

func coordinatorCanBuild(r *Repo) { r.CoordinatorCanBuild = true }

func importPath(v string) modifyRepo { return func(r *Repo) { r.ImportPath = v } }

func desc(v string) modifyRepo { return func(r *Repo) { r.WebsiteDesc = v } }
func usePkgGoDev() modifyRepo  { return func(r *Repo) { r.usePkgGoDev = true } }

// addMirrored adds a repo that's on Gerrit and mirrored to GitHub.
func addMirrored(proj string, opts ...modifyRepo) {
	repo := &Repo{
		GoGerritProject: proj,
		MirrorToGitHub:  true,
		gitHubRepo:      "golang/" + proj,
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
		gitHubRepo:          "golang/" + proj,
		showOnDashboard:     true,
	}
	for _, o := range opts {
		o(repo)
	}
	add(repo)
}

func add(r *Repo) {
	if r.MirrorToGitHub {
		if r.gitHubRepo == "" {
			panic(fmt.Sprintf("project %+v has MirrorToGitHub but no gitHubRepo", r))
		}
		if r.GoGerritProject == "" {
			panic(fmt.Sprintf("project %+v has MirrorToGitHub but no GoGerritProject", r))
		}
	}
	if r.showOnDashboard {
		if !r.CoordinatorCanBuild {
			panic(fmt.Sprintf("project %+v is showOnDashboard but not marked buildable by coordinator", r))
		}
		if r.GoGerritProject == "" {
			panic(fmt.Sprintf("project %+v is showOnDashboard but has no Gerrit project", r))
		}
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

// GitHubRepo returns the "<org>/<repo>" that this repo either lives
// at or is mirrored to. It returns the empty string if this repo has no
// GitHub presence.
func (r *Repo) GitHubRepo() string { return r.gitHubRepo }

// UsePkgGoDev reports whether the repo has opted-in to use
// pkg.go.dev for displaying documentation for Go packages.
func (r *Repo) UsePkgGoDev() bool { return r.usePkgGoDev }
