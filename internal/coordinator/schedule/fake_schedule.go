// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package schedule

import (
	"context"
	"sync"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/types"
)

// Fake is a fake scheduler.
type Fake struct {
	mu    sync.Mutex
	state SchedulerState
}

// NewFake returns a fake scheduler.
func NewFake() *Fake {
	return &Fake{
		state: SchedulerState{
			HostTypes: []SchedulerHostState{},
		},
	}
}

// State returns the state of the fake scheduler.
func (f *Fake) State() (st SchedulerState) { return f.state }

// WaiterState is the waiter state of the fake scheduler.
func (f *Fake) WaiterState(waiter *queue.SchedItem) (ws types.BuildletWaitStatus) {
	return types.BuildletWaitStatus{
		Message: "buildlet created",
		Ahead:   0,
	}
}

// GetBuildlet returns a fake buildlet client for the requested buildlet.
func (f *Fake) GetBuildlet(ctx context.Context, si *queue.SchedItem) (buildlet.Client, error) {
	return &buildlet.FakeClient{}, nil
}
