// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/coordinator/pool"
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
	waiting map[string]map[*SchedItem]bool // hostType -> item -> true

	// hostsCreating is the number of GetBuildlet calls currently in flight
	// to each hostType's respective buildlet pool.
	hostsCreating map[string]int // hostType -> count

	lastProgress map[string]time.Time // hostType -> time last delivered buildlet
}

// A getBuildletResult is a buildlet that was just created and is up and
// is ready to be assigned to a caller based on priority.
type getBuildletResult struct {
	Pool     pool.Buildlet
	HostType string

	// One of Client or Err gets set:
	Client *buildlet.Client
	Err    error
}

// NewScheduler returns a new scheduler.
func NewScheduler() *Scheduler {
	s := &Scheduler{
		hostsCreating: make(map[string]int),
		waiting:       make(map[string]map[*SchedItem]bool),
		lastProgress:  make(map[string]time.Time),
	}
	return s
}

// matchBuildlet matches up a successful getBuildletResult to the
// highest priority waiter, or closes it if there is none.
func (s *Scheduler) matchBuildlet(res getBuildletResult) {
	if res.Err != nil {
		go s.schedule()
		return
	}
	for {
		waiter, ok := s.matchWaiter(res.HostType)
		if !ok {
			log.Printf("sched: no waiter for buildlet of type %q; closing", res.HostType)
			go res.Client.Close()
			return
		}
		select {
		case ch := <-waiter.wantRes:
			// Normal happy case. Something gets its buildlet.
			ch <- res.Client

			s.mu.Lock()
			s.lastProgress[res.HostType] = time.Now()
			s.mu.Unlock()
			return
		case <-waiter.ctxDone:
			// Waiter went away in the tiny window between
			// matchWaiter returning it and here. This
			// should happen super rarely, so log it to verify that.
			log.Printf("sched: waiter of type %T went away; trying to match next", res.HostType)
		}
	}
}

// schedule starts creating buildlets if there's demand.
//
// It acquires s.mu.
func (s *Scheduler) schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleLocked()
}

// scheduleLocked starts creating buildlets if there's demand.
//
// It requires that s.mu be held.
func (s *Scheduler) scheduleLocked() {
	for hostType, waiting := range s.waiting {
		need := len(waiting) - s.hostsCreating[hostType]
		if need <= 0 {
			continue
		}
		pool := poolForConf(dashboard.Hosts[hostType])
		// TODO: recognize certain pools like the reverse pool
		// that have finite capacity and will just queue up
		// GetBuildlet calls anyway and avoid extra goroutines
		// here and just cap the number of outstanding
		// GetBuildlet calls. But even with thousands of
		// outstanding builds, that's a small constant memory
		// savings, so for now just do the simpler thing.
		for i := 0; i < need; i++ {
			s.hostsCreating[hostType]++
			go s.getPoolBuildlet(pool, hostType)
		}
	}
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
	return createSpan(l, event, optText...)
}

// getPoolBuildlet is launched as its own goroutine to do a
// potentially long blocking cal to pool.GetBuildlet.
func (s *Scheduler) getPoolBuildlet(pool pool.Buildlet, hostType string) {
	res := getBuildletResult{
		Pool:     pool,
		HostType: hostType,
	}
	ctx := context.Background() // TODO: make these cancelable and cancel unneeded ones earlier?
	res.Client, res.Err = pool.GetBuildlet(ctx, hostType, stderrLogger{})

	// This is still slightly racy, but probably ok for now.
	// (We might invoke the schedule method right after
	// GetBuildlet returns and dial an extra buildlet, but if so
	// we'll close it without using it.)
	s.mu.Lock()
	s.hostsCreating[res.HostType]--
	s.mu.Unlock()

	s.matchBuildlet(res)
}

// matchWaiter returns (and removes from the waiting set) the highest priority SchedItem
// that matches the provided host type.
func (s *Scheduler) matchWaiter(hostType string) (_ *SchedItem, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.waiting[hostType]

	var best *SchedItem
	for si := range waiters {
		if best == nil || schedLess(si, best) {
			best = si
		}
	}
	if best != nil {
		delete(waiters, best)
		return best, true
	}
	return nil, false
}

func (s *Scheduler) removeWaiter(si *SchedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.waiting[si.HostType]; m != nil {
		delete(m, si)
	}
}

