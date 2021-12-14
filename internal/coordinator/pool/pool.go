// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

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
	GetBuildlet(ctx context.Context, hostType string, lg Logger) (buildlet.Client, error)

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

// deleteTimeoutFromContextOrValue retrieves the buildlet timeout duration from the
// context. If it it is not found in the context, it will fallback to using the timeout passed
// into the function.
func deleteTimeoutFromContextOrValue(ctx context.Context, timeout time.Duration) time.Duration {
	deleteIn, ok := ctx.Value(BuildletTimeoutOpt{}).(time.Duration)
	if !ok {
		deleteIn = timeout
	}
	return deleteIn
}

// isBuildlet checks the name string in order to determine if the name is for a buildlet.
func isBuildlet(name string) bool {
	return strings.HasPrefix(name, "buildlet-")
}

// TestPoolHook is used to override the buildlet returned by ForConf. It should only be used for
// testing purposes.
var TestPoolHook func(*dashboard.HostConfig) Buildlet

// ForHost returns the appropriate buildlet depending on the host configuration that is passed it.
// The returned buildlet can be overriden for testing purposes by registering a test hook.
func ForHost(conf *dashboard.HostConfig) Buildlet {
	if TestPoolHook != nil {
		return TestPoolHook(conf)
	}
	if conf == nil {
		panic("nil conf")
	}
	switch {
	case conf.IsEC2():
		return EC2BuildetPool()
	case conf.IsVM():
		return NewGCEConfiguration().BuildletPool()
	case conf.IsContainer():
		if NewGCEConfiguration().BuildEnv().PreferContainersOnCOS || KubeErr() != nil {
			return NewGCEConfiguration().BuildletPool() // it also knows how to do containers.
		} else {
			return KubePool()
		}
	case conf.IsReverse:
		return ReversePool()
	default:
		panic(fmt.Sprintf("no buildlet pool for host type %q", conf.HostType))
	}
}
