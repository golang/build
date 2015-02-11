// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gerrit

import "net/http"

// Auth is a Gerrit authentication mode.
// The most common ones are NoAuth or BasicAuth.
type Auth interface {
	setAuth(*Client, *http.Request)
}

// BasicAuth sends a username and password.
func BasicAuth(username, password string) Auth {
	return basicAuth{username, password}
}

// TODO(bradfitz): add a GitCookies auth mode, where it's automatic
// from the url string given to the client.

type basicAuth struct {
	username, password string
}

func (ba basicAuth) setAuth(c *Client, r *http.Request) {
	r.SetBasicAuth(ba.username, ba.password)
}

// NoAuth makes requests unauthenticated.
var NoAuth = noAuth{}

type noAuth struct{}

func (noAuth) setAuth(c *Client, r *http.Request) {}
