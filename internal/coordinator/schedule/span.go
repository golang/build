// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package schedule

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/types"
)

type Spanner interface {
	SpanRecord(*Span, error) *types.SpanRecord
}

// Span is an event covering a region of time.
// A Span ultimately ends in an error or success, and will eventually
// be visualized and logged.
type Span struct {
	event   string // event name like "get_foo" or "write_bar"
	optText string // optional details for event
	start   time.Time
	end     time.Time
	el      pool.EventTimeLogger // where we log to at the end; TODO: this will change
}

// Event is the span's event.
func (s *Span) Event() string {
	return s.event
}

// OptText is the optional text for a span.
func (s *Span) OptText() string {
	return s.optText
}

// Start is the start time for the span.
func (s *Span) Start() time.Time {
	return s.start
}

// End is the end time for an span.
func (s *Span) End() time.Time {
	return s.end
}

// CreateSpan creates a span with the appropriate metadata. It also starts the span.
func CreateSpan(el pool.EventTimeLogger, event string, optText ...string) *Span {
	start := time.Now()
	el.LogEventTime(event, optText...)
	return &Span{
		el:      el,
		event:   event,
		start:   start,
		optText: strings.Join(optText, " "),
	}
}

// Done ends a span.
// It is legal to call Done multiple times. Only the first call
// logs.
// Done always returns its input argument.
func (s *Span) Done(err error) error {
	if !s.end.IsZero() {
		return err
	}
	t1 := time.Now()
	s.end = t1
	td := t1.Sub(s.start)
	var text bytes.Buffer
	fmt.Fprintf(&text, "after %s", friendlyDuration(td))
	if err != nil {
		fmt.Fprintf(&text, "; err=%v", err)
	}
	if s.optText != "" {
		fmt.Fprintf(&text, "; %v", s.optText)
	}
	if st, ok := s.el.(Spanner); ok {
		pool.CoordinatorProcess().PutSpanRecord(st.SpanRecord(s, err))
	}
	s.el.LogEventTime("finish_"+s.event, text.String())
	return err
}

// TODO: This is a copy of the function in cmd/coordinator/status.go. This should be removed once status
// is moved into it's own package.
func friendlyDuration(d time.Duration) string {
	if d > 10*time.Second {
		d2 := ((d + 50*time.Millisecond) / (100 * time.Millisecond)) * (100 * time.Millisecond)
		return d2.String()
	}
	if d > time.Second {
		d2 := ((d + 5*time.Millisecond) / (10 * time.Millisecond)) * (10 * time.Millisecond)
		return d2.String()
	}
	d2 := ((d + 50*time.Microsecond) / (100 * time.Microsecond)) * (100 * time.Microsecond)
	return d2.String()
}
