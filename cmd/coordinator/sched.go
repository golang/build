// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"context"
	"log"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/cmd/coordinator/spanlog"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/buildgo"
)

// useScheduler controls whether we actually use the scheduler. This
// is temporarily false during development. Once we're happy with it
// we'll delete this const.
//
// If false, any GetBuildlet call to the schedule delegates directly
// to the BuildletPool's GetBuildlet and we make a bunch of callers
// fight over a mutex and a random one wins, like we used to do it.
const useScheduler = false

// The Scheduler prioritizes access to buidlets. It accepts requests
// for buildlets, starts the creation of buildlets from BuildletPools,
// and prioritizes which callers gets them first when they're ready.
type Scheduler struct {
	// mu guards waiting and hostsCreating.
	mu sync.Mutex

	// waiting contains all the set of callers who are waiting for
	// a buildlet, keyed by the host type they're waiting for.
	waiting map[string]map[*SchedItem]bool // hostType -> item -> true

	// hostsCreating is the number of GetBuildlet calls currently in flight
	// to each hostType's respective buildlet pool.
	hostsCreating map[string]int // hostType -> count
}

// A getBuildletResult is a buildlet that was just created and is up and
// is ready to be assigned to a caller based on priority.
type getBuildletResult struct {
	Pool     BuildletPool
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
		case waiter.res <- res.Client:
			// Normal happy case. Something gets its buildlet.
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

func (s *Scheduler) getPoolBuildlet(pool BuildletPool, hostType string) {
	res := getBuildletResult{
		Pool:     pool,
		HostType: hostType,
	}
	ctx := context.Background() // TODO: make these cancelable and cancel unneeded ones earlier?
	res.Client, res.Err = pool.GetBuildlet(ctx, hostType, stderrLogger{})
	s.matchBuildlet(res)
}

// matchWaiter returns (and removes from the waiting queue) the highest priority SchedItem
// that matches the provided host type.
func (s *Scheduler) matchWaiter(hostType string) (_ *SchedItem, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *SchedItem
	for si := range s.waiting[hostType] {
		if best == nil || schedLess(si, best) {
			best = si
		}
	}
	return best, best != nil
}

func (s *Scheduler) removeWaiter(si *SchedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.waiting[si.HostType]; m != nil {
		delete(m, si)
	}
}

func (s *Scheduler) enqueueWaiter(si *SchedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.waiting[si.HostType]; !ok {
		s.waiting[si.HostType] = make(map[*SchedItem]bool)
	}
	s.waiting[si.HostType][si] = true
	s.scheduleLocked()
}

// schedLess reports whether scheduled item ia is "less" (more
// important) than scheduled item ib.
func schedLess(ia, ib *SchedItem) bool {
	// TODO: flesh out this policy more. For now this is much
	// better than the old random policy.
	// For example, consider IsHelper? Figure out a policy.

	// Gomote is most important, then TryBots, then FIFO for
	// either Gomote/Try, else LIFO for post-submit builds.
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
	// Post-submit builds are LIFO.
	return ib.requestTime.Before(ia.requestTime)
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

	// We set in GetBuildlet:
	s           *Scheduler
	requestTime time.Time
	tryFor      string // which user. (user with 1 trybot >> user with 50 trybots)
	pool        BuildletPool
	ctxDone     <-chan struct{}
	// TODO: track the commit time of the BuilderRev, via call to maintnerd probably
	// commitTime time.Time

	// res is the result channel, containing either a
	// *buildlet.Client or an error. It is read by GetBuildlet and
	// written by assignBuildlet.
	res chan interface{}
}

func (si *SchedItem) cancel() {
	si.s.removeWaiter(si)
}

// GetBuildlet requests a buildlet with the parameters described in si.
//
// The provided si must be newly allocated; ownership passes to the scheduler.
func (s *Scheduler) GetBuildlet(ctx context.Context, lg logger, si *SchedItem) (*buildlet.Client, error) {
	pool := poolForConf(dashboard.Hosts[si.HostType])

	if !useScheduler {
		return pool.GetBuildlet(ctx, si.HostType, lg)
	}

	si.pool = pool
	si.s = s
	si.requestTime = time.Now()
	si.res = make(chan interface{}) // NOT buffered
	si.ctxDone = ctx.Done()

	// TODO: once we remove the useScheduler const, we can
	// remove the "lg" logger parameter. We don't need to
	// log anything during the buildlet creation process anymore
	// because we don't which build it'll be for. So all we can
	// say in the logs for is "Asking for a buildlet" and "Got
	// one", which the caller already does. I think. Verify that.

	s.enqueueWaiter(si)
	select {
	case v := <-si.res:
		if bc, ok := v.(*buildlet.Client); ok {
			return bc, nil
		}
		return nil, v.(error)
	case <-ctx.Done():
		si.cancel()
		return nil, ctx.Err()
	}
}
