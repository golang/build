// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.23

package buildstats

import (
	"context"
	"fmt"

	"golang.org/x/build/buildenv"
)

// SyncBuilds syncs the datastore "Build" entities to the BigQuery "Builds" table.
// This stores information on each build as a whole, without details.
func SyncBuilds(ctx context.Context, env *buildenv.Environment) error {
	return fmt.Errorf("buildstats: SyncBuilds is not implemented for Go 1.23 onwards at this time")
}

// SyncSpans syncs the datastore "Span" entities to the BigQuery "Spans" table.
// These contain the fine-grained timing details of how a build ran.
func SyncSpans(ctx context.Context, env *buildenv.Environment) error {
	return fmt.Errorf("buildstats: SyncSpans is not implemented for Go 1.23 onwards at this time")
}

// QueryTestStats returns stats on all tests for all builders.
func QueryTestStats(ctx context.Context, env *buildenv.Environment) (*TestStats, error) {
	return nil, fmt.Errorf("buildstats: QueryTestStats is not implemented for Go 1.23 onwards at this time")
}
