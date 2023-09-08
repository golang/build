// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package pool

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/coordinator/pool/queue"
)

// Buildlet defines an interface for a pool of buildlets.
type Buildlet interface {
	// GetBuildlet returns a new buildlet client.
	//
	// The hostType is the key into the dashboard.Hosts
	// map (such as "host-linux-bullseye"), NOT the buidler type
	// ("linux-386").
	//
	// Users of GetBuildlet must both call Client.Close when done
	// with the client as well as cancel the provided Context.
	GetBuildlet(ctx context.Context, hostType string, lg Logger, item *queue.SchedItem) (buildlet.Client, error)

	String() string // TODO(bradfitz): more status stuff
}

// IsRemoteBuildletFunc should report whether the buildlet instance name is
// is a remote buildlet. This is applicable to GCE and EC2 instances.
//
// TODO(golang.org/issue/38337): should be removed once remote buildlet management
// functions are moved into a package.
type IsRemoteBuildletFunc func(instanceName string) bool

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

// instanceName generates a random instance name according to the host type.
func instanceName(hostType string, length int) string {
	return fmt.Sprintf("buildlet-%s-rn%s", strings.TrimPrefix(hostType, "host-"), randHex(length))
}

// determineDeleteTimeout reports the buildlet delete timeout duration
// with the following priority:
//
// 1. Host type override from host config.
// 2. Global default.
func determineDeleteTimeout(host *dashboard.HostConfig) time.Duration {
	if host.CustomDeleteTimeout != 0 {
		return host.CustomDeleteTimeout
	}

	// The value we return below is effectively a global default.
	//
	// The comment of CleanUpOldVMs (and CleanUpOldPodsLoop) includes:
	//
	//	This is the safety mechanism to delete VMs which stray from the
	//	normal deleting process. VMs are created to run a single build and
	//	should be shut down by a controlling process. Due to various types
	//	of failures, they might get stranded. To prevent them from getting
	//	stranded and wasting resources forever, we instead set the
	//	"delete-at" metadata attribute on them when created to some time
	//	that's well beyond their expected lifetime.
	//
	// Issue go.dev/issue/52929 tracks what to do about this global
	// timeout in the long term. Unless something changes,
	// it needs to be maintained manually so that it's always
	// "well beyond their expected lifetime" of each builder that doesn't
	// otherwise override this timeout—otherwise it'll cause even more
	// resources to be used due the automatic (and unlimited) retrying
	// as described in go.dev/issue/42699.
	//
	// A global timeout of 45 minutes was chosen in 2015.
	// Longtest builders were added in 2018 started to reach 45 mins by 2021-2022.
	// Try 2 hours next, which might last some years (depending on test volume and test speed).
	return 2 * time.Hour
}

// isBuildlet checks the name string in order to determine if the name is for a buildlet.
func isBuildlet(name string) bool {
	return strings.HasPrefix(name, "buildlet-")
}

// TestPoolHook is used to override the buildlet returned by ForConf. It should only be used for
// testing purposes.
var TestPoolHook func(*dashboard.HostConfig) Buildlet

// ForHost returns the appropriate buildlet depending on the host configuration that is passed it.
// The returned buildlet can be overridden for testing purposes by registering a test hook.
func ForHost(conf *dashboard.HostConfig) Buildlet {
	if TestPoolHook != nil {
		return TestPoolHook(conf)
	}
	if conf == nil {
		panic("nil conf")
	}
	switch {
	case conf.IsEC2:
		return EC2BuildetPool()
	case conf.IsVM(), conf.IsContainer():
		return NewGCEConfiguration().BuildletPool()
	case conf.IsReverse:
		return ReversePool()
	default:
		panic(fmt.Sprintf("no buildlet pool for host type %q", conf.HostType))
	}
}
