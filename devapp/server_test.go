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

func TestRandomHelpWantedIssue(t *testing.T) {
	req := httptest.NewRequest("GET", "/imfeelinglucky", nil)
	w := httptest.NewRecorder()
	testServer.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("w.Code = %d; want %d", w.Code, http.StatusSeeOther)
	}
	if g, w := w.Header().Get("Location"), issuesURLBase; g != w {
		t.Errorf("Location header = %q; want %q", g, w)
	}

	testServer.cMu.Lock()
	testServer.helpWantedIssues = []int32{42}
	testServer.cMu.Unlock()
	w = httptest.NewRecorder()
	testServer.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("w.Code = %d; want %d", w.Code, http.StatusSeeOther)
	}
	if g, w := w.Header().Get("Location"), issuesURLBase+"42"; g != w {
		t.Errorf("Location header = %q; want %q", g, w)
	}
}

func TestIntFromStr(t *testing.T) {
	testcases := []struct {
		s string
		i int
	}{
		{"123", 123},
		{"User ID: 98403", 98403},
		{"1234 User 5431 ID", 5431},
		{"Stardate 153.2415", 2415},
	}
	for _, tc := range testcases {
		r, ok := intFromStr(tc.s)
		if !ok {
			t.Errorf("intFromStr(%q) = %v", tc.s, ok)
		}
		if r != tc.i {
			t.Errorf("intFromStr(%q) = %d; want %d", tc.s, r, tc.i)
		}
	}
	noInt := "hello there"
	r, ok := intFromStr(noInt)
	if ok {
		t.Errorf("intFromStr(%q) = %v; want false", noInt, ok)
	}
}
