// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
)

func list(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "list usage: gomote list")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
	}

	cc, err := buildlet.NewCoordinatorClientFromFlags()
	if err != nil {
		log.Fatal(err)
	}
	rbs, err := cc.RemoteBuildlets()
	if err != nil {
		log.Fatal(err)
	}
	for _, rb := range rbs {
		fmt.Printf("%s\t%s\t%s\texpires in %v\n", rb.Name, rb.BuilderType, rb.HostType, rb.Expires.Sub(time.Now()))
	}

	return nil
}

func clientAndConf(name string) (bc *buildlet.Client, conf dashboard.BuildConfig, err error) {
	cc, err := buildlet.NewCoordinatorClientFromFlags()
	if err != nil {
		return
	}

	rbs, err := cc.RemoteBuildlets()
	if err != nil {
		return
	}
	var ok bool
	for _, rb := range rbs {
		if rb.Name == name {
			conf, ok = dashboard.Builders[rb.BuilderType]
			if !ok {
				err = fmt.Errorf("builder %q exists, but unknown builder type %q", name, rb.BuilderType)
				return
			}
			break
		}
	}
	if !ok {
		err = fmt.Errorf("unknown builder %q", name)
		return
	}

	bc, err = namedClient(name)
	return
}

func namedClient(name string) (*buildlet.Client, error) {
	if strings.Contains(name, ":") {
		return buildlet.NewClient(name, buildlet.NoKeyPair), nil
	}
	cc, err := buildlet.NewCoordinatorClientFromFlags()
	if err != nil {
		return nil, err
	}
	return cc.NamedBuildlet(name)
}
