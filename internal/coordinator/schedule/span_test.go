// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package schedule

import (
	"testing"
)

type fakeEventTimeLogger struct {
	event   string
	optText []string
}

func (l *fakeEventTimeLogger) LogEventTime(event string, optText ...string) {
	l.event = event
	l.optText = optText
}

func TestSpan(t *testing.T) {
	l := &fakeEventTimeLogger{}
	event := "log_event"
	s := CreateSpan(l, event, "a", "b", "c")
	if err := s.Done(nil); err != nil {
		t.Fatalf("Span.Done() = %s; want no error", err)
	}
	if l.event != "finish_"+event {
		t.Errorf("EventTimeLogger.event = %q, want %q", l.event, "finish_"+event)
	}
	if len(l.optText) == 0 {
		t.Errorf("EventTimeLogger.optText = %+v; want entries", l.optText)
	}
}
