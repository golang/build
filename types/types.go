// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package types contains common types used by the Go continuous build
// system.
package types

import "time"

// BuildStatus is the data structure that's marshalled as JSON
// for the https://build.golang.org/?mode=json page.
type BuildStatus struct {
	// Builders is a list of all known builders.
	// The order that builders appear is the same order as the build results for a revision.
	Builders []string `json:"builders"`

	// Revisions are the revisions shown on the front page of build.golang.org,
	// in the same order. It starts with the "go" repo, from recent to old, and then
	// it has 1 each of the subrepos, with only their most recent commit.
	Revisions []BuildRevision `json:"revisions"`
}

// BuildRevision is the status of a commit across all builders.
// It corresponds to a single row of https://build.golang.org/
type BuildRevision struct {
	// Repo is "go" for the main repo, else  "tools", "crypto", "net", etc.
	// These are repos as listed at https://go.googlesource.com/
	Repo string `json:"repo"`

	// Revision is the full git hash of the repo.
	Revision string `json:"revision"`

	// GoRevision is the full git hash of the "go" repo, if Repo is not "go" itself.
	// Otherwise this is empty.
	GoRevision string `json:"goRevision,omitempty"`

	// Date is the commit date of this revision, formatted in RFC3339.
	Date string `json:"date"`

	// Branch is the branch of this commit, e.g. "master" or "dev.ssa".
	Branch string `json:"branch"`

	// GoBranch is the branch of the GoRevision, for subrepos.
	// It is empty for the main repo.
	// Otherwise it's of the form "master", "release-branch.go1.8", etc.
	GoBranch string `json:"goBranch,omitempty"`

	// Author is the author of this commit in standard git form
	// "Name <email>".
	Author string `json:"author"`

	// Desc is the commit message of this commit. It may be
	// truncated.
	Desc string `json:"desc"`

	// Results are the build results for each of the builders in
	// the same length slice BuildStatus.Builders.
	// Each string is either "" (if no data), "ok", or the URL to failure logs.
	Results []string `json:"results"`
}

// SpanRecord is a datastore entity we write only at the end of a span
// (roughly a "step") of the build.
type SpanRecord struct {
	BuildID string
	IsTry   bool // is trybot run
	GoRev   string
	Rev     string // same as GoRev for repo "go"
	Repo    string // "go", "net", etc.
	Builder string // "linux-amd64-foo"
	OS      string // "linux"
	Arch    string // "amd64"

	Event     string
	Error     string // empty for no error
	Detail    string
	StartTime time.Time
	EndTime   time.Time
	Seconds   float64
}

// BuildRecord is the datastore entity we write both at the beginning
// and end of a build. Some fields are not updated until the end.
type BuildRecord struct {
	ID            string
	ProcessID     string
	StartTime     time.Time
	IsTry         bool // is trybot run
	IsSlowBot     bool // is an explicitly requested "slowbot" builder
	GoRev         string
	Rev           string // same as GoRev for repo "go"
	Repo          string // "go", "net", etc.
	Builder       string // "linux-amd64-foo"
	ContainerHost string // "" means GKE; "cos" means Container-Optimized OS
	OS            string // "linux"
	Arch          string // "amd64"

	EndTime    time.Time
	Seconds    float64
	Result     string // empty string, "ok", "fail"
	FailureURL string `datastore:",noindex"` // deprecated; use LogURL
	LogURL     string `datastore:",noindex"`

	// TODO(bradfitz): log which reverse buildlet we got?
	// Buildlet string
}

type ReverseBuilder struct {
	Name         string
	HostType     string
	ConnectedSec float64
	IdleSec      float64 `json:",omitempty"`
	BusySec      float64 `json:",omitempty"`
	Version      string  // buildlet version
	Busy         bool
}

// ReverseHostStatus is part of ReverseBuilderStatus.
type ReverseHostStatus struct {
	HostType  string // dashboard.Hosts key
	Connected int    // number of connected buildlets
	Expect    int    // expected number, from dashboard.Hosts config
	Idle      int
	Busy      int
	Waiters   int // number of builds waiting on a buildlet host of this type

	// Machines are all connected buildlets of this host type,
	// keyed by machine self-reported unique name.
	Machines map[string]*ReverseBuilder
}

// ReverseBuilderStatus is https://farmer.golang.org/status/reverse.json
//
// It is used by monitoring and the Mac VMWare infrastructure to
// adjust the Mac VMs based on deaths and demand.
type ReverseBuilderStatus struct {
	// Machines maps from the connected builder name (anything unique) to its status.
	HostTypes map[string]*ReverseHostStatus
}

func (s *ReverseBuilderStatus) Host(hostType string) *ReverseHostStatus {
	if s.HostTypes == nil {
		s.HostTypes = make(map[string]*ReverseHostStatus)
	}
	hs, ok := s.HostTypes[hostType]
	if ok {
		return hs
	}
	hs = &ReverseHostStatus{HostType: hostType}
	s.HostTypes[hostType] = hs
	return hs
}

// MajorMinor is a major-minor version pair.
type MajorMinor struct {
	Major, Minor int
}

// Less reports whether a is less than b.
func (a MajorMinor) Less(b MajorMinor) bool {
	if a.Major != b.Major {
		return a.Major < b.Major
	}
	return a.Minor < b.Minor
}

// BuildletWaitStatus is the periodic messages we send to "gomote create"
// clients or show on trybot status pages to tell the user who long
// they're expected to wait.
type BuildletWaitStatus struct {
	// Message is a free-form message to send to the user's gomote binary.
	// If present, all other fields are ignored.
	Message string `json:"message"`

	// Ahead are the number of waiters ahead of this buildlet request.
	Ahead int `json:"ahead"`

	// TODO: add number of active builds, and number of builds
	// creating. And for how long. And maybe an estimate of how
	// long those builds typically take? But recognize which are
	// dynamic vs static (reverse) builder types and don't say
	// that "1 is creating" on a reverse buildlet that can't
	// actually "create" any. (It can just wait for one register
	// itself)
}

// ActivePostSubmitBuild is a summary of an active build that the
// coordinator's doing. Each one is rendered on build.golang.org as a
// blue gopher which links to StatusURL to watch the build live.
type ActivePostSubmitBuild struct {
	Builder   string `json:"builder"`            // "linux-amd64"
	Commit    string `json:"commit"`             // hash of commit being tested
	GoCommit  string `json:"goCommit,omitempty"` // hash of Go commit, or empty for the main repo
	StatusURL string `json:"statusURL"`
}
