// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build !appengine

package devapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticAssetsFound(t *testing.T) {
	req := httptest.NewRequest("GET", "/static/dash.css", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected code 200, got %d", w.Code)
	}
	if hdr := w.Header().Get("Content-Type"); hdr != "text/css; charset=utf-8" {
		t.Errorf("incorrect Content-Type header, got headers: %v", w.Header())
	}
}

func TestFaviconFound(t *testing.T) {
	req := httptest.NewRequest("GET", "/favicon.ico", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected code 200, got %d", w.Code)
	}
	if hdr := w.Header().Get("Content-Type"); hdr != "image/x-icon" {
		t.Errorf("incorrect Content-Type header, got headers: %v", w.Header())
	}
}
