// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package buildstats contains code to sync the coordinator's build
// logs from Datastore to BigQuery.
package buildstats

import (
	"sort"
	"time"
)

// Verbose controls logging verbosity.
var Verbose = false

// TestStats describes stats for a cmd/dist test on a particular build
// configuration (a "builder").
type TestStats struct {
	// AsOf is the time that the stats were queried from BigQuery.
	AsOf time.Time

	// BuilderTestStats maps from a builder name to that builder's
	// test stats.
	BuilderTestStats map[string]*BuilderTestStats
}

// Duration returns the median time to run testName on builder, if known.
// Otherwise it returns some non-zero default value.
func (ts *TestStats) Duration(builder, testName string) time.Duration {
	if ts != nil {
		if bs, ok := ts.BuilderTestStats[builder]; ok {
			if d, ok := bs.MedianDuration[testName]; ok {
				return d
			}
		}
	}
	return 3 * time.Second // some arbitrary value if unknown
}

func (ts *TestStats) Builders() []string {
	s := make([]string, 0, len(ts.BuilderTestStats))
	for k := range ts.BuilderTestStats {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

type BuilderTestStats struct {
	// Builder is which build configuration this is for.
	Builder string

	// Runs is how many times tests have run recently, for some
	// fuzzy definition of "recently".
	// The map key is a cmd/dist test name.
	Runs map[string]int

	// MedianDuration is the median duration for a test to
	// pass on this BuilderTestStat's Builder.
	// The map key is a cmd/dist test name.
	MedianDuration map[string]time.Duration
}

func (ts *BuilderTestStats) Tests() []string {
	s := make([]string, 0, len(ts.Runs))
	for k := range ts.Runs {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}
