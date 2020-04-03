// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pool

import "golang.org/x/build/internal/spanlog"

// EventTimeLogger is the logging interface used to log
// an event at a point in time.
type EventTimeLogger interface {
	LogEventTime(event string, optText ...string)
}

// Logger is the logging interface used within the coordinator.
// It can both log a message at a point in time, as well
// as log a span (something having a start and end time, as well as
// a final success status).
type Logger interface {
	EventTimeLogger // point in time
	spanlog.Logger  // action spanning time
}
