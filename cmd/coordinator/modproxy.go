// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// proxyModuleCache proxies from https://farmer.golang.org (with Auth
// & a magic header, as handled by coordinator.go's httpRouter type)
// to Go's private module proxy server running on GKE. The module proxy protocol
// does not define authentication, so we do it ourselves.
//
// The complete path is the buildlet listens on localhost:3000 to run
// an unauthenticated module proxy server for the cmd/go binary to use
// via GOPROXY=http://localhost:3000. That localhost:3000 server
// proxies it to https://farmer.golang.org with auth headers and a
// sentinel X-Proxy-Service:module-cache header. Then coordinator.go's
// httpRouter sends it here after the auth has been checked.
//
// This code then does the final reverse proxy, sent without auth.
//
// In summary:
//
//   cmd/go -> localhost:3000 -> buildlet -> coordinator --> GKE server
func proxyModuleCache(w http.ResponseWriter, r *http.Request) {
	target := moduleProxy()
	if !strings.HasPrefix(target, "http") {
		http.Error(w, "module proxy not configured", 500)
		return
	}
	backend, err := url.Parse(target)
	if err != nil {
		http.Error(w, "module proxy misconfigured", 500)
		return
	}
	// TODO: maybe only create this once early. But probably doesn't matter.
	rp := httputil.NewSingleHostReverseProxy(backend)
	r.Header.Del("Authorization")
	r.Header.Del("X-Proxy-Service")
	rp.ServeHTTP(w, r)
}
