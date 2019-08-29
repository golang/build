// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var durationTests = []struct {
	in   time.Duration
	want string
}{
	{10*time.Second + 555*time.Millisecond, "10.6s"},
	{10*time.Second + 500*time.Millisecond, "10.5s"},
	{10*time.Second + 499*time.Millisecond, "10.5s"},
	{10*time.Second + 401*time.Millisecond, "10.4s"},
	{9*time.Second + 401*time.Millisecond, "9.4s"},
	{9*time.Second + 456*time.Millisecond, "9.46s"},
	{9*time.Second + 445*time.Millisecond, "9.45s"},
	{1 * time.Second, "1s"},
	{859*time.Millisecond + 445*time.Microsecond, "859.4ms"},
	{859*time.Millisecond + 460*time.Microsecond, "859.5ms"},
}

func TestFriendlyDuration(t *testing.T) {
	for _, tt := range durationTests {
		got := friendlyDuration(tt.in)
		if got != tt.want {
			t.Errorf("friendlyDuration(%v): got %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestHandleStatus_HealthFormatting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addHealthCheckers(ctx)
	addHealthChecker(&healthChecker{
		ID:    "allgood",
		Title: "All Good Test",
		Check: func(*checkWriter) {},
	})
	addHealthChecker(&healthChecker{
		ID:    "errortest",
		Title: "Error Test",
		Check: func(cw *checkWriter) {
			cw.info("test-info")
			cw.warn("test-warn")
			cw.error("test-error")
		},
	})

	statusMu.Lock()
	for k := range status {
		delete(status, k)
	}
	for k := range tries {
		delete(tries, k)
	}
	tryList = nil
	statusMu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	handleStatus(rec, req)
	const pre = "<h2 id=health>Health"
	const suf = "<h2 id=trybots>Active Trybot Runs"
	got := rec.Body.String()
	if i := strings.Index(got, pre); i != -1 {
		got = got[i+len(pre):]
	} else {
		t.Fatalf("output didn't contain %q: %s", pre, got)
	}
	if i := strings.Index(got, suf); i != -1 {
		got = got[:i]
	} else {
		t.Fatalf("output didn't contain %q: %s", suf, got)
	}
	for _, sub := range []string{
		`<a href="/status/macs">MacStadium Mac VMs</a> [`,
		`<a href="/status/scaleway">Scaleway linux/arm machines</a> [`,
		`<li>scaleway-prod-02 not yet connected</li>`,
		`<li>macstadium_host06a not yet connected</li>`,
		`<a href="/status/allgood">All Good Test</a>: ok`,
		`<li>test-info</li>`,
		`<li><span style='color: orange'>test-warn</span></li>`,
		`<li><span style='color: red'><b>test-error</b></span></li>`,
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("didn't find substring %q in output", sub)
		}
	}
	if t.Failed() {
		t.Logf("Got: %s", got)
	}
}
