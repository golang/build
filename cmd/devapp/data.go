// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"golang.org/x/build/maintner"
)

var (
	excludedProjects = map[string]bool{
		"gocloud":              true,
		"google-api-go-client": true,
	}
	deletedChanges = map[struct {
		proj string
		num  int32
	}]bool{
		{"crypto", 35958}:  true,
		{"scratch", 71730}: true,
		{"scratch", 71850}: true,
		{"scratch", 72090}: true,
		{"scratch", 72091}: true,
		{"scratch", 72110}: true,
		{"scratch", 72131}: true,
		{"tools", 93515}:   true,
	}
)

func filterProjects(fn func(*maintner.GerritProject) error) func(*maintner.GerritProject) error {
	return func(p *maintner.GerritProject) error {
		if excludedProjects[p.Project()] {
			return nil
		}
		return fn(p)
	}
}

func withoutDeletedCLs(p *maintner.GerritProject, fn func(*maintner.GerritCL) error) func(*maintner.GerritCL) error {
	return func(cl *maintner.GerritCL) error {
		if deletedChanges[struct {
			proj string
			num  int32
		}{p.Project(), cl.Number}] {
			return nil
		}
		return fn(cl)
	}
}
