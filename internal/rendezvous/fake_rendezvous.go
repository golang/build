// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rendezvous

import (
	"context"
	"log"
	"net/http"
	"time"

	"golang.org/x/build/buildlet"
)

type rendezvousServer interface {
	DeregisterInstance(ctx context.Context, id string)
	HandleReverse(w http.ResponseWriter, r *http.Request)
	RegisterInstance(ctx context.Context, id string, wait time.Duration)
	WaitForInstance(ctx context.Context, id string) (buildlet.Client, error)
}

var _ rendezvousServer = (*FakeRendezvous)(nil)
var _ rendezvousServer = (*Rendezvous)(nil)

// FakeRendezvous is a fake rendezvous implementation intended for use in testing.
type FakeRendezvous struct {
	validator TokenValidator
}

// NewFake creates a Fake Rendezvous instance.
func NewFake(ctx context.Context, validator TokenValidator) *FakeRendezvous {
	rdv := &FakeRendezvous{
		validator: validator,
	}
	return rdv
}

// RegisterInstance is a fake implementation.
func (rdv *FakeRendezvous) RegisterInstance(ctx context.Context, id string, wait time.Duration) {
	// do nothing
}

// DeregisterInstance is a fake implementation.
func (rdv *FakeRendezvous) DeregisterInstance(ctx context.Context, id string) {
	// do nothing
}

// WaitForInstance is a fake implementation.
func (rdv *FakeRendezvous) WaitForInstance(ctx context.Context, id string) (buildlet.Client, error) {
	return &buildlet.FakeClient{}, nil
}

// HandleReverse is a fake implementation of the handler.
func (rdv *FakeRendezvous) HandleReverse(w http.ResponseWriter, r *http.Request) {
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
	if !rdv.validator(r.Context(), authToken) {
		log.Printf("rendezvous: Unable to validate authentication token id=%s", id)
		http.Error(w, "invalid authentication Token", http.StatusPreconditionFailed)
		return
	}
}
