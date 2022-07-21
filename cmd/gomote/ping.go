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

func legacyPing(args []string) error {
	if activeGroup != nil {
		return fmt.Errorf("command does not support groups")
	}

	fs := flag.NewFlagSet("ping", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "ping usage: gomote ping [--status] <instance>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var status bool
	fs.BoolVar(&status, "status", false, "print buildlet status")
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
	}
	name := fs.Arg(0)
	bc, err := remoteClient(name)
	if err != nil {
		return err
	}
	ctx := context.Background()
	wd, err := bc.WorkDir(ctx)
	if err != nil {
		return err
	}
	if status {
		fmt.Printf("workdir: %v\n", wd)
	}
	return nil
}

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
		return fmt.Errorf("unable to ping instance: %s", statusFromError(err))
	}
	return nil
}
