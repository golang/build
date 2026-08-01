// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetRetryAfter(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer s.Close()

	_, err := Get(nil, s.URL)
	var e tooManyRequestsError
	if !errors.As(err, &e) {
		t.Fatalf("Get error = %v, want tooManyRequestsError", err)
	}
	if e.RetryAfter != 2*time.Second {
		t.Fatalf("RetryAfter = %v, want 2s", e.RetryAfter)
	}
}
