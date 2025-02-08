// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package groups contains group definitions for ACL purposes.
// This is defined in a separate package to avoid import cycles.
package groups

// NOTE: Before adding a group to this package, make sure that it has been added
// to the CrIA sync list first, and that the group appears in
// https://chrome-infra-auth.appspot.com/auth/groups/.

const (
	ReleaseTeam  = "mdb/golang-release-eng-policy"
	SecurityTeam = "mdb/golang-security-policy"
	ToolsTeam    = "mdb/go-tools-team"
	GolangTeam   = "mdb/golang-team"
)
