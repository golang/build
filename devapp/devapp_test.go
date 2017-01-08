// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package devapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHSTSHeaderSetDash(t *testing.T) {
	req := httptest.NewRequest("GET", "/dash", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, req)
	if hdr := w.Header().Get("Strict-Transport-Security"); hdr == "" {
		t.Errorf("missing Strict-Transport-Security header; headers = %v", w.Header())
	}
}

func TestReleaseReturns(t *testing.T) {
	req := httptest.NewRequest("GET", "/dash", nil)
	w := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(w, req)
	// This shouldn't panic. TODO add a better assertion.
}
