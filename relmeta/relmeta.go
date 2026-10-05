// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package relmeta aims to improve the
// coordination and correctness of Go
// Security Releases.
//
// Any API described within this package
// is meant for internal use only; it is
// not subject to the Go 1 compatibility
// promise and may change at any time.
package relmeta

// ReleaseMilestone describes all of the
// self-contained patches which are part
// of a given Go Security Release.
type ReleaseMilestone struct {
	ID      int64            `yaml:"id"`
	Patches []*SecurityPatch `yaml:"security_patches"`
}

// SecurityPatch is a self-contained body
// of work that addresses a vulnerability.
type SecurityPatch struct {
	ID             int64           `yaml:"id"`
	Track          GoSecurityTrack `yaml:"track"`
	Toolchain      bool            `yaml:"is_toolchain"`
	Package        string          `yaml:"package"`
	Symbols        []string        `yaml:"symbols"`
	Changelists    []string        `yaml:"changelists"`
	ReleaseNote    string          `yaml:"release_note"`
	TargetReleases []string        `yaml:"target_releases,omitempty"` // required for std/cmd; omit for x-repo
	GitHubIssueID  int64           `yaml:"github_issue_id"`
	VulnReportID   string          `yaml:"vuln_report_id"`   // for example, GO-20YY-NNNN
	VulnReportDesc string          `yaml:"vuln_report_desc"` // optional
	Credits        []string        `yaml:"credits"`
	CVE            string          `yaml:"cve"`
	CWE            string          `yaml:"cwe"`

	// DeploymentMap acts as an escape hatch
	// for complex CLs that would otherwise
	// require a large amount of complexity
	// to release in the normal workflow.
	//
	// Each key is a canonical internal CL
	// URL and it must appear in the Changelists
	// field as well. The values are a compound
	// "<project>:<branch>". For example,
	// "go:internal-release-branch.go1.27.2"
	// or "net:public".
	//
	// When non-empty, relui trusts the map
	// implicitly: It rebases and submits
	// each CL on its listed branch, creates
	// no cherry-picks for the patch, and
	// fails fast on any error.
	//
	// DeploymentMap should really only be used
	// for complicated x/net vendoring and http2
	// bundling patches.
	DeploymentMap map[string]string `yaml:"deployment_map,omitempty"`
}

type GoSecurityTrack string

const (
	Public  GoSecurityTrack = "PUBLIC"
	Private GoSecurityTrack = "PRIVATE"
	Urgent  GoSecurityTrack = "URGENT"
)
