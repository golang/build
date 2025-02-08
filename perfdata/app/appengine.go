// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build appengine

package app

import (
	"context"
	"net/http"

	"google.golang.org/appengine/v2"
	"google.golang.org/appengine/v2/log"
)

// requestContext returns the Context object for a given request.
func requestContext(r *http.Request) context.Context {
	return appengine.NewContext(r)
}

var infof = log.Infof
var errorf = log.Errorf
