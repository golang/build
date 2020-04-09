// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package pool

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
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/revdial/v2"
	"golang.org/x/build/types"
)

const minBuildletVersion = 1

var (
	reversePool = &ReverseBuildletPool{
		oldInUse:     make(map[*buildlet.Client]bool),
		hostLastGood: make(map[string]time.Time),
	}

	builderMasterKey []byte
)

const maxOldRevdialUsers = 10

// SetBuilderMasterKey sets the builder master key used
// to generate keys used by the builders.
func SetBuilderMasterKey(masterKey []byte) {
	builderMasterKey = masterKey
}

// ReversePool retrieves the reverse buildlet pool.
func ReversePool() *ReverseBuildletPool {
	return reversePool
}

// ReverseBuildletPool manages the pool of reverse buildlet pools.
type ReverseBuildletPool struct {
	// mu guards all 5 fields below and also fields of
	// *reverseBuildlet in buildlets
	mu sync.Mutex

	// buildlets are the currently connected buildlets.
	// TODO: switch to a map[hostType][]buildlets or map of set.
	buildlets []*reverseBuildlet

	wakeChan map[string]chan token // hostType => best-effort wake-up chan when buildlet free

	waiters map[string]int // hostType => number waiters blocked in GetBuildlet

	// oldInUse tracks which buildlets with the old revdial code are currently in use.
	// These are a liability due to runaway memory issues (Issue 31639) so
	// we bound how many can be running at once. Fortunately there aren't many left.
	oldInUse map[*buildlet.Client]bool

	// hostLastGood tracks when buildlets were last seen to be
	// healthy. It's only used by the health reporting code (in
	// status.go). The reason it's a map on ReverseBuildletPool
	// rather than a field on each reverseBuildlet is because we
	// also want to track the last known health time of buildlets
	// that aren't currently connected.
	//
	// Each buildlet's health is recorded in the map twice, under
	// two different keys: 1) its reported host name, and 2) its
	// hostType + ":" + its reported host name. It's recorded both
	// ways so the status code can check for both globally-unique
	// hostnames that change host types (e.g. our Macs), as well
	// as hostnames that aren't globally unique and are expected
	// to be found with different hostTypes (e.g. our ppc64le
	// machines as both POWER8 and POWER9 host types, but with the
	// same names).
	hostLastGood map[string]time.Time
}

// BuildletLastSeen gives the last time a buildlet was connected to the pool. If
// the buildlet has not been seen a false is returned by the boolean.
func (p *ReverseBuildletPool) BuildletLastSeen(host string) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	t, ok := p.hostLastGood[host]
	return t, ok
}

// ServeReverseStatusJSON is an HTTP handler implementation which serves the status in
// JSON format.
func (p *ReverseBuildletPool) ServeReverseStatusJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := p.BuildReverseStatusJSON()
	j, _ := json.MarshalIndent(status, "", "\t")
	w.Write(j)
}

// BuildReverseStatusJSON is an HTTP handler implementation which builds the reverse
// status reverse buildlets.
func (p *ReverseBuildletPool) BuildReverseStatusJSON() *types.ReverseBuilderStatus {
	status := &types.ReverseBuilderStatus{}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.buildlets {
		hs := status.Host(b.hostType)
		if hs.Machines == nil {
			hs.Machines = make(map[string]*types.ReverseBuilder)
		}
		hs.Connected++
		bs := &types.ReverseBuilder{
			Name:         b.hostname,
			HostType:     b.hostType,
			ConnectedSec: time.Since(b.regTime).Seconds(),
			Version:      b.version,
		}
		if b.inUse && !b.inHealthCheck {
			hs.Busy++
			bs.Busy = true
			bs.BusySec = time.Since(b.inUseTime).Seconds()
		} else {
			hs.Idle++
			bs.IdleSec = time.Since(b.inUseTime).Seconds()
		}

		hs.Machines[b.hostname] = bs
	}
	for hostType, waiters := range p.waiters {
		status.Host(hostType).Waiters = waiters
	}
	for hostType, hc := range dashboard.Hosts {
		if hc.ExpectNum > 0 {
			status.Host(hostType).Expect = hc.ExpectNum
		}
	}
	return status
}

