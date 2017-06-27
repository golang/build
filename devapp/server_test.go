// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

var testServer = newServer(http.DefaultServeMux, "./static/")

func TestStaticAssetsFound(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	testServer.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected code %d, got %d", http.StatusOK, w.Code)
	}
	if hdr := w.Header().Get("Content-Type"); hdr != "text/html; charset=utf-8" {
		t.Errorf("incorrect Content-Type header, got headers: %v", w.Header())
	}
}

func TestFaviconFound(t *testing.T) {
	req := httptest.NewRequest("GET", "/favicon.ico", nil)
	w := httptest.NewRecorder()
	testServer.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected code %d, got %d", http.StatusOK, w.Code)
	}
	if hdr := w.Header().Get("Content-Type"); hdr != "image/x-icon" {
		t.Errorf("incorrect Content-Type header, got headers: %v", w.Header())
	}
}

func TestHSTSHeaderSet(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	testServer.ServeHTTP(w, req)
	if hdr := w.Header().Get("Strict-Transport-Security"); hdr == "" {
		t.Errorf("missing Strict-Transport-Security header; headers = %v", w.Header())
	}
}
