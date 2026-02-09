// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package schedule

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	cpool "golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/spanlog"
)

type discardLogger struct{}

func (discardLogger) LogEventTime(event string, optText ...string) {}

func (discardLogger) CreateSpan(event string, optText ...string) spanlog.Span {
	return CreateSpan(discardLogger{}, event, optText...)
}

// step is a test step for TestScheduler
type step func(*testing.T, *Scheduler)

// getBuildletCall represents a call to GetBuildlet.
type getBuildletCall struct {
	si        *queue.SchedItem
	ctx       context.Context
	ctxCancel context.CancelFunc

	done      chan struct{} // closed when call done
	gotClient buildlet.Client
	gotErr    error
}

func newGetBuildletCall(si *queue.SchedItem) *getBuildletCall {
	c := &getBuildletCall{
		si:   si,
		done: make(chan struct{}),
	}
	c.ctx, c.ctxCancel = context.WithCancel(context.Background())
	return c
}

func (c *getBuildletCall) cancel(t *testing.T, s *Scheduler) { c.ctxCancel() }

// start is a step (assignable to type step) that starts a
// s.GetBuildlet call and waits for it to either succeed or get
// blocked in the scheduler.
func (c *getBuildletCall) start(t *testing.T, s *Scheduler) {
	t.Logf("starting buildlet call for SchedItem=%p", c.si)
	go func() {
		c.gotClient, c.gotErr = s.GetBuildlet(c.ctx, c.si)
		close(c.done)
	}()

	// Wait for si to be enqueued, or this call to be satisfied.
	if !trueSoon(func() bool {
		select {
		case <-c.done:
			return true
		default:
			return s.hasWaiter(c.si)
		}
	}) {
		t.Fatalf("timeout waiting for GetBuildlet call to run to its blocking point")
	}
}

func trueSoon(f func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			return false
		}
		if f() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wantGetBuildlet is a step (assignable to type step) that) that expects
// the GetBuildlet call to succeed.
func (c *getBuildletCall) wantGetBuildlet(t *testing.T, s *Scheduler) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	t.Logf("waiting on sched.getBuildlet(%q) ...", c.si.HostType)
	select {
	case <-c.done:
		t.Logf("got sched.getBuildlet(%q).", c.si.HostType)
		if c.gotErr != nil {
			t.Fatalf("GetBuildlet(%q): %v", c.si.HostType, c.gotErr)
		}
	case <-timer.C:
		stack := make([]byte, 1<<20)
		stack = stack[:runtime.Stack(stack, true)]
		t.Fatalf("timeout waiting for buildlet of type %q; stacks:\n%s", c.si.HostType, stack)
	}
}

type fakePool struct {
	poolChan map[string]chan any // hostType -> { buildlet.Client | error}
}

func (f *fakePool) GetBuildlet(ctx context.Context, hostType string, lg cpool.Logger, item *queue.SchedItem) (buildlet.Client, error) {
	c, ok := f.poolChan[hostType]
	if !ok {
		return nil, fmt.Errorf("pool doesn't support host type %q", hostType)
	}
	select {
	case v := <-c:
		if c, ok := v.(buildlet.Client); ok {
			return c, nil
		}
		return nil, v.(error)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (fakePool) String() string { return "testing poolChan" }

func TestScheduler(t *testing.T) {
	defer func() { cpool.TestPoolHook = nil }()

	var pool *fakePool // initialized per test below
	// buildletAvailable is a step that creates a buildlet to the pool.
	buildletAvailable := func(hostType string) step {
		return func(t *testing.T, s *Scheduler) {
			bc := buildlet.NewClient("127.0.0.1:9999", buildlet.NoKeyPair) // dummy
			t.Logf("adding buildlet to pool for %q...", hostType)
			ch := pool.poolChan[hostType]
			ch <- bc
			t.Logf("added buildlet to pool for %q (ch=%p)", hostType, ch)
		}
	}

	tests := []struct {
		name  string
		steps func() []step
	}{
		{
			name: "simple-get-before-available",
			steps: func() []step {
				si := &queue.SchedItem{HostType: "test-host-foo"}
				fooGet := newGetBuildletCall(si)
				return []step{
					fooGet.start,
					buildletAvailable("test-host-foo"),
					fooGet.wantGetBuildlet,
				}
			},
		},
		{
			name: "simple-get-already-available",
			steps: func() []step {
				si := &queue.SchedItem{HostType: "test-host-foo"}
				fooGet := newGetBuildletCall(si)
				return []step{
					buildletAvailable("test-host-foo"),
					fooGet.start,
					fooGet.wantGetBuildlet,
				}
			},
		},
		{
			name: "cancel-context-removes-waiter",
			steps: func() []step {
				si := &queue.SchedItem{HostType: "test-host-foo"}
				get := newGetBuildletCall(si)
				return []step{
					get.start,
					get.cancel,
					func(t *testing.T, s *Scheduler) {
						if !trueSoon(func() bool { return !s.hasWaiter(si) }) {
							t.Errorf("still have SchedItem in waiting set")
						}
					},
				}
			},
		},
	}
	for _, tt := range tests {
		pool = &fakePool{poolChan: map[string]chan any{}}
		pool.poolChan["test-host-foo"] = make(chan any, 1)
		pool.poolChan["test-host-bar"] = make(chan any, 1)

		cpool.TestPoolHook = func(*dashboard.HostConfig) cpool.Buildlet { return pool }
		t.Run(tt.name, func(t *testing.T) {
			s := NewScheduler()
			for i, st := range tt.steps() {
				t.Logf("step %v...", i)
				st(t, s)
			}
		})
	}
}
