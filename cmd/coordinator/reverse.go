// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"golang.org/x/build/buildlet"
)

const minBuildletVersion = 1

var reversePool = &reverseBuildletPool{}

type reverseBuildletPool struct {
	mu        sync.Mutex
	buildlets []reverseBuildlet
}

type reverseBuildlet struct {
	modes  []string
	client *buildlet.Client
}

func handleReverse(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "buildlet registration requires SSL", http.StatusInternalServerError)
		return
	}
	// Check build keys.
	modes := r.Header["X-Go-Builder-Type"]
	gobuildkeys := r.Header["X-Go-Builder-Key"]
	if len(modes) == 0 || len(modes) != len(gobuildkeys) {
		http.Error(w, fmt.Sprintf("need at least one mode and matching key, got %d/%d", len(modes), len(gobuildkeys)), http.StatusPreconditionFailed)
		return
	}
	for i, m := range modes {
		if gobuildkeys[i] != builderKey(m) {
			http.Error(w, fmt.Sprintf("bad key for mode %q", m), http.StatusPreconditionFailed)
			return
		}
	}

	conn, bufrw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Registering reverse buildlet %s", r.RemoteAddr)

	// The server becomes a (very simple) http client.
	(&http.Response{StatusCode: 200, Proto: "HTTP/1.1"}).Write(conn)

	client := buildlet.NewClient("none", buildlet.NoKeyPair)
	client.SetHTTPClient(&http.Client{
		Transport: newRoundTripper(bufrw),
	})
	status, err := client.Status()
	if err != nil {
		log.Printf("Reverse connection did not answer status: %v", err)
		conn.Close()
		return
	}
	if status.Version < minBuildletVersion {
		log.Printf("Buildlet too old: %s, %+v", r.RemoteAddr, status)
		conn.Close()
		return
	}
	log.Printf("Buildlet %s: %+v for %s", r.RemoteAddr, status, modes)

	// TODO(crawshaw): unregister buildlet when it disconnects. Maybe just
	// periodically request Status, and if there's no response unregister.
	reversePool.mu.Lock()
	defer reversePool.mu.Unlock()
	b := reverseBuildlet{
		modes:  modes,
		client: client,
	}
	reversePool.buildlets = append(reversePool.buildlets, b)
	registerBuildlet(b)
}

var registerBuildlet = func(b reverseBuildlet) {} // test hook

func newRoundTripper(bufrw *bufio.ReadWriter) *reverseRoundTripper {
	return &reverseRoundTripper{
		bufrw: bufrw,
		sema:  make(chan bool, 1),
	}
}

// reverseRoundTripper is an http client that serializes all requests
// over a *bufio.ReadWriter.
//
// Attempts at concurrent requests return an error.
type reverseRoundTripper struct {
	bufrw *bufio.ReadWriter
	sema  chan bool
}

func (c *reverseRoundTripper) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	select {
	case c.sema <- true:
	default:
		return nil, fmt.Errorf("reverseRoundTripper: line busy")
	}
	if err := req.Write(c.bufrw); err != nil {
		return nil, err
	}
	if err := c.bufrw.Flush(); err != nil {
		return nil, err
	}
	resp, err = http.ReadResponse(c.bufrw.Reader, req)
	if err != nil {
		return nil, err
	}
	resp.Body = &reverseLockedBody{resp.Body, c.sema}
	return resp, err
}

type reverseLockedBody struct {
	body io.ReadCloser
	sema chan bool
}

func (b *reverseLockedBody) Read(p []byte) (n int, err error) {
	return b.body.Read(p)
}

func (b *reverseLockedBody) Close() error {
	err := b.body.Close()
	<-b.sema
	b.body = nil // prevent double close
	return err
}
