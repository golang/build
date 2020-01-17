// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"io"
	"log"
	"net/http"
)

func listenAndServeInternalModuleProxy() {
	err := http.ListenAndServe(":8123", http.HandlerFunc(proxyModuleCache))
	log.Fatalf("error running internal module proxy: %v", err)
}

// proxyModuleCache proxies requests to https://proxy.golang.org/
func proxyModuleCache(w http.ResponseWriter, r *http.Request) {
	proxyURL(w, r, "https://proxy.golang.org")
}

func proxyURL(w http.ResponseWriter, r *http.Request, baseURL string) {
	outReq, err := http.NewRequest("GET", baseURL+r.RequestURI, nil)
	if err != nil {
		http.Error(w, "invalid URL", http.StatusBadRequest)
		return
	}
	outReq = outReq.WithContext(r.Context())
	outReq.Header = r.Header.Clone()
	res, err := http.DefaultClient.Do(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()
	for k, vv := range res.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	if res.StatusCode/100 != 2 && res.StatusCode != 410 {
		log.Printf("modproxy: proxying HTTP %s response from backend for %s, %s %s", res.Status, r.RemoteAddr, r.Method, r.RequestURI)
	}

	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
}
