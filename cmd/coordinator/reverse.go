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
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
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
			time.Sleep(15 * time.Second)
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
			b.inUseTime = time.Now()
			b.client.SetCloseFunc(func() error {
				p.mu.Lock()
				b.inUseAs = ""
				b.inUseTime = time.Now()
				p.mu.Unlock()
				p.noteBuildletReturned()
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

func (p *reverseBuildletPool) noteBuildletReturned() {
	select {
	case p.buildletReturned <- token{}:
	default:
	}
}

// nukeBuildlet wipes out victim as a buildlet we'll ever return again,
// and closes its TCP connection in hopes that it will fix itself
// later.
func (p *reverseBuildletPool) nukeBuildlet(victim *buildlet.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, rb := range p.buildlets {
		if rb.client == victim {
			defer rb.conn.Close()
			p.buildlets = append(p.buildlets[:i], p.buildlets[i+1:]...)
			return
		}
	}
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
		b.inUseTime = time.Now()
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
		b.inUseTime = time.Now()
		p.noteBuildletReturned()
		var err error
		select {
		case err = <-res:
		default:
			// It had 5 seconds above to send to the
			// buffered channel. So if we're here, it took
			// over 5 seconds.
			err = errors.New("health check timeout")
		}
		if err == nil {
			buildlets = append(buildlets, b)
			continue
		}
		// remove bad buildlet
		log.Printf("Reverse buildlet %s %v not responding, removing from pool", b.client, b.modes)
		go b.client.Close()
		go b.conn.Close()
	}
	p.buildlets = buildlets
	p.mu.Unlock()
}

var (
	highPriorityBuildletMu sync.Mutex
	highPriorityBuildlet   = make(map[string]chan *buildlet.Client)
)

func highPriChan(typ string) chan *buildlet.Client {
	highPriorityBuildletMu.Lock()
	defer highPriorityBuildletMu.Unlock()
	if c, ok := highPriorityBuildlet[typ]; ok {
		return c
	}
	c := make(chan *buildlet.Client)
	highPriorityBuildlet[typ] = c
	return c
}

func (p *reverseBuildletPool) GetBuildlet(cancel Cancel, machineType, rev string, el eventTimeLogger) (*buildlet.Client, error) {
	seenErrInUse := false
	for {
		b, err := p.tryToGrab(machineType)
		if err == errInUse {
			if !seenErrInUse {
				el.logEventTime("waiting_machine_in_use")
				seenErrInUse = true
			}
			var highPri chan *buildlet.Client
			if rev == "release" || rev == "adg" || rev == "bradfitz" {
				highPri = highPriChan(machineType)
				log.Printf("Rev %q is waiting high-priority", rev)
			}
			select {
			case bc := <-highPri:
				log.Printf("Rev %q stole a high-priority one.", rev)
				return p.cleanedBuildlet(bc, el)
			case <-p.buildletReturned:
			// As multiple goroutines can be listening for the
			// buildletReturned signal, it must be treated as
			// a best effort signal. So periodically try to grab
			// a buildlet again.
			case <-time.After(30 * time.Second):
			case <-cancel:
				return nil, ErrCanceled
			}
		} else if err != nil {
			return nil, err
		} else {
			select {
			case highPriChan(machineType) <- b:
				// Somebody else was more important.
			default:
				return p.cleanedBuildlet(b, el)
			}
		}
	}
}

func (p *reverseBuildletPool) cleanedBuildlet(b *buildlet.Client, el eventTimeLogger) (*buildlet.Client, error) {
	el.logEventTime("got_machine")
	// Clean up any files from previous builds.
	if err := b.RemoveAll("."); err != nil {
		b.Close()
		return nil, err
	}
	el.logEventTime("cleaned_up")
	return b, nil
}

func (p *reverseBuildletPool) WriteHTMLStatus(w io.Writer) {
	// total maps from a builder type to the number of machines which are
	// capable of that role.
	total := make(map[string]int)
	// inUse and inUseOther track the number of machines using machines.
	// inUse is how many machines are building that type, and inUseOther counts
	// how many machines are occupied doing a similar role on that hardware.
	// e.g. "darwin-amd64-10_10" occupied as a "darwin-arm-a5ios",
	// or "linux-arm" as a "linux-arm-arm5" count as inUseOther.
	inUse := make(map[string]int)
	inUseOther := make(map[string]int)

	var machineBuf bytes.Buffer
	p.mu.Lock()
	for _, b := range p.buildlets {
		machStatus := "<i>idle</i>"
		if b.inUseAs != "" {
			machStatus = "working as <b>" + b.inUseAs + "</b>"
		}
		fmt.Fprintf(&machineBuf, "<li>%s, %s: %s for %v</li>\n",
			b.conn.RemoteAddr(), strings.Join(b.modes, ", "), machStatus, time.Since(b.inUseTime))
		for _, mode := range b.modes {
			if b.inUseAs != "" && b.inUseAs != "health" {
				if mode == b.inUseAs {
					inUse[mode]++
				} else {
					inUseOther[mode]++
				}
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

	io.WriteString(w, "<b>Reverse pool summary</b><ul>")
	if len(modes) == 0 {
		io.WriteString(w, "<li>no connections</li>")
	}
	for _, mode := range modes {
		use, other := inUse[mode], inUseOther[mode]
		if use+other == 0 {
			fmt.Fprintf(w, "<li>%s: 0/%d</li>", mode, total[mode])
		} else {
			fmt.Fprintf(w, "<li>%s: %d/%d (%d + %d other)</li>", mode, use+other, total[mode], use, other)
		}
	}
	io.WriteString(w, "</ul>")

	fmt.Fprintf(w, "<b>Reverse pool machine detail</b><ul>%s</ul>", machineBuf.Bytes())
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
	conn   net.Conn

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
	// inUseTime is when it entered that state.
	// Both are guarded by the mutex on reverseBuildletPool.
	inUseAs   string
	inUseTime time.Time
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
	log.Printf("Registering reverse buildlet %s for modes %v", r.RemoteAddr, modes)

	// The server becomes a (very simple) http client.
	(&http.Response{StatusCode: 200, Proto: "HTTP/1.1"}).Write(conn)

	client := buildlet.NewClient("none", buildlet.NoKeyPair)
	client.SetHTTPClient(&http.Client{
		Transport: newRoundTripper(client, conn, bufrw),
	})
	client.SetDescription(fmt.Sprintf("reverse peer %s for modes %v", r.RemoteAddr, modes))
	tstatus := time.Now()
	status, err := client.Status()
	if err != nil {
		log.Printf("Reverse connection %s for modes %v did not answer status after %v: %v", r.RemoteAddr, modes, time.Since(tstatus), err)
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
		modes:     modes,
		client:    client,
		conn:      conn,
		inUseTime: time.Now(),
	}
	reversePool.buildlets = append(reversePool.buildlets, b)
	registerBuildlet(modes)
}

var registerBuildlet = func(modes []string) {} // test hook

func newRoundTripper(bc *buildlet.Client, conn net.Conn, bufrw *bufio.ReadWriter) *reverseRoundTripper {
	return &reverseRoundTripper{
		bc:    bc,
		conn:  conn,
		bufrw: bufrw,
		sema:  make(chan bool, 1),
	}
}

// reverseRoundTripper is an http client that serializes all requests
// over a *bufio.ReadWriter.
//
// Attempts at concurrent requests return an error.
type reverseRoundTripper struct {
	bc    *buildlet.Client
	conn  net.Conn
	bufrw *bufio.ReadWriter
	sema  chan bool
}

func (c *reverseRoundTripper) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	// Serialize trips. It is up to callers to avoid deadlocking.
	c.sema <- true
	if err := req.Write(c.bufrw); err != nil {
		go c.conn.Close()
		<-c.sema
		return nil, err
	}
	if err := c.bufrw.Flush(); err != nil {
		go c.conn.Close()
		<-c.sema
		return nil, err
	}
	resp, err = http.ReadResponse(c.bufrw.Reader, req)
	if err != nil {
		go c.conn.Close()
		<-c.sema
		return nil, err
	}
	resp.Body = &reverseLockedBody{c, resp.Body, c.sema}
	return resp, err
}

type reverseLockedBody struct {
	rt   *reverseRoundTripper
	body io.ReadCloser
	sema chan bool
}

func (b *reverseLockedBody) Read(p []byte) (n int, err error) {
	n, err = b.body.Read(p)
	if err != nil && err != io.EOF {
		go b.rt.conn.Close()
	}
	return
}

func (b *reverseLockedBody) Close() error {
	// Set a timer to hard-nuke the connection in case b.body.Close hangs,
	// as seen in Issue 11869.
	t := time.AfterFunc(5*time.Second, func() {
		reversePool.nukeBuildlet(b.rt.bc)
		go b.rt.conn.Close() // redundant if nukeBuildlet did it, but harmless.
	})
	err := b.body.Close()
	t.Stop()
	<-b.sema
	b.body = nil // prevent double close
	return err
}
