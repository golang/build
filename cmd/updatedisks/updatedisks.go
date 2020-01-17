// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The updatedisks command creates & deletes VM disks as needed
// across the various GCP zones.
//
// This code is run automatically in the background in the coordinator
// and is purely an optimization. It's only a separate tool for debugging
// purposes.
package main

import (
	"context"
	"flag"
	"log"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/internal/buildgo"
	compute "google.golang.org/api/compute/v1"
)

var (
	computeSvc *compute.Service
	env        *buildenv.Environment
)

func main() {
	buildenv.RegisterFlags()
	flag.Parse()

	env = buildenv.FromFlags()

	ctx := context.Background()

	c, err := buildgo.NewClient(ctx, env)
	if err != nil {
		log.Fatal(err)
	}

	if err := c.MakeBasepinDisks(ctx); err != nil {
		log.Fatal(err)
	}
}
