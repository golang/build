// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package remote

import (
	"context"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
)

// Buildlets is a store for the legacy remote buildlets.
type Buildlets struct {
	sync.Mutex
	M map[string]*Buildlet // keyed by buildletName
}

// Buildlet is the representation of the legacy remote buildlet.
type Buildlet struct {
	User        string // "user-foo" build key
	Name        string // dup of key
	HostType    string
	BuilderType string // default builder config to use if not overwritten
	Created     time.Time
	Expires     time.Time

	buildlet buildlet.Client
}

// Renew renews rb's idle timeout if ctx hasn't expired.
// Renew should run in its own goroutine.
func (rb *Buildlet) Renew(ctx context.Context, rbs *Buildlets) {
	rbs.Lock()
	defer rbs.Unlock()
	select {
	case <-ctx.Done():
		return
	default:
	}
	if got := rbs.M[rb.Name]; got == rb {
		rb.Expires = time.Now().Add(remoteBuildletIdleTimeout)
		time.AfterFunc(time.Minute, func() { rb.Renew(ctx, rbs) })
	}
}

// Buildlet returns the buildlet client for the associated legacy buildlet.
func (rb *Buildlet) Buildlet() buildlet.Client {
	return rb.buildlet
}

// SetBuildlet sets the buildlet client for a legacy buildlet.
func (rb *Buildlet) SetBuildlet(b buildlet.Client) {
	rb.buildlet = b
}
