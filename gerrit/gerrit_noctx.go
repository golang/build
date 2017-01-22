// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build !go1.7

package gerrit

import (
	"net/http"

	"golang.org/x/net/context"
)

func withContext(r *http.Request, ctx context.Context) *http.Request {
	r.Cancel = ctx.Done()
	return r
}