func (s *Scheduler) addWaiter(si *SchedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.waiting[si.HostType]; !ok {
		s.waiting[si.HostType] = make(map[*SchedItem]bool)
	}
	s.waiting[si.HostType][si] = true
	s.scheduleLocked()
}

func (s *Scheduler) hasWaiter(si *SchedItem) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting[si.HostType][si]
}

type schedulerWaitingState struct {
	Count  int
	Newest time.Duration
	Oldest time.Duration
}

func (st *schedulerWaitingState) add(si *SchedItem) {
	st.Count++
	age := time.Since(si.requestTime).Round(time.Second)
	if st.Newest == 0 || age < st.Newest {
		st.Newest = age
	}
	if st.Oldest == 0 || age > st.Oldest {
		st.Oldest = age
	}
}

type schedulerHostState struct {
	HostType     string
	LastProgress time.Duration
	Total        schedulerWaitingState
	Gomote       schedulerWaitingState
	Try          schedulerWaitingState
	Regular      schedulerWaitingState
}

type schedulerState struct {
	HostTypes []schedulerHostState
}

func (s *Scheduler) state() (st schedulerState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for hostType, m := range s.waiting {
		if len(m) == 0 {
			continue
		}
		var hst schedulerHostState
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

// waiterState returns tells waiter how many callers are on the line
// in front of them.
func (s *Scheduler) waiterState(waiter *SchedItem) (ws types.BuildletWaitStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := s.waiting[waiter.HostType]
	for si := range m {
		if schedLess(si, waiter) {
			ws.Ahead++
		}
	}

	return ws
}

// schedLess reports whether the scheduler item ia is "less" (more
// important) than scheduler item ib.
func schedLess(ia, ib *SchedItem) bool {
	// TODO: flesh out this policy more. For now this is much
	// better than the old random policy.
	// For example, consider IsHelper? Figure out a policy.
	// TODO: consider SchedItem.Branch.
	// TODO: pass in a context to schedLess that includes current time and current
	// top of various branches. Then we can use that in decisions rather than doing
	// lookups or locks in a less function.

	// Gomote is most important, then TryBots (FIFO for either), then
	// post-submit builds (LIFO, by commit time)
	if ia.IsGomote != ib.IsGomote {
		return ia.IsGomote
	}
	if ia.IsTry != ib.IsTry {
		return ia.IsTry
	}
	// Gomote and TryBots are FIFO.
	if ia.IsGomote || ia.IsTry {
		// TODO: if IsTry, consider how many TryBot requests
		// are outstanding per user. The scheduler should
		// round-robin between CL authors, rather than use
		// time. But time works for now.
		return ia.requestTime.Before(ib.requestTime)
	}

	// Post-submit builds are LIFO by commit time, not necessarily
	// when the coordinator's findWork loop threw them at the
	// scheduler.
	return ia.CommitTime.After(ib.CommitTime)
}

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
	// buffering doesn't matter) to pass over a *buildlet.Client
	// just obtained from a BuildletPool. The contract to use
	// wantRes is that the sender must have a result already
	// available to send on the inner channel, and the receiver
	// still wants it (their context hasn't expired).
	wantRes chan chan<- *buildlet.Client
}

// GetBuildlet requests a buildlet with the parameters described in si.
//
// The provided si must be newly allocated; ownership passes to the scheduler.
func (s *Scheduler) GetBuildlet(ctx context.Context, si *SchedItem) (*buildlet.Client, error) {
	hostConf, ok := dashboard.Hosts[si.HostType]
	if !ok && testPoolHook == nil {
		return nil, fmt.Errorf("invalid SchedItem.HostType %q", si.HostType)
	}
	pool := poolForConf(hostConf)

	si.pool = pool
	si.s = s
	si.requestTime = time.Now()
	si.ctxDone = ctx.Done()
	si.wantRes = make(chan chan<- *buildlet.Client) // unbuffered

	s.addWaiter(si)

	ch := make(chan *buildlet.Client)
	select {
	case si.wantRes <- ch:
		// No need to call removeWaiter. If we're here, the
		// sender has already done so.
		return <-ch, nil
	case <-ctx.Done():
		s.removeWaiter(si)
		return nil, ctx.Err()
	}
}
