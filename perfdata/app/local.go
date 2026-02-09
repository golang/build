// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !appengine

package app

import (
	"context"
	"log"
	"net/http"
)

// requestContext returns the Context object for a given request.
func requestContext(r *http.Request) context.Context {
	return r.Context()
}

func infof(_ context.Context, format string, args ...any) {
	log.Printf(format, args...)
}

var errorf = infof
