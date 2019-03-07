// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux

package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// proxyModuleCache proxies from https://farmer.golang.org (with a
// magic header, as handled by coordinator.go's httpRouter type) to
// Go's private module proxy server running on GKE. The module proxy
// protocol does not define authentication, so we do it ourselves.
//
// The complete path is the buildlet listens on localhost:3000 to run
// an unauthenticated module proxy server for the cmd/go binary to use
// via GOPROXY=http://localhost:3000. That localhost:3000 server
// proxies it to https://farmer.golang.org with auth headers and a
// sentinel X-Proxy-Service:module-cache header. Then coordinator.go's
// httpRouter sends it here.
//
// This code then does the final reverse proxy, sent without auth.
//
// In summary:
//
//   cmd/go -> localhost:3000 -> buildlet -> coordinator -> GKE server
func proxyModuleCache(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "https required", http.StatusBadRequest)
		return
	}
	builder, pass, ok := r.BasicAuth()
	if !ok {
		http.Error(w, "missing required authentication", http.StatusBadRequest)
		return
	}
	if !strings.Contains(builder, "-") || builderKey(builder) != pass {
		http.Error(w, "bad username or password", http.StatusUnauthorized)
		return
	}

	targetURL := moduleProxy()
	if !strings.HasPrefix(targetURL, "http") {
		log.Printf("unsupported GOPROXY backend value %q; not proxying", targetURL)
		http.Error(w, "no GOPROXY backend available", http.StatusInternalServerError)
		return
	}
	backend, err := url.Parse(targetURL)
	if err != nil {
		log.Printf("failed to parse GOPROXY value as URL: %v", err)
		http.Error(w, "module proxy misconfigured", http.StatusInternalServerError)
		return
	}
	// TODO: maybe only create this once early. But probably doesn't matter.
	rp := httputil.NewSingleHostReverseProxy(backend)
	r.Header.Del("Authorization")
	r.Header.Del("X-Proxy-Service")
	rp.ServeHTTP(w, r)
}
