// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package queue

import (
	"container/heap"
	"context"
	"sort"
	"sync"
)

// NewQuota returns an initialized *Quota ready for use.
func NewQuota() *Quota {
	return &Quota{
		queue: new(buildletQueue),
	}
}

// Quota manages a queue for a single quota.
type Quota struct {
	mu    sync.Mutex
	queue *buildletQueue
	limit int
	used  int
	// On GCE, other instances run in the same project as buildlet
	// instances. Track those separately, and subtract from available.
	untrackedUsed int
}

func (q *Quota) push(item *Item) {
	defer q.updated()
	q.mu.Lock()
	defer q.mu.Unlock()
	heap.Push(q.queue, item)
}

func (q *Quota) cancel(item *Item) {
	defer q.updated()
	q.mu.Lock()
	defer q.mu.Unlock()
	if item.index != -1 {
		heap.Remove(q.queue, item.index)
	}
}

func (q *Quota) updated() {
	for {
		if q.tryPop() == nil {
			return
		}
	}
}

// tryPop returns a Item if quota is available and unblocks the
// AwaitQueue call.
func (q *Quota) tryPop() *Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !(q.queue.Len() != 0 && q.queue.Peek().cost <= q.limit-q.used-q.untrackedUsed) {
		return nil
	}
	b := q.queue.PopBuildlet()
	q.used += b.cost
	b.ready()
	return b
}

// Empty returns true when there are no items in the queue.
func (q *Quota) Empty() bool {
	return q.Len() == 0
}

// Len returns the number of items in the queue.
func (q *Quota) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.queue.Len()
}

// UpdateQuotas updates the limit and used values on the queue.
func (q *Quota) UpdateQuotas(used, limit int) {
	defer q.updated()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limit = limit
	q.used = used
}

// UpdateLimit updates the limit values on the queue.
func (q *Quota) UpdateLimit(limit int) {
	defer q.updated()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.limit = limit
}

func (q *Quota) UpdateUntracked(n int) {
	defer q.updated()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.untrackedUsed = n
}

// ReturnQuota decrements the used quota value by v.
func (q *Quota) ReturnQuota(v int) {
	defer q.updated()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used -= v
}

type Usage struct {
	Used          int
	Limit         int
	UntrackedUsed int
}

// Quotas returns the used, limit, and untracked values for the queue.
func (q *Quota) Quotas() Usage {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Usage{
		Used:          q.used,
		Limit:         q.limit,
		UntrackedUsed: q.untrackedUsed,
	}
}

// Enqueue a build and return an Item. See Item's documentation for
// waiting and releasing quota.
func (q *Quota) Enqueue(cost int, si *SchedItem) *Item {
	item := &Item{
		cost:    cost,
		release: func() { q.ReturnQuota(cost) },
		popped:  make(chan struct{}),
		build:   si,
	}
	item.cancel = func() { q.cancel(item) }
	q.push(item)
	return item
}

// AwaitQueue enqueues a build and returns once the item is unblocked
// by quota, by order of minimum priority.
//
// If the provided context is cancelled before popping, the item is
// removed from the queue and an error is returned.
func (q *Quota) AwaitQueue(ctx context.Context, cost int, si *SchedItem) error {
	if err := ctx.Err(); err != nil {
		return ctx.Err()
	}
	return q.Enqueue(cost, si).Await(ctx)
}

type QuotaStats struct {
	Usage
	Items []ItemStats
}

type ItemStats struct {
	Build *SchedItem
	Cost  int
}

func (q *Quota) ToExported() *QuotaStats {
	q.mu.Lock()
	qs := &QuotaStats{
		Usage: Usage{
			Used:          q.used,
			Limit:         q.limit,
			UntrackedUsed: q.untrackedUsed,
		},
		Items: make([]ItemStats, q.queue.Len()),
	}
	for i, item := range *q.queue {
		qs.Items[i].Build = item.SchedItem()
		qs.Items[i].Cost = item.cost
	}
	q.mu.Unlock()

	sort.Slice(qs.Items, func(i, j int) bool {
		return qs.Items[i].Build.Less(qs.Items[j].Build)
	})
	return qs
}

// An Item is something we manage in a priority buildletQueue.
type Item struct {
	build   *SchedItem
	cancel  func()
	cost    int
	popped  chan struct{}
	release func()
	// index is maintained by the heap.Interface methods.
	index int
}

// SchedItem returns a copy of the SchedItem for a build.
func (i *Item) SchedItem() *SchedItem {
	build := *i.build
	return &build
}

// Await blocks until the Item holds the necessary quota amount, or the
// context is cancelled.
//
// On success, the caller must call ReturnQuota() to release the quota.
func (i *Item) Await(ctx context.Context) error {
	if ctx.Err() != nil {
		i.cancel()
		i.ReturnQuota()
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		i.cancel()
		i.ReturnQuota()
		return ctx.Err()
	case <-i.popped:
		return nil
	}
}

// ReturnQuota returns quota to the Queue. ReturnQuota is a no-op if
// the item has never been popped.
func (i *Item) ReturnQuota() {
	select {
	case <-i.popped:
		i.release()
	default:
		// We haven't been popped yet, nothing to release.
		return
	}
}

func (i *Item) ready() {
	close(i.popped)
}

// A buildletQueue implements heap.Interface and holds Items.
type buildletQueue []*Item

func (q buildletQueue) Len() int { return len(q) }

func (q buildletQueue) Less(i, j int) bool {
	return q[i].build.Less(q[j].build)
}

func (q buildletQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *buildletQueue) Push(x interface{}) {
	n := len(*q)
	item := x.(*Item)
	item.index = n
	*q = append(*q, item)
}

func (q *buildletQueue) Pop() interface{} {
	old := *q
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // necessary to avoid races in (*Queue).cancel().
	*q = old[0 : n-1]
	return item
}

func (q *buildletQueue) PopBuildlet() *Item {
	return heap.Pop(q).(*Item)
}

func (q buildletQueue) Peek() *Item {
	return q[0]
}
