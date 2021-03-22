// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.13 && (linux || darwin)
// +build go1.13
// +build linux darwin

package main

import (
	"context"
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"golang.org/x/build/internal/coordinator/pool"
)

var (
	kHostType         = tag.MustNewKey("go-build/coordinator/host_type")
	mReverseBuildlets = stats.Int64("go-build/coordinator/reverse_buildlets_count", "number of reverse buildlets", stats.UnitDimensionless)
)

// views should contain all measurements. All *view.View added to this
// slice will be registered and exported to the metric service.
var views = []*view.View{
	{
		Name:        "go-build/coordinator/reverse_buildlets_count",
		Description: "Number of reverse buildlets that are up",
		Measure:     mReverseBuildlets,
		TagKeys:     []tag.Key{kHostType},
		Aggregation: view.LastValue(),
	},
}

// reportReverseCountMetrics gathers and reports
// a count of running reverse buildlets per type.
func reportReverseCountMetrics() {
	for {
		// 1. Gather # buildlets up per reverse builder type.
		totals := pool.ReversePool().HostTypeCount()
		// 2. Write counts out to the metrics recorder, grouped by hostType.
		for hostType, n := range totals {
			stats.RecordWithTags(context.Background(),
				[]tag.Mutator{tag.Upsert(kHostType, hostType)},
				mReverseBuildlets.M(int64(n)))
		}

		time.Sleep(5 * time.Minute)
	}
}
