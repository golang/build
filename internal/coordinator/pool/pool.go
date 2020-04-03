// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pool

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"golang.org/x/build/buildlet"
)

// BuildletTimeoutOpt is a context.Value key for BuildletPool.GetBuildlet.
type BuildletTimeoutOpt struct{} // context Value key; value is time.Duration

// Buildlet defines an interface for a pool of buildlets.
type Buildlet interface {
	// GetBuildlet returns a new buildlet client.
	//
	// The hostType is the key into the dashboard.Hosts
	// map (such as "host-linux-jessie"), NOT the buidler type
	// ("linux-386").
	//
	// Users of GetBuildlet must both call Client.Close when done
	// with the client as well as cancel the provided Context.
	//
	// The ctx may have context values of type buildletTimeoutOpt
	// and highPriorityOpt.
	GetBuildlet(ctx context.Context, hostType string, lg Logger) (*buildlet.Client, error)

	String() string // TODO(bradfitz): more status stuff
}

// randHex generates a random hex string.
func randHex(n int) string {
	buf := make([]byte, n/2+1)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("randHex: %v", err)
	}
	return fmt.Sprintf("%x", buf)[:n]
}

func friendlyDuration(d time.Duration) string {
	if d > 10*time.Second {
		d2 := ((d + 50*time.Millisecond) / (100 * time.Millisecond)) * (100 * time.Millisecond)
		return d2.String()
	}
	if d > time.Second {
		d2 := ((d + 5*time.Millisecond) / (10 * time.Millisecond)) * (10 * time.Millisecond)
		return d2.String()
	}
	d2 := ((d + 50*time.Microsecond) / (100 * time.Microsecond)) * (100 * time.Microsecond)
	return d2.String()
}
