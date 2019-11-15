// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"net/http/httptest"
	"regexp"
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

func TestStatusSched(t *testing.T) {
	data := statusData{
		SchedState: schedulerState{
			HostTypes: []schedulerHostState{
				{
					HostType:     "no-special",
					LastProgress: 5 * time.Minute,
					Total:        schedulerWaitingState{Count: 10, Newest: 5 * time.Minute, Oldest: 61 * time.Minute},
					Regular:      schedulerWaitingState{Count: 1},
				},
				{
					HostType:     "with-try",
					LastProgress: 5 * time.Minute,
					Total:        schedulerWaitingState{Count: 3},
					Try:          schedulerWaitingState{Count: 1, Newest: 2 * time.Second, Oldest: 5 * time.Minute},
					Regular:      schedulerWaitingState{Count: 2},
				},
				{
					HostType: "gomote-and-try",
					Total:    schedulerWaitingState{Count: 6, Newest: 3 * time.Second, Oldest: 4 * time.Minute},
					Gomote:   schedulerWaitingState{Count: 2, Newest: 3 * time.Second, Oldest: 4 * time.Minute},
					Try:      schedulerWaitingState{Count: 1},
					Regular:  schedulerWaitingState{Count: 3},
				},
			},
		},
	}
	buf := new(bytes.Buffer)
	if err := statusTmpl.Execute(buf, data); err != nil {
		t.Fatal(err)
	}

	wantMatch := []string{
		`(?s)<li><b>no-special</b>: 10 waiting \(oldest 1h1m0s, newest 5m0s, progress 5m0s\)\s+</li>`,
		`<li>try: 1 \(oldest 5m0s, newest 2s\)</li>`,
		`(?s)<li><b>gomote-and-try</b>: 6 waiting \(oldest 4m0s, newest 3s\)`, // checks for no ", progress"
		`<li>gomote: 2 \(oldest 4m0s, newest 3s\)</li>`,
	}
	for _, rx := range wantMatch {
		matched, err := regexp.Match(rx, buf.Bytes())
		if err != nil {
			t.Errorf("error matching %#q: %v", rx, err)
			continue
		}
		if !matched {
			t.Errorf("didn't match %#q", rx)
		}
	}
	if t.Failed() {
		t.Logf("Got: %s", section(buf.Bytes(), "sched"))
	}
}

// section returns the section of the status HTML page that starts
// with <h2 id=$section> and ends with any other ^<h2 line.
func section(in []byte, section string) []byte {
	start := "<h2 id=" + section + ">"
	bs := bufio.NewScanner(bytes.NewReader(in))
	var out bytes.Buffer
	var foundStart bool
	for bs.Scan() {
		if foundStart {
			if strings.HasPrefix(bs.Text(), "<h2") {
				break
			}
		} else {
			if strings.HasPrefix(bs.Text(), start) {
				foundStart = true
			} else {
				continue
			}
		}
		out.Write(bs.Bytes())
		out.WriteByte('\n')
	}
	return out.Bytes()
}
