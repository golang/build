// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package internal

import (
	"context"
	"time"
)

// PeriodicallyDo calls f every period until the provided context is cancelled.
func PeriodicallyDo(ctx context.Context, period time.Duration, f func(context.Context, time.Time)) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			f(ctx, now)
		}
	}
}
