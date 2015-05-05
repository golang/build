// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
)

func list(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "list usage: gomote list\n\n")
		fs.PrintDefaults()
		os.Exit(1)
	}
	fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
	}

	prefix := fmt.Sprintf("mote-%s-", username())
	vms, err := buildlet.ListVMs(projTokenSource(), *proj, *zone)
	if err != nil {
		return fmt.Errorf("failed to list VMs: %v", err)
	}
	for _, vm := range vms {
		if !strings.HasPrefix(vm.Name, prefix) {
			continue
		}
		fmt.Printf("%s\thttps://%s\n", strings.TrimPrefix(vm.Name, prefix), strings.TrimSuffix(vm.IPPort, ":443"))
	}
	return nil
}

func namedClient(name string) (*buildlet.Client, error) {
	if strings.Contains(name, ":") {
		return buildlet.NewClient(name, buildlet.NoKeyPair), nil
	}
	// TODO(bradfitz): cache the list on disk and avoid the API call?
	vms, err := buildlet.ListVMs(projTokenSource(), *proj, *zone)
	if err != nil {
		return nil, fmt.Errorf("error listing VMs while looking up %q: %v", name, err)
	}
	wantName := fmt.Sprintf("mote-%s-%s", username(), name)
	var matches []buildlet.VM
	for _, vm := range vms {
		if vm.Name == wantName {
			return buildlet.NewClient(vm.IPPort, vm.TLS), nil
		}
		if strings.HasPrefix(vm.Name, wantName) {
			matches = append(matches, vm)
		}
	}
	if len(matches) == 1 {
		vm := matches[0]
		return buildlet.NewClient(vm.IPPort, vm.TLS), nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("prefix %q is ambiguous", wantName)
	}
	return nil, fmt.Errorf("buildlet %q not running", name)
}

// namedConfig returns the builder configuration that matches the given mote
// name. It matches prefixes to accommodate motes than have "-n" suffixes.
func namedConfig(name string) (dashboard.BuildConfig, bool) {
	match := ""
	for cname := range dashboard.Builders {
		if strings.HasPrefix(name, cname) && len(cname) > len(match) {
			match = cname
		}
	}
	return dashboard.Builders[match], match != ""
}

// nextName returns the next available numbered name or the given buildlet base
// name. For example, if the provided prefix is "linux-amd64" and there's
// already an instance named "linux-amd64", nextName will return
// "linux-amd64-1".
func nextName(prefix string) (string, error) {
	vms, err := buildlet.ListVMs(projTokenSource(), *proj, *zone)
	if err != nil {
		return "", fmt.Errorf("error listing VMs: %v", err)
	}
	matches := map[string]bool{}
	for _, vm := range vms {
		if strings.HasPrefix(vm.Name, prefix) {
			matches[vm.Name] = true
		}
	}
	if len(matches) == 0 || !matches[prefix] {
		return prefix, nil
	}
	for i := 1; ; i++ {
		next := fmt.Sprintf("%v-%v", prefix, i)
		if !matches[next] {
			return next, nil
		}
	}
}
