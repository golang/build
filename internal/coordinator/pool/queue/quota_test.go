// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestQueueEmpty(t *testing.T) {
	q := NewQuota()
	if !q.Empty() {
		t.Errorf("q.Empty() = %t, wanted %t", q.Empty(), true)
	}
	q.Enqueue(100, new(SchedItem))
	if q.Empty() {
		t.Errorf("q.Empty() = %t, wanted %t", q.Empty(), false)
	}
}

func TestQueueReturnQuotas(t *testing.T) {
	q := NewQuota()
	q.UpdateQuotas(7, 15)
	q.ReturnQuota(3)
	usage := q.Quotas()
	if !(usage.Used == 4 && usage.Limit == 15) {
		t.Errorf("q.Quotas() = %d, %d, wanted %d, %d", usage.Used, usage.Limit, 10, 15)
	}
}

func TestQueue(t *testing.T) {
	q := NewQuota()
	q.UpdateQuotas(14, 16)
	q.UpdateUntracked(1)
	item := q.Enqueue(4, new(SchedItem))

	if q.Empty() {
		t.Errorf("q.Empty() = %v, wanted %v", q.Empty(), false)
	}

	want := Usage{
		Used:          14,
		Limit:         16,
		UntrackedUsed: 1,
	}
	got := q.Quotas()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("q.ToExported() mismatch (-want +got):\n%s", diff)
	}

	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		done <- item.Await(ctx)
	}()

	q.ReturnQuota(14)

	if !q.Empty() {
		t.Errorf("q.Empty() = %v, wanted %v", q.Empty(), true)
	}

	want = Usage{
		Used:          4,
		Limit:         16,
		UntrackedUsed: 1,
	}
	got = q.Quotas()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("q.ToExported() mismatch (-want +got):\n%s", diff)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("item.Await() = %v, wanted no error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("item.Await() never returned, wanted return after q.tryPop()")
	}

	item.ReturnQuota()

	if !q.Empty() {
		t.Errorf("q.Empty() = %v, wanted %v", q.Empty(), true)
	}
	want = Usage{
		Used:          0,
		Limit:         16,
		UntrackedUsed: 1,
	}
	got = q.Quotas()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("q.ToExported() mismatch (-want +got):\n%s", diff)
	}
}

func TestQueueUpdatedMany(t *testing.T) {
	q := NewQuota()
	ctx := context.Background()
	items := []*Item{
		q.Enqueue(3, &SchedItem{IsGomote: true}),
		q.Enqueue(1, new(SchedItem)),
		q.Enqueue(1, new(SchedItem)),
	}

	var wg sync.WaitGroup
	wg.Add(3)
	done := make(chan error, 3)
	for _, item := range items {
		go func(item *Item) {
			done <- item.Await(ctx)
			item.ReturnQuota()
			wg.Done()
		}(item)
	}
	if q.Len() != 3 {
		t.Errorf("q.Len() = %d, wanted %d", q.Len(), 3)
	}
	q.UpdateLimit(3)
	for range items {
		<-done
	}
	if !q.Empty() {
		t.Errorf("q.Empty() = %t, wanted %t", q.Empty(), true)
	}
	wg.Wait()

	if !q.Empty() {
		t.Errorf("q.Empty() = %v, wanted %v", q.Empty(), true)
	}
	usage := q.Quotas()
	if !(usage.Used == 0 && usage.Limit == 3) {
		t.Errorf("q.Quotas() = %d, %d, wanted %d, %d", usage.Used, usage.Limit, 0, 3)
	}
}

func TestQueueCancel(t *testing.T) {
	q := NewQuota()
	q.UpdateQuotas(0, 15)
	ctx, cancel := context.WithCancel(context.Background())

	enqueued := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- q.AwaitQueue(ctx, 100, new(SchedItem))
	}()
	go func() {
		for q.Empty() {
			time.Sleep(100 * time.Millisecond)
		}
		close(enqueued)
	}()
	select {
	case <-time.After(time.Second):
		t.Fatal("q.AwaitQueue() never called, wanted one call")
	case <-enqueued:
		// success.
	}
	if q.Empty() {
		t.Errorf("q.Empty() = %v, wanted %v", q.Empty(), false)
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("q.AwaitQueue() = %v, wanted error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("q.AwaitQueue() never returned, wanted return after cancel()")
	}

	b := q.tryPop()
	if b != nil {
		t.Errorf("q.tryPop() = %v, wanted %v", b, nil)
	}
	if !q.Empty() {
		t.Errorf("q.Empty() = %v, wanted %v", q.Empty(), true)
	}
	usage := q.Quotas()
	if !(usage.Used == 0 && usage.Limit == 15) {
		t.Errorf("q.Quotas() = %d, %d, wanted %d, %d", usage.Used, usage.Limit, 0, 15)
	}
}

func TestQueueToExported(t *testing.T) {
	q := NewQuota()
	q.UpdateLimit(10)
	q.Enqueue(100, &SchedItem{IsTry: true})
	q.Enqueue(100, &SchedItem{IsTry: true})
	q.Enqueue(100, &SchedItem{IsTry: true})
	q.Enqueue(100, &SchedItem{IsGomote: true})
	q.Enqueue(100, &SchedItem{IsGomote: true})
	q.Enqueue(100, &SchedItem{IsGomote: true})
	q.Enqueue(100, &SchedItem{IsRelease: true})
	want := &QuotaStats{
		Usage: Usage{
			Limit: 10,
		},
		Items: []ItemStats{
			{Build: &SchedItem{IsRelease: true}, Cost: 100},
			{Build: &SchedItem{IsGomote: true}, Cost: 100},
			{Build: &SchedItem{IsGomote: true}, Cost: 100},
			{Build: &SchedItem{IsGomote: true}, Cost: 100},
			{Build: &SchedItem{IsTry: true}, Cost: 100},
			{Build: &SchedItem{IsTry: true}, Cost: 100},
			{Build: &SchedItem{IsTry: true}, Cost: 100},
		},
	}
	got := q.ToExported()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("q.ToExported() mismatch (-want +got):\n%s", diff)
	}
}
