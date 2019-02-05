// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The buildstats command syncs build logs from Datastore to Bigquery.
//
// It will eventually also do more stats.
package main // import "golang.org/x/build/cmd/buildstats"

import (
	"context"
	"flag"
	"fmt"
	"log"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/internal/buildstats"
)

var (
	mode    = flag.String("mode", "", "one of 'sync', 'testspeed'")
	verbose = flag.Bool("v", false, "verbose")
)

var env *buildenv.Environment

func main() {
	buildenv.RegisterFlags()
	flag.Parse()
	buildstats.Verbose = *verbose
	if *mode == "" {
		log.Printf("missing required --mode")
		flag.Usage()
	}

	env = buildenv.FromFlags()

	ctx := context.Background()
	switch *mode {
	case "sync":
		if err := buildstats.SyncBuilds(ctx, env); err != nil {
			log.Fatalf("SyncBuilds: %v", err)
		}
		if err := buildstats.SyncSpans(ctx, env); err != nil {
			log.Fatalf("SyncSpans: %v", err)
		}
	case "testspeed":
		ts, err := buildstats.QueryTestStats(ctx, env)
		if err != nil {
			log.Fatalf("QueryTestStats: %v", err)
		}
		for _, builder := range ts.Builders() {
			bs := ts.BuilderTestStats[builder]
			for _, test := range bs.Tests() {
				fmt.Printf("%s\t%s\t%.1f\t%d\n",
					builder,
					test,
					bs.MedianDuration[test].Seconds(),
					bs.Runs[test])
			}
		}
	default:
		log.Fatalf("unknown --mode=%s", *mode)
	}

}
