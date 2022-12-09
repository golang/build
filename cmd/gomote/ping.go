// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"golang.org/x/build/internal/gomote/protos"
)

func ping(args []string) error {
	fs := flag.NewFlagSet("ping", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "ping usage: gomote ping [instance]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Instance name is optional if a group is specified.")
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
			fmt.Fprintf(os.Stderr, "%s: %v\n", inst, err)
		} else {
			fmt.Fprintf(os.Stderr, "%s: alive\n", inst)
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