// tryToGrab returns non-nil bc on success if a buildlet is free.
//
// Otherwise it returns how many were busy, which might be 0 if none
// were (yet?) registered. The busy valid is only valid if bc == nil.
func (p *ReverseBuildletPool) tryToGrab(hostType string) (bc *buildlet.Client, busy int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.buildlets {
		if b.hostType != hostType {
			continue
		}
		if b.inUse {
			busy++
			continue
		}
		if b.isOldRevDial && len(p.oldInUse) >= maxOldRevdialUsers {
			continue
		}
		// Found an unused match.
		b.inUse = true
		b.inUseTime = time.Now()
		if b.isOldRevDial {
			p.oldInUse[b.client] = true
		}
		return b.client, 0
	}
	return nil, busy
}

func (p *ReverseBuildletPool) getWakeChan(hostType string) chan token {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.wakeChan == nil {
		p.wakeChan = make(map[string]chan token)
	}
	c, ok := p.wakeChan[hostType]
	if !ok {
		c = make(chan token)
		p.wakeChan[hostType] = c
	}
	return c
}

func (p *ReverseBuildletPool) noteBuildletAvailable(hostType string) {
	wake := p.getWakeChan(hostType)
	select {
	case wake <- token{}:
	default:
	}
}

// nukeBuildlet wipes out victim as a buildlet we'll ever return again,
// and closes its TCP connection in hopes that it will fix itself
// later.
func (p *ReverseBuildletPool) nukeBuildlet(victim *buildlet.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.oldInUse, victim)
	for i, rb := range p.buildlets {
		if rb.client == victim {
			defer rb.conn.Close()
			p.buildlets = append(p.buildlets[:i], p.buildlets[i+1:]...)
			return
		}
	}
}

// healthCheckBuildletLoop periodically requests the status from b.
// If the buildlet fails to respond promptly, it is removed from the pool.
func (p *ReverseBuildletPool) healthCheckBuildletLoop(b *reverseBuildlet) {
	for {
		time.Sleep(time.Duration(10+rand.Intn(5)) * time.Second)
		if !p.healthCheckBuildlet(b) {
			return
		}
	}
}

// recordHealthy updates the two map entries in hostLastGood recording
// that b is healthy.
func (p *ReverseBuildletPool) recordHealthy(b *reverseBuildlet) {
	t := time.Now()
	p.hostLastGood[b.hostname] = t
	p.hostLastGood[b.hostType+":"+b.hostname] = t
}

