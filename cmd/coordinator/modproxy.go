// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux darwin

package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

var proxyGolangOrg *httputil.ReverseProxy // initialized just below

func init() {
	u, err := url.Parse("https://proxy.golang.org")
	if err != nil {
		log.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.ModifyResponse = func(res *http.Response) error {
		r := res.Request
		if res.StatusCode/100 != 2 && res.StatusCode != 410 && r != nil {
			log.Printf("modproxy: proxying HTTP %s response from backend for %s, %s %s", res.Status, r.RemoteAddr, r.Method, r.RequestURI)
		}
		return nil
	}
	proxyGolangOrg = rp
}

func listenAndServeInternalModuleProxy() {
	err := http.ListenAndServe(":8123", http.HandlerFunc(proxyModuleCache))
	log.Fatalf("error running internal module proxy: %v", err)
}

// proxyModuleCache proxies requests to https://proxy.golang.org/
func proxyModuleCache(w http.ResponseWriter, r *http.Request) {
	// Delete any Host header so it's the one sent to the backend
	// is proxy.golang.org by the default ReverseProxy.Director
	// setting the URL.Host. (Host takes priority for Host header,
	// if present)
	r.Host = ""
	proxyGolangOrg.ServeHTTP(w, r)
}
