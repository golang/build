// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package schedule

import (
	"context"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/types"
)

// Fake is a fake scheduler.
type Fake struct{}

// NewFake returns a fake scheduler.
func NewFake() *Fake { return &Fake{} }

// State returns the state of the fake scheduler.
func (f *Fake) State() (st SchedulerState) { return SchedulerState{} }

// WaiterState is the waiter state of the fake scheduler.
func (f *Fake) WaiterState(waiter *SchedItem) (ws types.BuildletWaitStatus) {
	return types.BuildletWaitStatus{}
}

// GetBuildlet returns a fake buildlet client for the requested buildlet.
func (f *Fake) GetBuildlet(ctx context.Context, si *SchedItem) (buildlet.Client, error) {
	return &buildlet.FakeClient{}, nil
}
