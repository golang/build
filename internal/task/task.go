// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package task implements tasks involved in making a Go release.
package task

import (
	"time"

	"golang.org/x/build/internal/workflow"
)

// CommunicationTasks combines communication tasks together.
type CommunicationTasks struct {
	AnnounceMailTasks
	TweetTasks
}

var AwaitDivisor int = 1

// AwaitCondition calls the condition function every period until it returns
// true to indicate success, or an error. If the condition succeeds,
// AwaitCondition returns its result.
func AwaitCondition[T any](ctx *workflow.TaskContext, period time.Duration, condition func() (T, bool, error)) (T, error) {
	pollTimer := time.NewTicker(period / time.Duration(AwaitDivisor))
	defer pollTimer.Stop()
	heartbeatTimer := time.NewTicker(time.Minute)
	defer heartbeatTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-heartbeatTimer.C:
			// TODO: reset watchdog
		case <-pollTimer.C:
			res, done, err := condition()
			if done || err != nil {
				return res, err
			}
		}
	}
}
