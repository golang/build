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
	info   buildlet.Info
	client *buildlet.Client
}

func handleReverse(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "buildlet registration requires SSL", http.StatusInternalServerError)
		return
	}
	// TODO(crawshaw): check key

	conn, bufrw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Registering reverse buildlet %s", r.RemoteAddr)

	// The server becomes a (very simple) http client.
	(&http.Response{Status: "200 OK", Proto: "HTTP/1.1"}).Write(conn)

	client := buildlet.NewClient("none", buildlet.NoKeyPair)
	client.SetHTTPClient(&http.Client{
		Transport: newRoundTripper(bufrw),
	})
	info, err := client.Info()
	if err != nil {
		log.Printf("Reverse connection did not answer /info: %v", err)
		conn.Close()
		return
	}
	if info.Version < minBuildletVersion {
		log.Printf("Buildlet too old: %s, %+v", r.RemoteAddr, info)
		conn.Close()
		return
	}
	log.Printf("Buildlet %s: %+v", r.RemoteAddr, info)
	// TODO(crawshaw): register buildlet with pool, pass conn
	// TODO(crawshaw): add connection test
	select {}
}

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
