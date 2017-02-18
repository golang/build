// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHomepage(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handleRoot(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /: want code 200, got %d", w.Code)
	}
	if hdr := w.Header().Get("Content-Type"); !strings.Contains(hdr, "text/html") {
		t.Fatalf("GET /: want html content-type, got %s", hdr)
	}
}

func TestDebugWatcher(t *testing.T) {
	r := &Repo{path: "build"}
	r.setStatus("waiting")
	req := httptest.NewRequest("GET", "/debug/watcher/build", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /: want code 200, got %d", w.Code)
	}
	body := w.Body.String()
	if substr := `watcher status for repo: "build"`; !strings.Contains(body, substr) {
		t.Fatalf("GET /debug/watcher/build: want %q in body, got %s", substr, body)
	}
	if substr := "waiting"; !strings.Contains(body, substr) {
		t.Fatalf("GET /debug/watcher/build: want %q in body, got %s", substr, body)
	}
}
