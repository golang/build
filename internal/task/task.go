// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package task implements tasks involved in making a Go release.
package task

import (
	"golang.org/x/build/internal/secret"
)

// ExternalConfig holds configuration and credentials for external
// services that tasks need to interact with as part of their work.
type ExternalConfig struct {
	// DryRun is whether the dry-run mode is on.
	//
	// In dry-run mode, tasks are expected to report
	// what would be done, without changing anything.
	DryRun bool

	// TwitterAPI holds Twitter API credentials that
	// can be used to post a tweet.
	TwitterAPI secret.TwitterCredentials
}
