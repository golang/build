// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Same requirements as internal/coordinator/pool/reverse.go.
//go:build linux || darwin

package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	"golang.org/x/build"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/revdial/v2"
)

// coordinatorServer creates a server and listener for the coordinator side of
// revdial. They should be closed when done.
func coordinatorServer() (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/reverse", pool.HandleReverse)
	mux.Handle("/revdial", revdial.ConnHandler())

	ln, err := net.Listen("tcp", "")
	if err != nil {
		return nil, nil, fmt.Errorf(`net.Listen(":"): %v`, err)
	}

	cert, err := tls.X509KeyPair([]byte(build.DevCoordinatorCA), []byte(build.DevCoordinatorKey))
	if err != nil {
		return nil, nil, fmt.Errorf("error creating TLS cert: %v", err)
	}

	ln = tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{cert},
	})

	addr := ln.Addr().String()
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return srv, ln, nil
}

// testReverseDial verifies that a revdial connection can be established and
// registered in the coordinator reverse pool at coordAddr.
func testReverseDial(t *testing.T, coordAddr, hostType string) {
	t.Helper()

	oldCoordinator := *coordinator
	defer func() {
		*coordinator = oldCoordinator
	}()
	*coordinator = coordAddr

	// N.B. We don't need to set *hostname to anything in particular as it
	// is only advisory in the coordinator. It is not used to connect back
	// to reverse buildlets.

	oldReverseType := *reverseType
	defer func() {
		*reverseType = oldReverseType
	}()
	*reverseType = hostType

	ln, err := dialCoordinator()
	if err != nil {
		t.Fatalf("dialCoordinator got err %v want nil", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", handleStatus)
	srv := &http.Server{
		Handler: mux,
	}
	c := make(chan error, 1)
	go func() {
		c <- srv.Serve(ln)
	}()
	defer func() {
		srv.Close()
		err := <-c
		if err != http.ErrServerClosed {
			t.Errorf("Server shutdown got err %v want ErrServerClosed", err)
		}
	}()

	// Verify that we eventually get the "buildlet" registered with the pool.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	start := time.Now()
	for range tick.C {
		if time.Since(start) > 1*time.Second {
			t.Fatalf("Buildlet failed to register within 1s.")
		}

		types := pool.ReversePool().HostTypes()
		if slices.Contains(types, hostType) {
			// Success!
			return
		}
	}
}

// TestReverseDial verifies that a revdial connection can be established and
// registered in the coordinator reverse pool.
func TestReverseDial(t *testing.T) {
	pool.SetBuilderMasterKey([]byte(devMasterKey))

	srv, ln, err := coordinatorServer()
	if err != nil {
		t.Fatalf("serveCoordinator got err %v want nil", err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	const hostType = "test-reverse-dial"
	testReverseDial(t, srv.Addr, hostType)
}

// TestReverseDialRedirect verifies that a revdial connection works with a 307
// redirect to the endpoints. The coordinator will do this in dev mode.
func TestReverseDialRedirect(t *testing.T) {
	pool.SetBuilderMasterKey([]byte(devMasterKey))

	srv, ln, err := coordinatorServer()
	if err != nil {
		t.Fatalf("serveCoordinator got err %v want nil", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/redirected/reverse", pool.HandleReverse)
	mux.Handle("/redirected/revdial", revdial.ConnHandler())

	redirect := func(w http.ResponseWriter, r *http.Request) {
		u := *r.URL
		u.Path = "/redirected/" + u.Path
		http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
	}
	mux.HandleFunc("/reverse", redirect)
	mux.HandleFunc("/revdial", redirect)
	srv.Handler = mux

	go srv.Serve(ln)
	defer srv.Close()

	const hostType = "test-reverse-dial-redirect"
	testReverseDial(t, srv.Addr, hostType)
}
