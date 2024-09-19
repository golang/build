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
	"strings"

	"golang.org/x/build/internal/gomote/protos"
)

func ls(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.Usage = func() {
		log := usageLogger
		log.Print("ls usage: gomote ls [ls-opts] [instance] [dir]")
		log.Print("")
		log.Print("Instance name is optional if a group is specified.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var recursive bool
	fs.BoolVar(&recursive, "R", false, "recursive")
	var digest bool
	fs.BoolVar(&digest, "d", false, "get file digests")
	var skip string
	fs.StringVar(&skip, "skip", "", "comma-separated list of relative directories to skip (use forward slashes)")
	fs.Parse(args)

	ctx := context.Background()
	dir := "."
	var lsSet []string
	switch fs.NArg() {
	case 0:
		// With no arguments, we need an active group to do anything useful.
		if activeGroup == nil {
			log.Print("error: no group specified")
			fs.Usage()
		}
		for _, inst := range activeGroup.Instances {
			lsSet = append(lsSet, inst)
		}
	case 1:
		// Ambiguous case. Check if it's a real instance, if not, treat it
		// as a directory.
		if err := doPing(ctx, fs.Arg(0)); instanceDoesNotExist(err) {
			// Not an instance.
			for _, inst := range activeGroup.Instances {
				lsSet = append(lsSet, inst)
			}
			dir = fs.Arg(0)
		} else if err == nil {
			// It's an instance.
			lsSet = []string{fs.Arg(0)}
		} else {
			return fmt.Errorf("failed to ping %q: %w", fs.Arg(0), err)
		}
	case 2:
		// Instance and directory is specified.
		lsSet = []string{fs.Arg(0)}
		dir = fs.Arg(1)
	default:
		log.Print("error: too many arguments")
		fs.Usage()
	}
	for _, inst := range lsSet {
		client := gomoteServerClient(ctx)
		resp, err := client.ListDirectory(ctx, &protos.ListDirectoryRequest{
			GomoteId:  inst,
			Directory: dir,
			Recursive: recursive,
			SkipFiles: strings.Split(skip, ","),
			Digest:    digest,
		})
		if err != nil {
			return fmt.Errorf("unable to ls: %w", err)
		}
		if len(lsSet) > 1 {
			fmt.Fprintf(os.Stdout, "# %s\n", inst)
		}
		for _, entry := range resp.GetEntries() {
			fmt.Fprintf(os.Stdout, "%s\n", entry)
		}
		if len(lsSet) > 1 {
			fmt.Fprintln(os.Stdout)
		}
	}
	return nil
}