func (p *ReverseBuildletPool) healthCheckBuildlet(b *reverseBuildlet) bool {
	if b.client.IsBroken() {
		return false
	}
	p.mu.Lock()
	if b.inHealthCheck { // sanity check
		panic("previous health check still running")
	}
	if b.inUse {
		p.recordHealthy(b)
		p.mu.Unlock()
		return true // skip busy buildlets
	}
	b.inUse = true
	b.inHealthCheck = true
	b.inUseTime = time.Now()
	res := make(chan error, 1)
	go func() {
		_, err := b.client.Status(context.Background())
		res <- err
	}()
	p.mu.Unlock()

	t := time.NewTimer(5 * time.Second) // give buildlets time to respond
	var err error
	select {
	case err = <-res:
		t.Stop()
	case <-t.C:
		err = errors.New("health check timeout")
	}

	if err != nil {
		// remove bad buildlet
		log.Printf("Health check fail; removing reverse buildlet %v (type %v): %v", b.hostname, b.hostType, err)
		go b.client.Close()
		go p.nukeBuildlet(b.client)
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !b.inHealthCheck {
		// buildlet was grabbed while lock was released; harmless.
		return true
	}
	b.inUse = false
	b.inHealthCheck = false
	b.inUseTime = time.Now()
	p.recordHealthy(b)
	go p.noteBuildletAvailable(b.hostType)
	return true
}

func (p *ReverseBuildletPool) updateWaiterCounter(hostType string, delta int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiters == nil {
		p.waiters = make(map[string]int)
	}
	p.waiters[hostType] += delta
}

// GetBuildlet builds a buildlet client for the passed in host.
func (p *ReverseBuildletPool) GetBuildlet(ctx context.Context, hostType string, lg Logger) (*buildlet.Client, error) {
	p.updateWaiterCounter(hostType, 1)
	defer p.updateWaiterCounter(hostType, -1)
	seenErrInUse := false

	sp := lg.CreateSpan("wait_static_builder", hostType)
	for {
		bc, busy := p.tryToGrab(hostType)
		if bc != nil {
			sp.Done(nil)
			return p.cleanedBuildlet(bc, lg)
		}
		if busy > 0 && !seenErrInUse {
			lg.LogEventTime("waiting_machine_in_use")
			seenErrInUse = true
		}
		select {
		case <-ctx.Done():
			return nil, sp.Done(ctx.Err())
		case <-time.After(10 * time.Second):
			// As multiple goroutines can be listening for
			// the available signal, it must be treated as
			// a best effort signal. So periodically try
			// to grab a buildlet again.
		case <-p.getWakeChan(hostType):
		}
	}
}

func (p *ReverseBuildletPool) cleanedBuildlet(b *buildlet.Client, lg Logger) (*buildlet.Client, error) {
	// Clean up any files from previous builds.
	sp := lg.CreateSpan("clean_buildlet", b.String())
	err := b.RemoveAll(context.Background(), ".")
	sp.Done(err)
	if err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// WriteHTMLStatus writes a status of the reverse buildlet pool, in HTML format,
//  to the passed in io.Writer.
func (p *ReverseBuildletPool) WriteHTMLStatus(w io.Writer) {
	// total maps from a host type to the number of machines which are
	// capable of that role.
	total := make(map[string]int)
	for typ, host := range dashboard.Hosts {
		if host.ExpectNum > 0 {
			total[typ] = 0
		}
	}
	// inUse track the number of non-idle host types.
	inUse := make(map[string]int)

	var buf bytes.Buffer
	p.mu.Lock()
	buildlets := append([]*reverseBuildlet(nil), p.buildlets...)
	sort.Sort(byTypeThenHostname(buildlets))
	numInUse := 0
	for _, b := range buildlets {
		machStatus := "<i>idle</i>"
		if b.inUse {
			machStatus = "working"
			numInUse++
		}
		fmt.Fprintf(&buf, "<li>%s (%s) version %s, %s: connected %s, %s for %s</li>\n",
			b.hostname,
			b.conn.RemoteAddr(),
			b.version,
			b.hostType,
			friendlyDuration(time.Since(b.regTime)),
			machStatus,
			friendlyDuration(time.Since(b.inUseTime)))
		total[b.hostType]++
		if b.inUse && !b.inHealthCheck {

			inUse[b.hostType]++
		}
	}
	numOldInUse := len(p.oldInUse)
	numConnected := len(buildlets)
	p.mu.Unlock()

	var typs []string
	for typ := range total {
		typs = append(typs, typ)
	}
	sort.Strings(typs)

	io.WriteString(w, "<b>Reverse pool stats</b><ul>\n")
	fmt.Fprintf(w, "<li>Buildlets connected: %d</li>\n", numConnected)
	fmt.Fprintf(w, "<li>Buildlets in use: %d</li>\n", numInUse)
	fmt.Fprintf(w, "<li>Old revdial buildlets in use: %d</li>\n", numOldInUse)
	io.WriteString(w, "</ul>")

	io.WriteString(w, "<b>Reverse pool by host type</b> (in use / total)<ul>\n")
	if len(typs) == 0 {
		io.WriteString(w, "<li>no connections</li>\n")
	}
	for _, typ := range typs {
		if dashboard.Hosts[typ] != nil && total[typ] < dashboard.Hosts[typ].ExpectNum {
			fmt.Fprintf(w, "<li>%s: %d/%d (%d missing)</li>\n",
				typ, inUse[typ], total[typ], dashboard.Hosts[typ].ExpectNum-total[typ])
		} else {
			fmt.Fprintf(w, "<li>%s: %d/%d</li>\n", typ, inUse[typ], total[typ])
		}
	}
	io.WriteString(w, "</ul>\n")

	fmt.Fprintf(w, "<b>Reverse pool machine detail</b><ul>%s</ul>", buf.Bytes())
}

// HostTypeCount iterates through the running reverse buildlets, and
// constructs a count of running buildlets per hostType.
func (p *ReverseBuildletPool) HostTypeCount() map[string]int {
	total := map[string]int{}
	p.mu.Lock()
	for _, b := range p.buildlets {
		total[b.hostType]++
	}
	p.mu.Unlock()
	return total
}

// SingleHostTypeCount iterates through the running reverse buildlets, and
// constructs a count of the running buildlet hostType requested.
func (p *ReverseBuildletPool) SingleHostTypeCount(hostType string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, b := range p.buildlets {
		if b.hostType == hostType {
			n++
		}
	}
	return n
}

func (p *ReverseBuildletPool) String() string {
	// This doesn't currently show up anywhere, so ignore it for now.
	return "TODO: some reverse buildlet summary"
}

// HostTypes returns the a deduplicated list of buildlet types curently supported
// by the pool.
func (p *ReverseBuildletPool) HostTypes() (types []string) {
	s := make(map[string]bool)
	p.mu.Lock()
	for _, b := range p.buildlets {
		s[b.hostType] = true
	}
	p.mu.Unlock()

	for t := range s {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// CanBuild reports whether the pool has a machine capable of building mode,
// even if said machine isn't currently idle.
func (p *ReverseBuildletPool) CanBuild(hostType string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.buildlets {
		if b.hostType == hostType {
			return true
		}
	}
	return false
}

func (p *ReverseBuildletPool) addBuildlet(b *reverseBuildlet) {
	p.mu.Lock()
	defer p.noteBuildletAvailable(b.hostType)
	defer p.mu.Unlock()
	p.buildlets = append(p.buildlets, b)
	p.recordHealthy(b)
	go p.healthCheckBuildletLoop(b)
}

// BuildletHostnames returns a slice of reverse buildlet hostnames.
func (p *ReverseBuildletPool) BuildletHostnames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	h := make([]string, 0, len(p.buildlets))
	for _, b := range p.buildlets {
		h = append(h, b.hostname)
	}
	return h
}

// reverseBuildlet is a registered reverse buildlet.
// Its immediate fields are guarded by the ReverseBuildletPool mutex.
type reverseBuildlet struct {
	// hostname is the name of the buildlet host.
	// It doesn't have to be a complete DNS name.
	hostname string
	// version is the reverse buildlet's version.
	version      string
	isOldRevDial bool // version 22 or under: using the v1 revdial package (Issue 31639)

	// sessRand is the unique random number for every unique buildlet session.
	sessRand string

	client  *buildlet.Client
	conn    net.Conn
	regTime time.Time // when it was first connected

	// hostType is the configuration of this machine.
	// It is the key into the dashboard.Hosts map.
	hostType string

	// inUseAs signifies that the buildlet is in use.
	// inUseTime is when it entered that state.
	// inHealthCheck is whether it's inUse due to a health check.
	// All three are guarded by the mutex on ReverseBuildletPool.
	inUse         bool
	inUseTime     time.Time
	inHealthCheck bool
}

// HandleReverse handles reverse buildlet connections.
func HandleReverse(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "buildlet registration requires SSL", http.StatusInternalServerError)
		return
	}

	var (
		hostType        = r.Header.Get("X-Go-Host-Type")
		buildKey        = r.Header.Get("X-Go-Builder-Key")
		buildletVersion = r.Header.Get("X-Go-Builder-Version")
		hostname        = r.Header.Get("X-Go-Builder-Hostname")
	)

	switch r.Header.Get("X-Revdial-Version") {
	case "":
		// Old.
		http.Error(w, "buildlet binary is too old", http.StatusBadRequest)
		return
	case "2":
		// Current.
	default:
		http.Error(w, "unknown revdial version", http.StatusBadRequest)
		return
	}

	if hostname == "" {
		http.Error(w, "missing X-Go-Builder-Hostname header", http.StatusBadRequest)
		return
	}

	// Check build keys.
	if hostType == "" {
		http.Error(w, "missing X-Go-Host-Type; old buildlet binary?", http.StatusBadRequest)
		return
	}
	if buildKey != builderKey(hostType) {
		http.Error(w, "invalid build key", http.StatusPreconditionFailed)
		return
	}

	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := (&http.Response{StatusCode: http.StatusSwitchingProtocols, Proto: "HTTP/1.1"}).Write(conn); err != nil {
		log.Printf("error writing upgrade response to reverse buildlet %s (%s) at %s: %v", hostname, hostType, r.RemoteAddr, err)
		conn.Close()
		return
	}

	log.Printf("Registering reverse buildlet %q (%s) for host type %v; buildletVersion=%v",
		hostname, r.RemoteAddr, hostType, buildletVersion)

	revDialer := revdial.NewDialer(conn, "/revdial")
	revDialerDone := revDialer.Done()
	dialer := revDialer.Dial

	client := buildlet.NewClient(hostname, buildlet.NoKeyPair)
	client.SetHTTPClient(&http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer(ctx)
			},
		},
	})
	client.SetDialer(dialer)
	client.SetDescription(fmt.Sprintf("reverse peer %s/%s for host type %v", hostname, r.RemoteAddr, hostType))

	var isDead struct {
		sync.Mutex
		v bool
	}
	client.SetOnHeartbeatFailure(func() {
		isDead.Lock()
		isDead.v = true
		isDead.Unlock()
		conn.Close()
		reversePool.nukeBuildlet(client)
	})

	// If the reverse dialer (which is always reading from the
	// conn) detects that the remote went away, close the buildlet
	// client proactively show
	go func() {
		<-revDialerDone
		isDead.Lock()
		defer isDead.Unlock()
		if !isDead.v {
			client.Close()
		}
	}()
	tstatus := time.Now()
	status, err := client.Status(context.Background())
	if err != nil {
		log.Printf("Reverse connection %s/%s for %s did not answer status after %v: %v",
			hostname, r.RemoteAddr, hostType, time.Since(tstatus), err)
		conn.Close()
		return
	}
	if status.Version < minBuildletVersion {
		log.Printf("Buildlet too old: %s, %+v", r.RemoteAddr, status)
		conn.Close()
		return
	}
	log.Printf("Buildlet %s/%s: %+v for %s", hostname, r.RemoteAddr, status, hostType)

	now := time.Now()
	b := &reverseBuildlet{
		hostname:     hostname,
		version:      buildletVersion,
		isOldRevDial: status.Version < 23,
		hostType:     hostType,
		client:       client,
		conn:         conn,
		inUseTime:    now,
		regTime:      now,
	}
	reversePool.addBuildlet(b)
}

type byTypeThenHostname []*reverseBuildlet

func (s byTypeThenHostname) Len() int      { return len(s) }
func (s byTypeThenHostname) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byTypeThenHostname) Less(i, j int) bool {
	bi, bj := s[i], s[j]
	ti, tj := bi.hostType, bj.hostType
	if ti == tj {
		return bi.hostname < bj.hostname
	}
	return ti < tj
}

// builderrKey generates the builder key used by reverse builders
// to authenticate with the coordinator.
func builderKey(builder string) string {
	if len(builderMasterKey) == 0 {
		return ""
	}
	h := hmac.New(md5.New, builderMasterKey)
	io.WriteString(h, builder)
	return fmt.Sprintf("%x", h.Sum(nil))
}
