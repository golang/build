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
	"golang.org/x/sync/errgroup"
)

func legacyRm(args []string) error {
	if activeGroup != nil {
		return fmt.Errorf("command does not support groups")
	}

	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "rm usage: gomote rm <instance> <file-or-dir>+")
		fmt.Fprintln(os.Stderr, "          gomote rm <instance> .  (to delete everything)")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)

	if fs.NArg() < 2 {
		fs.Usage()
	}
	name := fs.Arg(0)
	args = fs.Args()[1:]
	bc, err := remoteClient(name)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return bc.RemoveAll(ctx, args...)
}

func rm(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "rm usage: gomote rm [instance] <file-or-dir>+")
		fmt.Fprintln(os.Stderr, "          gomote rm [instance] .  (to delete everything)")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Instance name is optional if a group is specified.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)

	ctx := context.Background()
	var rmSet []string
	var paths []string
	if err := doPing(ctx, fs.Arg(0)); instanceDoesNotExist(err) {
		// When there's no active group, this is just an error.
		if activeGroup == nil {
			return fmt.Errorf("instance %q: %s", fs.Arg(0), statusFromError(err))
		}
		// When there is an active group, this just means that we're going
		// to use the group instead and assume the rest is a command.
		for _, inst := range activeGroup.Instances {
			rmSet = append(rmSet, inst)
		}
		if fs.NArg() == 0 {
			fmt.Fprintln(os.Stderr, "error: not enough arguments")
			fs.Usage()
		}
		paths = fs.Args()
	} else if err == nil {
		rmSet = append(rmSet, fs.Arg(0))
		if fs.NArg() == 1 {
			fmt.Fprintln(os.Stderr, "error: not enough arguments")
			fs.Usage()
		}
		paths = fs.Args()[1:]
	} else {
		return fmt.Errorf("checking instance %q: %v", fs.Arg(0), err)
	}

	eg, ctx := errgroup.WithContext(context.Background())
	for _, inst := range rmSet {
		inst := inst
		eg.Go(func() error {
			return doRm(ctx, inst, paths)
		})
	}
	return eg.Wait()
}

func doRm(ctx context.Context, inst string, paths []string) error {
	client := gomoteServerClient(ctx)
	if _, err := client.RemoveFiles(ctx, &protos.RemoveFilesRequest{
		GomoteId: inst,
		Paths:    paths,
	}); err != nil {
		return fmt.Errorf("unable to remove files: %s", statusFromError(err))
	}
	return nil
}
