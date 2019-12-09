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
	// It is empty for the main Go repo.
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
	x("benchmarks")
	x("blog")
	x("build")
	x("crypto")
	x("debug")
	x("example", noDash)
	x("exp")
	x("image")
	x("lint", noDash)
	x("mobile")
	x("mod")
	x("net")
	x("oauth2")
	x("perf")
	x("playground", noDash)
	x("review")
	x("scratch", noDash)
	x("sync")
	x("sys")
	x("talks")
	x("term")
	x("text")
	x("time")
	x("tools")
	x("tour", noDash)
	x("vgo", noDash)
	x("website")
	x("xerrors", noDash)

	add(&Repo{GoGerritProject: "gollvm"})
	add(&Repo{GoGerritProject: "grpc-review"})

	add(&Repo{
		GoGerritProject: "protobuf",
		MirrorToGitHub:  true,
		ImportPath:      "github.com/google/protobuf",
		gitHubRepo:      "protocolbuffers/protobuf-go",
	})
}

type modifyRepo func(*Repo)

// noDash is an option to the x func that marks the repo as hidden on
// the https://build.golang.org/ dashboard.
func noDash(r *Repo) { r.showOnDashboard = false }

func coordinatorCanBuild(r *Repo) { r.CoordinatorCanBuild = true }

func importPath(v string) modifyRepo { return func(r *Repo) { r.ImportPath = v } }

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
