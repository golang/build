// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rendezvous

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal"
	"golang.org/x/build/revdial/v2"
	"google.golang.org/api/idtoken"
)

// result contains the result for a waiting instance registration.
type result struct {
	bc  buildlet.Client
	err error
}

// entry contains the elements needed to process an instance registration.
type entry struct {
	deadline time.Time
	ch       chan *result
}

// TokenValidator verifies if a token is valid.
type TokenValidator func(ctx context.Context, jwt string) bool

// Rendezvous waits for buildlets to connect, verifies they are valid instances
// and passes the connection to the waiting caller.
type Rendezvous struct {
	mu sync.Mutex

	m         map[string]*entry
	validator TokenValidator
}

// Option is an optional configuration setting.
type Option func(*Rendezvous)

// OptionValidator changes the verifier used by Rendezvous.
func OptionValidator(v TokenValidator) Option {
	return func(rdv *Rendezvous) {
		rdv.validator = v
	}
}

// New creates a Rendezvous element. The context that is passed in should be non-canceled
// during the lifetime of the running service.
func New(ctx context.Context, opts ...Option) *Rendezvous {
	rdv := &Rendezvous{
		m:         make(map[string]*entry),
		validator: validateLUCIIDToken,
	}
	for _, opt := range opts {
		opt(rdv)
	}
	go internal.PeriodicallyDo(ctx, 10*time.Second, func(ctx context.Context, t time.Time) {
		rdv.purgeExpiredRegistrations()
	})
	return rdv
}

// purgeExpiredRegistrations will purge expired registrations.
func (rdv *Rendezvous) purgeExpiredRegistrations() {
	rdv.mu.Lock()
	for id, ent := range rdv.m {
		if time.Now().After(ent.deadline) {
			log.Printf("rendezvous: stopped waiting for instance=%q due to timeout", id)
			ent.ch <- &result{err: fmt.Errorf("timed out waiting for rendezvous client=%q", id)}
			delete(rdv.m, id)
		}
	}
	rdv.mu.Unlock()
}

// RegisterInstance notes an instance and waits for that instance to connect to the handler. An
// instance must be registered before the instance can attempt to connect. If an instance does
// not connect before the end of the wait period, the instance will not be able to connect.
func (rdv *Rendezvous) RegisterInstance(ctx context.Context, id string, wait time.Duration) {
	rdv.mu.Lock()
	rdv.m[id] = &entry{
		deadline: time.Now().Add(wait),
		ch:       make(chan *result, 1),
	}
	rdv.mu.Unlock()
}

// DeregisterInstance removes the registration for an instance which has been
// previously registered.
func (rdv *Rendezvous) DeregisterInstance(ctx context.Context, id string) {
	rdv.mu.Lock()
	delete(rdv.m, id)
	rdv.mu.Unlock()
}

// WaitForInstance waits for the registered instance to successfully connect. It waits for the
// lifetime of the context. If the instance is not registered or has exceeded the timeout period,
// it will immediately return an error.
func (rdv *Rendezvous) WaitForInstance(ctx context.Context, id string) (buildlet.Client, error) {
	rdv.mu.Lock()
	e, ok := rdv.m[id]
	rdv.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("instance not found: name=%q", id)
	}
	select {
	case <-ctx.Done():
		rdv.mu.Lock()
		delete(rdv.m, id)
		rdv.mu.Unlock()
		return nil, fmt.Errorf("context timeout waiting for rendezvous client=%q: %w", id, ctx.Err())
	case res := <-e.ch:
		rdv.mu.Lock()
		delete(rdv.m, id)
		close(e.ch)
		rdv.mu.Unlock()
		return res.bc, res.err
	}
}

const (
	// HeaderID is the HTTP header used for passing the gomote ID.
	HeaderID = "X-Go-Gomote-ID"
	// HeaderToken is the HTTP header used for passing in the authentication token.
	HeaderToken = "X-Go-Swarming-Auth-Token"
	// HeaderHostname is the HTTP header used for passing in the hostname.
	HeaderHostname = "X-Go-Hostname"
)

