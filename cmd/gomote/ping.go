// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"golang.org/x/build/internal/gomote/protos"
)

func ping(args []string) error {
	fs := flag.NewFlagSet("ping", flag.ContinueOnError)
	fs.Usage = func() {
		log := usageLogger
		log.Print("ping usage: gomote ping [instance]")
		log.Print("")
		log.Print("Instance name is optional if a group is specified.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)

	var pingSet []string
	if fs.NArg() == 1 {
		pingSet = []string{fs.Arg(0)}
	} else if fs.NArg() == 0 && activeGroup != nil {
		for _, inst := range activeGroup.Instances {
			pingSet = append(pingSet, inst)
		}
	} else {
		fs.Usage()
	}

	ctx := context.Background()
	for _, inst := range pingSet {
		if err := doPing(ctx, inst); err != nil {
			log.Printf("%s: %v\n", inst, err)
		} else {
			log.Printf("%s: alive\n", inst)
		}
	}
	return nil
}

func doPing(ctx context.Context, name string) error {
	client := gomoteServerClient(ctx)
	_, err := client.InstanceAlive(ctx, &protos.InstanceAliveRequest{
		GomoteId: name,
	})
	if err != nil {
		return fmt.Errorf("unable to ping instance: %w", err)
	}
	return nil
}
