// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package schedule

import (
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/coordinator/pool"
)

type BuildletPriority int

const (
	// PriorityUrgent is reserved for Go releases.
	PriorityUrgent BuildletPriority = iota
	PriorityInteractive
	PriorityAutomated
	PriorityBatch
)

// SchedItem is a specification of a requested buildlet in its
// exported fields, and internal scheduler state used while waiting
// for that buildlet.
type SchedItem struct {
	buildgo.BuilderRev // not set for gomote
	HostType           string
	IsGomote           bool
	IsTry              bool
	IsHelper           bool
	Branch             string

	// CommitTime is the latest commit date of the relevant repos
	// that make up the work being tested. (For example, x/foo
	// being tested against master can have either x/foo commit
	// being newer, or master being newer).
	CommitTime time.Time

	// The following unexported fields are set by the Scheduler in
	// Scheduler.GetBuildlet.

	s           *Scheduler
	requestTime time.Time
	tryFor      string // TODO: which user. (user with 1 trybot >> user with 50 trybots)
	pool        pool.Buildlet
	ctxDone     <-chan struct{}

	// wantRes is the unbuffered channel that's passed
	// synchronously from Scheduler.GetBuildlet to
	// Scheduler.matchBuildlet. Its value is a channel (whose
	// buffering doesn't matter) to pass over a buildlet.Client
	// just obtained from a BuildletPool. The contract to use
	// wantRes is that the sender must have a result already
	// available to send on the inner channel, and the receiver
	// still wants it (their context hasn't expired).
	wantRes chan chan<- buildlet.Client
}

// Priority returns the BuildletPriority for a SchedItem.
func (s *SchedItem) Priority() BuildletPriority {
	switch {
	case s.IsGomote:
		return PriorityInteractive
	case s.IsTry:
		return PriorityAutomated
	default:
		return PriorityBatch
	}
}

func (s *SchedItem) sortTime() time.Time {
	if s.IsGomote || s.IsTry || s.CommitTime.IsZero() {
		return s.requestTime
	}
	return s.CommitTime
}

// Less returns a boolean value of whether SchedItem is more important
// than the provided SchedItem.
func (s *SchedItem) Less(other *SchedItem) bool {
	if s.Priority() != other.Priority() {
		return s.Priority() < other.Priority()
	}
	if s.Priority() == PriorityBatch {
		// Batch items are completed in LIFO.
		return s.sortTime().After(other.sortTime())
	}
	return other.sortTime().After(s.sortTime())
}
