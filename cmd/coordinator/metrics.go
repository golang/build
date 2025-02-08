// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gliderlabs/ssh"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"golang.org/x/build/internal/coordinator/pool"
)

var (
	kBuilderType        = tag.MustNewKey("go-build/coordinator/keys/builder_type")
	kGomoteSSHSuccess   = tag.MustNewKey("go-build/coordinator/keys/gomote_ssh_success")
	kHostType           = tag.MustNewKey("go-build/coordinator/host_type")
	mGitHubAPIRemaining = stats.Int64("go-build/githubapi/remaining", "remaining GitHub API rate limit", stats.UnitDimensionless)
	mGomoteCreateCount  = stats.Int64("go-build/coordinator/gomote_create_count", "counter for gomote create invocations", stats.UnitDimensionless)
	mGomoteRDPCount     = stats.Int64("go-build/coordinator/gomote_rdp_count", "counter for gomote RDP invocations", stats.UnitDimensionless)
	mGomoteSSHCount     = stats.Int64("go-build/coordinator/gomote_ssh_count", "counter for gomote SSH invocations", stats.UnitDimensionless)
	mReverseBuildlets   = stats.Int64("go-build/coordinator/reverse_buildlets_count", "number of reverse buildlets", stats.UnitDimensionless)
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
	{
		Name:        "go-build/githubapi/remaining",
		Description: "Remaining GitHub API rate limit",
		Measure:     mGitHubAPIRemaining,
		Aggregation: view.LastValue(),
	},
	{
		Name:        "go-build/coordinator/gomote_create_count",
		Description: "Count of gomote create invocations",
		Measure:     mGomoteCreateCount,
		TagKeys:     []tag.Key{kBuilderType},
		Aggregation: view.Count(),
	},
	{
		Name:        "go-build/coordinator/gomote_ssh_count",
		Description: "Count of gomote SSH invocations",
		Measure:     mGomoteSSHCount,
		TagKeys:     []tag.Key{kGomoteSSHSuccess},
		Aggregation: view.Count(),
	},
	{
		Name:        "go-build/coordinator/gomote_rdp_count",
		Description: "Count of gomote RDP ivocations",
		Measure:     mGomoteRDPCount,
		Aggregation: view.Count(),
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

// recordBuildletCreate records information about gomote creates and sends them
// to the configured metrics backend.
func recordBuildletCreate(ctx context.Context, builderType string) {
	stats.RecordWithTags(ctx,
		[]tag.Mutator{
			tag.Upsert(kBuilderType, builderType),
		},
		mGomoteCreateCount.M(1))
}

// recordSSHPublicKeyAuthHandler returns a handler which wraps and ssh public key handler and
// records information about gomote SSH usage and sends them to the configured metrics backend.
func recordSSHPublicKeyAuthHandler(fn ssh.PublicKeyHandler) ssh.PublicKeyHandler {
	return func(ctx ssh.Context, key ssh.PublicKey) bool {
		success := fn(ctx, key)
		stats.RecordWithTags(ctx,
			[]tag.Mutator{
				tag.Upsert(kGomoteSSHSuccess, fmt.Sprintf("%t", success)),
			},
			mGomoteSSHCount.M(1))
		return success
	}
}

// recordGomoteRDPUsage records the use of the gomote RDP functionality and sends it
// to the configured metrics backend.
func recordGomoteRDPUsage(ctx context.Context) {
	stats.Record(ctx, mGomoteRDPCount.M(1))
}
