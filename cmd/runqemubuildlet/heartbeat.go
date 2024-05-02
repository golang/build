// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/build/internal"
)

// buildletHealthTimeout is the maximum time to wait for a
// checkBuildletHealth request to complete.
const buildletHealthTimeout = 10 * time.Second

// checkBuildletHealth performs a GET request against URL, and returns
// an error if an http.StatusOK isn't returned before
// buildletHealthTimeout has elapsed.
func checkBuildletHealth(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, buildletHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
	return nil
}

// heartbeatContext calls f every period. If f consistently returns an
// error for longer than the provided timeout duration, the context
// returned by heartbeatContext will be cancelled, and
// heartbeatContext will stop sending requests.
//
// A single call to f that does not return an error will reset the
// timeout window, unless heartbeatContext has already timed out.
func heartbeatContext(ctx context.Context, period time.Duration, timeout time.Duration, f func(context.Context) error) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)

	lastSuccess := time.Now()
	go internal.PeriodicallyDo(ctx, period, func(ctx context.Context, t time.Time) {
		err := f(ctx)
		if err != nil && t.Sub(lastSuccess) > timeout {
			cancel()
		}
		if err == nil {
			lastSuccess = t
		}
	})

	return ctx, cancel
}
