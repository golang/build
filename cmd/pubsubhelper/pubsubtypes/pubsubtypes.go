// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package pubsubtypes contains types published by pubsubhelper.
package pubsubtypes

type Event struct {
	Gerrit *GerritEvent `json:",omitempty"`
	GitHub *GitHubEvent `json:",omitempty"`
}

type GerritEvent struct {
	URL          string
	CommitHash   string
	ChangeNumber int `json:",omitempty"`
}

type GitHubEvent struct {
	// TODO
}
