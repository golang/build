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
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/gomote/protos"
)

func legacyList(args []string) error {
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

// remoteClient returns a buildlet.Client for a named remote buildlet
// (a buildlet connection owned by the build coordinator).
//
// As a special case, if name contains '@', the name is expected to be
// of the form <build-config-name>@ip[:port]. For example,
// "windows-amd64-race@10.0.0.1".
func remoteClient(name string) (buildlet.RemoteClient, error) {
	bc, _, err := clientAndCondConf(name, false)
	return bc, err
}

// clientAndConf returns a buildlet.Client and its build config for
// a named remote buildlet (a buildlet connection owned by the build
// coordinator).
//
// As a special case, if name contains '@', the name is expected to be
// of the form <build-config-name>@ip[:port]. For example,
// "windows-amd64-race@10.0.0.1".
func clientAndConf(name string) (bc buildlet.RemoteClient, conf *dashboard.BuildConfig, err error) {
	return clientAndCondConf(name, true)
}

func clientAndCondConf(name string, withConf bool) (bc buildlet.RemoteClient, conf *dashboard.BuildConfig, err error) {
	if strings.Contains(name, "@") {
		f := strings.SplitN(name, "@", 2)
		if len(f) != 2 {
			err = fmt.Errorf("unsupported name %q; for @ form expect <build-config-name>@host[:port]", name)
			return
		}
		builderType := f[0]
		if withConf {
			var ok bool
			conf, ok = dashboard.Builders[builderType]
			if !ok {
				err = fmt.Errorf("unknown builder type %q (name %q)", builderType, name)
				return
			}
		}
		ipPort := f[1]
		if !strings.Contains(ipPort, ":") {
			ipPort += ":80"
		}
		bc = buildlet.NewClient(ipPort, buildlet.NoKeyPair)
		return
	}

	cc, err := buildlet.NewCoordinatorClientFromFlags()
	if err != nil {
		return
	}

	rbs, err := cc.RemoteBuildlets()
	if err != nil {
		return
	}
	var builderType string
	var ok bool
	for _, rb := range rbs {
		if rb.Name == name {
			ok = true
			builderType = rb.BuilderType
		}
	}
	if !ok {
		err = fmt.Errorf("unknown builder %q", name)
		return
	}

	bc, err = cc.NamedBuildlet(name)
	if err != nil {
		return
	}

	conf, ok = dashboard.Builders[builderType]
	if !ok {
		log.Fatalf("Builder type %q not known to this gomote binary. Update your gomote binary. TODO: teach gomote to fetch build configs from the server (Issue 30929)", builderType)
	}

	return bc, conf, nil
}

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
	groups, err := loadAllGroups()
	if err != nil {
		return fmt.Errorf("loading groups: %v", err)
	}
	ctx := context.Background()
	client := gomoteServerClient(ctx)
	resp, err := client.ListInstances(ctx, &protos.ListInstancesRequest{})
	if err != nil {
		return fmt.Errorf("unable to list instance: %s", statusFromError(err))
	}
	for _, inst := range resp.GetInstances() {
		var groupList strings.Builder
		for _, g := range groups {
			if !g.has(inst.GetGomoteId()) {
				continue
			}
			if groupList.Len() == 0 {
				groupList.WriteString(" (")
			} else {
				groupList.WriteString(", ")
			}
			groupList.WriteString(g.Name)
		}
		if groupList.Len() != 0 {
			groupList.WriteString(")")
		}
		fmt.Printf("%s%s\t%s\t%s\texpires in %v\n", inst.GetGomoteId(), groupList.String(), inst.GetBuilderType(), inst.GetHostType(), time.Unix(inst.GetExpires(), 0).Sub(time.Now()))
	}
	return nil
}
