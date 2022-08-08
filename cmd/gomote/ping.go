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
		fmt.Fprintln(os.Stderr, "ping usage: gomote v2 ping [--status] <instance>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)

	if fs.NArg() != 1 {
		fs.Usage()
	}
	name := fs.Arg(0)
	ctx := context.Background()
	client := gomoteServerClient(ctx)
	_, err := client.InstanceAlive(ctx, &protos.InstanceAliveRequest{
		GomoteId: name,
	})
	if err != nil {
		return fmt.Errorf("unable to ping instance: %s", statusFromError(err))
	}
	return nil
}
