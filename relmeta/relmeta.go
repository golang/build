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
	ID             int64             `yaml:"id"`
	Track          GoSecurityTrack   `yaml:"track"`
	Toolchain      bool              `yaml:"is_toolchain"`
	Package        string            `yaml:"package"`
	Changelists    []string          `yaml:"changelists"`
	ReleaseNote    string            `yaml:"release_note"`
	TargetReleases []string          `yaml:"target_releases,omitempty"` // required for std/cmd; omit for x-repo
	DeploymentMap  map[string]string `yaml:"deployment_map,omitempty"`
	GitHubIssueID  int64             `yaml:"github_issue_id"`
	VulnReportID   string            `yaml:"vuln_report_id"`   // for example, GO-20YY-NNNN
	VulnReportDesc string            `yaml:"vuln_report_desc"` // optional
	Credits        []string          `yaml:"credits"`
	CVE            string            `yaml:"cve"`
	CWE            string            `yaml:"cwe"`
}

type GoSecurityTrack string

const (
	Public  GoSecurityTrack = "PUBLIC"
	Private GoSecurityTrack = "PRIVATE"
	Urgent  GoSecurityTrack = "URGENT"
)
