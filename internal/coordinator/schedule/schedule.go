// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package schedule

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/spanlog"
	"golang.org/x/build/types"
)

// The Scheduler prioritizes access to buidlets. It accepts requests
// for buildlets, starts the creation of buildlets from BuildletPools,
// and prioritizes which callers gets them first when they're ready.
type Scheduler struct {
	// mu guards the following fields.
	mu sync.Mutex

	// waiting contains all the set of callers who are waiting for
	// a buildlet, keyed by the host type they're waiting for.
	waiting map[string]map[*queue.SchedItem]bool // hostType -> item -> true

	// hostsCreating is the number of GetBuildlet calls currently in flight
	// to each hostType's respective buildlet pool.
	hostsCreating map[string]int // hostType -> count

	lastProgress map[string]time.Time // hostType -> time last delivered buildlet
}

// NewScheduler returns a new scheduler.
func NewScheduler() *Scheduler {
	s := &Scheduler{
		hostsCreating: make(map[string]int),
		waiting:       make(map[string]map[*queue.SchedItem]bool),
		lastProgress:  make(map[string]time.Time),
	}
	return s
}

type stderrLogger struct{}

func (stderrLogger) LogEventTime(event string, optText ...string) {
	if len(optText) == 0 {
		log.Printf("sched.getbuildlet: %v", event)
	} else {
		log.Printf("sched.getbuildlet: %v, %v", event, optText[0])
	}
}

func (l stderrLogger) CreateSpan(event string, optText ...string) spanlog.Span {
	return CreateSpan(l, event, optText...)
}

func (s *Scheduler) removeWaiter(si *queue.SchedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.waiting[si.HostType]; m != nil {
		delete(m, si)
	}
}

func (s *Scheduler) addWaiter(si *queue.SchedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.waiting[si.HostType]; !ok {
		s.waiting[si.HostType] = make(map[*queue.SchedItem]bool)
	}
	s.waiting[si.HostType][si] = true
}

func (s *Scheduler) hasWaiter(si *queue.SchedItem) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting[si.HostType][si]
}

type SchedulerWaitingState struct {
	Count  int
	Newest time.Duration
	Oldest time.Duration
}

func (st *SchedulerWaitingState) add(si *queue.SchedItem) {
	st.Count++
	age := time.Since(si.RequestTime).Round(time.Second)
	if st.Newest == 0 || age < st.Newest {
		st.Newest = age
	}
	if st.Oldest == 0 || age > st.Oldest {
		st.Oldest = age
	}
}

type SchedulerHostState struct {
	HostType     string
	LastProgress time.Duration
	Total        SchedulerWaitingState
	Gomote       SchedulerWaitingState
	Try          SchedulerWaitingState
	Regular      SchedulerWaitingState
}

type SchedulerState struct {
	HostTypes []SchedulerHostState
}

func (s *Scheduler) State() (st SchedulerState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for hostType, m := range s.waiting {
		if len(m) == 0 {
			continue
		}
		var hst SchedulerHostState
		hst.HostType = hostType
		for si := range m {
			hst.Total.add(si)
			if si.IsGomote {
				hst.Gomote.add(si)
			} else if si.IsTry {
				hst.Try.add(si)
			} else {
				hst.Regular.add(si)
			}
		}
		if lp := s.lastProgress[hostType]; !lp.IsZero() {
			lastProgressAgo := time.Since(lp)
			if lastProgressAgo < hst.Total.Oldest {
				hst.LastProgress = lastProgressAgo.Round(time.Second)
			}
		}
		st.HostTypes = append(st.HostTypes, hst)
	}

	sort.Slice(st.HostTypes, func(i, j int) bool { return st.HostTypes[i].HostType < st.HostTypes[j].HostType })
	return st
}

// WaiterState returns tells waiter how many callers are on the line
// in front of them.
func (s *Scheduler) WaiterState(waiter *queue.SchedItem) (ws types.BuildletWaitStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := s.waiting[waiter.HostType]
	for si := range m {
		if si.Less(waiter) {
			ws.Ahead++
		}
	}

	return ws
}

// GetBuildlet requests a buildlet with the parameters described in si.
//
// The provided si must be newly allocated; ownership passes to the scheduler.
func (s *Scheduler) GetBuildlet(ctx context.Context, si *queue.SchedItem) (buildlet.Client, error) {
	hostConf, ok := dashboard.Hosts[si.HostType]
	if !ok && pool.TestPoolHook == nil {
		return nil, fmt.Errorf("invalid SchedItem.HostType %q", si.HostType)
	}
	si.RequestTime = time.Now()

	s.addWaiter(si)
	defer s.removeWaiter(si)

	return pool.ForHost(hostConf).GetBuildlet(ctx, si.HostType, stderrLogger{}, si)
}