// HandleReverse handles HTTP requests from the buildlet and passes the connection to
// the waiter.
func (rdv *Rendezvous) HandleReverse(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "buildlet registration requires SSL", http.StatusInternalServerError)
		return
	}
	var (
		id        = r.Header.Get(HeaderID)
		authToken = r.Header.Get(HeaderToken)
		hostname  = r.Header.Get(HeaderHostname)
	)
	if hostname == "" {
		http.Error(w, "missing X-Go-Hostname header", http.StatusBadRequest)
		return
	}
	if id == "" {
		http.Error(w, "missing X-Go-Gomote-ID header", http.StatusBadRequest)
		return
	}
	if authToken == "" {
		http.Error(w, "missing X-Go-Swarming-Auth-Token header", http.StatusBadRequest)
		return
	}
	rdv.mu.Lock()
	res, ok := rdv.m[id]
	rdv.mu.Unlock()

	if !ok {
		http.Error(w, "not expecting buildlet client", http.StatusPreconditionFailed)
		return
	}
	if !rdv.validator(r.Context(), authToken) {
		log.Printf("rendezvous: Unable to validate authentication token id=%s", id)
		http.Error(w, "invalid authentication Token", http.StatusPreconditionFailed)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "webserver does not support hijacking", http.StatusHTTPVersionNotSupported)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		res.ch <- &result{err: err}
		return
	}
	bc, err := connToClient(conn, hostname, "swarming_task")
	if err != nil {
		log.Printf("rendezvous: unable to create buildlet client: %s", err)
		conn.Close()
		res.ch <- &result{err: err}
		return
	}
	res.ch <- &result{bc: bc}
}

func connToClient(conn net.Conn, hostname, hostType string) (buildlet.Client, error) {
	if err := (&http.Response{StatusCode: http.StatusSwitchingProtocols, Proto: "HTTP/1.1"}).Write(conn); err != nil {
		log.Printf("gomote: error writing upgrade response to reverse buildlet %s (%s) at %s: %v", hostname, hostType, conn.RemoteAddr(), err)
		conn.Close()
		return nil, err
	}
	revDialer := revdial.NewDialer(conn, "/revdial")
	revDialerDone := revDialer.Done()
	dialer := revDialer.Dial

	client := buildlet.NewClient(conn.RemoteAddr().String(), buildlet.NoKeyPair)
	client.SetHTTPClient(&http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer(ctx)
			},
		},
	})
	client.SetDialer(dialer)
	client.SetDescription(fmt.Sprintf("reverse peer %s/%s for host type %v", hostname, conn.RemoteAddr(), hostType))

	var isDead struct {
		sync.Mutex
		v bool
	}
	client.SetOnHeartbeatFailure(func() {
		isDead.Lock()
		isDead.v = true
		isDead.Unlock()
		conn.Close()
	})

	// If the reverse dialer (which is always reading from the
	// conn detects that the remote went away, close the buildlet
	// client proactively.
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
			hostname, conn.RemoteAddr(), hostType, time.Since(tstatus), err)
		conn.Close()
		return nil, err
	}
	log.Printf("Buildlet %s/%s: %+v for %s", hostname, conn.RemoteAddr(), status, hostType)
	return client, nil
}

// validateLUCIIDToken verifies that the token is valid and contains the expected fields.
func validateLUCIIDToken(ctx context.Context, jwt string) bool {
	payload, err := idtoken.Validate(ctx, jwt, "https://gomote.golang.org")
	if err != nil {
		log.Printf("rendezvous: unable to validate authentication token: %s", err)
		return false
	}
	if payload.Issuer != "https://accounts.google.com" {
		log.Printf("rendezvous: incorrect issuer: %q", payload.Issuer)
		return false
	}
	if payload.Expires+30 < time.Now().Unix() || payload.IssuedAt-30 > time.Now().Unix() {
		log.Printf("rendezvous: Bad JWT times: expires %v, issued %v", time.Unix(payload.Expires, 0), time.Unix(payload.IssuedAt, 0))
		return false
	}
	email, ok := payload.Claims["email"]
	if !ok || email != "coordinator-builder@golang-ci-luci.iam.gserviceaccount.com" {
		log.Printf("rendezvous: incorrect email=%s", email)
		return false
	}
	emailVerified, ok := payload.Claims["email_verified"].(bool)
	if !ok || !emailVerified {
		log.Printf("rendezvous: email unverified email=%s", email)
		return false
	}
	return true
}
