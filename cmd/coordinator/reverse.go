// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

/*
This file implements reverse buildlets. These are buildlets that are not
started by the coordinator. They dial the coordinator and then accept
instructions. This feature is used for machines that cannot be started by
an API, for example real OS X machines with iOS and Android devices attached.

You can test this setup locally. In one terminal start a coordinator.
It will default to dev mode, using a dummy TLS cert and not talking to GCE.

	$ coordinator

In another terminal, start a reverse buildlet:

	$ buildlet -reverse "darwin-amd64"

It will dial and register itself with the coordinator. To confirm the
coordinator can see the buildlet, check the logs output or visit its
diagnostics page: https://localhost:8119. To send the buildlet some
work, go to:

	https://localhost:8119/dosomework
*/

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
)

const minBuildletVersion = 1

var reversePool = &reverseBuildletPool{
	buildletReturned: make(chan token, 1),
}

func init() {
	go func() {
		for {
			time.Sleep(30 * time.Second)
			reversePool.reverseHealthCheck()
		}
	}()
}

type token struct{}

type reverseBuildletPool struct {
	buildletReturned chan token // best-effort tickle when any buildlet becomes free

	mu        sync.Mutex // guards buildlets and their fields
	buildlets []*reverseBuildlet
}

var errInUse = errors.New("all buildlets are in use")

func (p *reverseBuildletPool) tryToGrab(machineType string) (*buildlet.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	usableCount := 0
	for _, b := range p.buildlets {
		usable := false
		for _, m := range b.modes {
			if m == machineType {
				usable = true
				usableCount++
				break
			}
		}
		if usable && b.inUseAs == "" {
			// Found an unused match.
			b.inUseAs = machineType
			b.client.SetCloseFunc(func() error {
				p.mu.Lock()
				b.inUseAs = ""
				p.mu.Unlock()
				select {
				case p.buildletReturned <- token{}:
				default:
				}
				return nil
			})
			return b.client, nil
		}
	}
	if usableCount == 0 {
		return nil, fmt.Errorf("no buildlets registered for machine type %q", machineType)
	}
	return nil, errInUse
}

// reverseHealthCheck requests the status page of each idle buildlet.
// If the buildlet fails to respond promptly, it is removed from the pool.
func (p *reverseBuildletPool) reverseHealthCheck() {
	p.mu.Lock()
	responses := make(map[*reverseBuildlet]chan error)
	for _, b := range p.buildlets {
		if b.inUseAs == "health" { // sanity check
			panic("previous health check still running")
		}
		if b.inUseAs != "" {
			continue // skip busy buildlets
		}
		b.inUseAs = "health"
		res := make(chan error, 1)
		responses[b] = res
		client := b.client
		go func() {
			_, err := client.Status()
			res <- err
		}()
	}
	p.mu.Unlock()
	time.Sleep(5 * time.Second) // give buildlets time to respond
	p.mu.Lock()

	var buildlets []*reverseBuildlet
	for _, b := range p.buildlets {
		res := responses[b]
		if b.inUseAs != "health" || res == nil {
			// buildlet skipped or registered after health check
			buildlets = append(buildlets, b)
			continue
		}
		b.inUseAs = ""
		err, done := <-res
		if !done {
			err = errors.New("health check timeout")
		}
		if err == nil {
			buildlets = append(buildlets, b)
			continue
		}
		// remove bad buildlet
		log.Printf("Reverse buildlet %s %v not responding, removing from pool", b.client, b.modes)
		b.client.Close()
	}
	p.buildlets = buildlets
	p.mu.Unlock()
}

func (p *reverseBuildletPool) GetBuildlet(machineType, rev string, el eventTimeLogger) (*buildlet.Client, error) {
	for {
		b, err := p.tryToGrab(machineType)
		if err == errInUse {
			select {
			case <-p.buildletReturned:
			// As multiple goroutines can be listening for the
			// buildletReturned signal, it must be treated as
			// a best effort signal. So periodically try to grab
			// a buildlet again.
			case <-time.After(30 * time.Second):
			}
		} else if err != nil {
			return nil, err
		} else {
			return b, nil
		}
	}
}

func (p *reverseBuildletPool) WriteHTMLStatus(w io.Writer) {
	inUse := make(map[string]int)
	total := make(map[string]int)

	p.mu.Lock()
	for _, b := range p.buildlets {
		for _, mode := range b.modes {
			if b.inUseAs != "" && b.inUseAs != "health" {
				inUse[mode]++
			}
			total[mode]++
		}
	}
	p.mu.Unlock()

	var modes []string
	for mode := range total {
		modes = append(modes, mode)
	}
	sort.Strings(modes)

	io.WriteString(w, "<b>Reverse pool</b><ul>")
	if len(modes) == 0 {
		io.WriteString(w, "<li>no connections</li>")
	}
	for _, mode := range modes {
		fmt.Fprintf(w, "<li>%s: %d/%d</li>", mode, inUse[mode], total[mode])
	}
	io.WriteString(w, "</ul>")
}

func (p *reverseBuildletPool) String() string {
	p.mu.Lock()
	inUse := 0
	for _, b := range p.buildlets {
		if b.inUseAs != "" && b.inUseAs != "health" {
			inUse++
		}
	}
	p.mu.Unlock()

	return fmt.Sprintf("Reverse pool capacity: %d/%d %s", inUse, len(p.buildlets), p.Modes())
}

// Modes returns the a deduplicated list of buildlet modes curently supported
// by the pool. Buildlet modes are described on reverseBuildlet comments.
func (p *reverseBuildletPool) Modes() (modes []string) {
	mm := make(map[string]bool)
	p.mu.Lock()
	for _, b := range p.buildlets {
		for _, mode := range b.modes {
			mm[mode] = true
		}
	}
	p.mu.Unlock()

	for mode := range mm {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	return modes
}

// CanBuild reports whether the pool has a machine capable of building mode.
// The machine may be in use, so you may have to wait.
func (p *reverseBuildletPool) CanBuild(mode string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.buildlets {
		for _, m := range b.modes {
			if m == mode {
				return true
			}
		}
	}
	return false
}

// reverseBuildlet is a registered reverse buildlet.
// Its immediate fields are guarded by the reverseBuildletPool mutex.
type reverseBuildlet struct {
	client *buildlet.Client

	// modes is the set of valid modes for this buildlet.
	//
	// A mode is the equivalent of a builder name, for example
	// "darwin-amd64", "android-arm", or "linux-amd64-race".
	//
	// Each buildlet may potentially have several modes. For example a
	// Mac OS X machine with an attached iOS device may be registered
	// as both "darwin-amd64", "darwin-arm64".
	modes []string

	// inUseAs signifies that the buildlet is in use as the named mode.
	// guarded by mutex on reverseBuildletPool.
	inUseAs string
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
	b := &reverseBuildlet{
		modes:  modes,
		client: client,
	}
	reversePool.buildlets = append(reversePool.buildlets, b)
	registerBuildlet(modes)
}

var registerBuildlet = func(modes []string) {} // test hook

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
	// Serialize trips. It is up to callers to avoid deadlocking.
	c.sema <- true
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
