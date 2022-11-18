// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/build/internal/gomote/protos"
	"golang.org/x/sync/errgroup"
)

// legacyGetTar a .tar.gz
func legacyGetTar(args []string) error {
	if activeGroup != nil {
		return fmt.Errorf("command does not support groups")
	}

	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "gettar usage: gomote gettar [get-opts] <buildlet-name>")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var dir string
	fs.StringVar(&dir, "dir", "", "relative directory from buildlet's work dir to tar up")

	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}

	name := fs.Arg(0)
	bc, err := remoteClient(name)
	if err != nil {
		return err
	}
	tgz, err := bc.GetTar(context.Background(), dir)
	if err != nil {
		return err
	}
	defer tgz.Close()
	_, err = io.Copy(os.Stdout, tgz)
	return err
}

// getTar a .tar.gz
func getTar(args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "gettar usage: gomote gettar [get-opts] [buildlet-name]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Writes tarball into the current working directory.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Buildlet name is optional if a group is selected, in which case")
		fmt.Fprintln(os.Stderr, "tarballs from all buildlets in the group are downloaded into the")
		fmt.Fprintln(os.Stderr, "current working directory.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var dir string
	fs.StringVar(&dir, "dir", "", "relative directory from buildlet's work dir to tar up")

	fs.Parse(args)

	var getSet []string
	if fs.NArg() == 1 {
		getSet = []string{fs.Arg(0)}
	} else if fs.NArg() == 0 && activeGroup != nil {
		for _, inst := range activeGroup.Instances {
			getSet = append(getSet, inst)
		}
	} else {
		fs.Usage()
	}

	eg, ctx := errgroup.WithContext(context.Background())
	for _, inst := range getSet {
		inst := inst
		eg.Go(func() error {
			f, err := os.Create(fmt.Sprintf("%s.tar.gz", inst))
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to create file to write instance tarball: %v", err)
				return nil
			}
			defer f.Close()
			fmt.Fprintf(os.Stderr, "# Downloading tarball for %q to %q...\n", inst, f.Name())
			return doGetTar(ctx, inst, dir, f)
		})
	}
	return eg.Wait()
}

func doGetTar(ctx context.Context, name, dir string, out io.Writer) error {
	client := gomoteServerClient(ctx)
	resp, err := client.ReadTGZToURL(ctx, &protos.ReadTGZToURLRequest{
		GomoteId:  name,
		Directory: dir,
	})
	if err != nil {
		return fmt.Errorf("unable to retrieve tgz URL: %w", err)
	}
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resp.GetUrl(), nil)
	if err != nil {
		return fmt.Errorf("unable to create HTTP Request: %w", err)
	}
	r, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("unable to download tgz: %w", err)
	}
	defer r.Body.Close()
	_, err = io.Copy(out, r.Body)
	if err != nil {
		return fmt.Errorf("unable to copy tgz to stdout: %w", err)
	}
	return nil
}
