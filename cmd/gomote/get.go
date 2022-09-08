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
)

// legacyGetTar a .tar.gz
func legacyGetTar(args []string) error {
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
	ctx := context.Background()
	client := gomoteServerClient(ctx)
	resp, err := client.ReadTGZToURL(ctx, &protos.ReadTGZToURLRequest{
		GomoteId:  name,
		Directory: dir,
	})
	if err != nil {
		return fmt.Errorf("unable to retrieve tgz URL: %s", statusFromError(err))
	}
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resp.GetUrl(), nil)
	if err != nil {
		return fmt.Errorf("unable to create HTTP Request: %s", err)
	}
	r, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("unable to download tgz: %s", err)
	}
	defer r.Body.Close()
	_, err = io.Copy(os.Stdout, r.Body)
	if err != nil {
		return fmt.Errorf("unable to copy tgz to stdout: %s", err)
	}
	return nil
}
