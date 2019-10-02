// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProxyURL tests that the response served from proxyURL is not a
// redirect, even if its backend URL serves a redirect. That is, our
// proxy should do the redirect following, not defer that to the
// client.
func TestProxyURL(t *testing.T) {
	const content = "some content"
	const header = "X-Some-Header"
	tsTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/bar" {
			w.Header().Set(header, content)
			io.WriteString(w, content)
		}
	}))
	defer tsTarget.Close()
	tsBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/foo" {
			if v := r.Header.Get(header); v != content {
				t.Errorf("header(%q) = %q in handler; want %q", header, v, content)
				w.WriteHeader(400)
				return
			}
			http.Redirect(w, r, tsTarget.URL+"/bar", http.StatusFound)
			return
		}
		w.WriteHeader(400)
	}))
	defer tsBackend.Close()

	req := httptest.NewRequest("GET", "/foo", nil)
	req.Header.Set(header, content)
	rr := httptest.NewRecorder()
	proxyURL(rr, req, tsBackend.URL)
	got := rr.Result()
	gotBody := rr.Body.String()
	if got.StatusCode != 200 {
		t.Errorf("status = %q; want 200", got.StatusCode)
	}
	if gotBody != content {
		t.Errorf("content = %q; want %q", gotBody, content)
	}
	if h := got.Header.Get(header); h != content {
		t.Errorf("header(%q) = %q; want %q", header, h, content)
	}
}
