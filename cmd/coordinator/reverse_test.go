// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	buildletmain "golang.org/x/build/cmd/buildlet"
)

func TestReverseDial(t *testing.T) {
	*mode = "dev"
	http.HandleFunc("/reverse", handleReverse)

	ln, err := net.Listen("tcp", "")
	if err != nil {
		t.Fatalf(`net.Listen(":"): %v`, err)
	}
	t.Logf("listening on %s...", ln.Addr())
	go serveTLS(ln)

	wantModes := "goos-goarch-test1,goos-goarch-test2"
	flag.CommandLine.Set("coordinator", ln.Addr().String())
	flag.CommandLine.Set("reverse", wantModes)

	ch := make(chan reverseBuildlet)
	registerBuildlet = func(b reverseBuildlet) { ch <- b }
	go buildletmain.TestDialCoordinator()

	select {
	case b := <-ch:
		gotModes := strings.Join(b.modes, ",")
		if gotModes != wantModes {
			t.Errorf("want buildlet registered with modes %q, got %q", wantModes, gotModes)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for buildlet registration")
	}
}
