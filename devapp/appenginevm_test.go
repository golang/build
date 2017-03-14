// Copyright 2017 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build appenginevm

package devapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetTokenMethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/setToken", nil)
	http.DefaultServeMux.ServeHTTP(w, req)
	if w.Code != 405 {
		t.Errorf("GET /setToken: got %d, want 405", w.Code)
	}
}
