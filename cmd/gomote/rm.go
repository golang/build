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

func legacyRm(args []string) error {
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
	ctx := context.Background()
	client := gomoteServerClient(ctx)
	if _, err := client.RemoveFiles(ctx, &protos.RemoveFilesRequest{
		GomoteId: name,
		Paths:    args,
	}); err != nil {
		return fmt.Errorf("unable to remove files: %s", statusFromError(err))
	}
	return nil
}
