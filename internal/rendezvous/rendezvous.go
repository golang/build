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

	"golang.org/x/build/internal"
)

// result contains the result for a waiting instance registration.
type result struct {
	conn net.Conn
	err  error
}

// entry contains the elements needed to process an instance registration.
type entry struct {
	deadline time.Time
	ch       chan *result
}

// TokenVerifier verifies if a token is valid.
type TokenVerifier func(ctx context.Context, jwt string) bool

// Rendezvous waits for buildlets to connect, verifies they are valid instances
// and passes the connection to the waiting caller.
type Rendezvous struct {
	mu sync.Mutex

	m        map[string]*entry
	verifier TokenVerifier
}

// Option is an optional configuration setting.
type Option func(*Rendezvous)

// OptionVerifier changes the verifier used by Rendezvous.
func OptionVerifier(v TokenVerifier) Option {
	return func(rdv *Rendezvous) {
		rdv.verifier = v
	}
}

// New creates a Rendezvous element. The context that is passed in should be non-canceled
// during the lifetime of the running service.
func New(ctx context.Context, opts ...Option) *Rendezvous {
	rdv := &Rendezvous{
		m:        make(map[string]*entry),
		verifier: verifyToken,
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
	log.Printf("rendezvous: waiting for instance=%q", id)
}

// DeregisterInstance removes the registration for an instance which has been
// previously registered.
func (rdv *Rendezvous) DeregisterInstance(ctx context.Context, id string) {
	rdv.mu.Lock()
	delete(rdv.m, id)
	rdv.mu.Unlock()
	log.Printf("rendezvous: stopped waiting for instance=%q", id)
}

// WaitForInstance waits for the registered instance to successfully connect. It waits for the
// lifetime of the context. If the instance is not registered or has exceeded the timeout period,
// it will immediately return an error.
func (rdv *Rendezvous) WaitForInstance(ctx context.Context, id string) (net.Conn, error) {
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
		return res.conn, res.err
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
	if !rdv.verifier(r.Context(), authToken) {
		http.Error(w, "invalid authentication Token", http.StatusPreconditionFailed)
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("rendezvous instance connected %q", id)
	res.ch <- &result{conn: conn}
}

// verifyToken verifies that the token is valid and contains the expected fields.
func verifyToken(ctx context.Context, jwt string) bool {
	// TODO(go.dev/issue/63354) add service account verification
	return false
}
