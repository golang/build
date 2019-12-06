// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package repos contains information about Go source repositories.
package repos

import "fmt"

type Repo struct {
	// GoGerritProject, if non-empty, is its Gerrit project name,
	// such as "go", "net", or "sys".
	GoGerritProject string

	// ImportPath is the repo's import path.
	// It is empty for the main Go repo.
	ImportPath string

	MirroredToGithub bool

	// HideFromDashboard, if true, makes the repo not appear at build.golang.org.
	HideFromDashboard bool

	// CoordinatorCanBuild reports whether this a repo that the
	// build coordinator knows how to build.
	CoordinatorCanBuild bool
}

// ByGerritProject maps from a Gerrit project name ("go", "net", etc)
// to the Repo's information.
var ByGerritProject = map[string]*Repo{ /* initialized below */ }

// ByImportPath maps from an import path ("golang.org/x/net") to the
// Repo's information.
var ByImportPath = map[string]*Repo{ /* initialized below */ }

func init() {
	add(&Repo{GoGerritProject: "go", MirroredToGithub: true, CoordinatorCanBuild: true})
	add(&Repo{GoGerritProject: "dl", MirroredToGithub: true, ImportPath: "golang.org/dl", HideFromDashboard: true, CoordinatorCanBuild: true})
	add(&Repo{GoGerritProject: "protobuf", MirroredToGithub: true, ImportPath: "github.com/google/protobuf", HideFromDashboard: true})
	add(&Repo{GoGerritProject: "gddo", MirroredToGithub: true, ImportPath: "github.com/golang/gddo", HideFromDashboard: true})
	add(&Repo{GoGerritProject: "gofrontend", MirroredToGithub: true, HideFromDashboard: true})
	add(&Repo{GoGerritProject: "gollvm", MirroredToGithub: false, HideFromDashboard: true})
	add(&Repo{GoGerritProject: "grpc-review", MirroredToGithub: false, HideFromDashboard: true})
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
}

type modifyRepo func(*Repo)

// noDash is an option to the x func that marks the repo as hidden on
// the https://build.golang.org/ dashboard.
func noDash(r *Repo) { r.HideFromDashboard = true }

// x adds a golang.org/x repo.
func x(proj string, opts ...modifyRepo) {
	repo := &Repo{
		GoGerritProject:     proj,
		MirroredToGithub:    true,
		CoordinatorCanBuild: true,
		ImportPath:          "golang.org/x/" + proj,
	}
	for _, o := range opts {
		o(repo)
	}
	add(repo)
}

func add(r *Repo) {
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
