// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux

package main

import (
	"net/http"
	"strings"
)

func insecureRedirectHandler() http.Handler {
	return http.HandlerFunc(insecureRedirectDispatch)
}

func insecureRedirectDispatch(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/insecure/") {
		http.Error(w, "path does not start with /insecure/", http.StatusInternalServerError)
		return
	}

	url := *r.URL
	url.Scheme = "http" // not "https"
	if url.Host == "" {
		url.Host = r.Host
	}
	url.Path = strings.TrimPrefix(url.Path, "/insecure")
	http.Redirect(w, r, url.String(), http.StatusMovedPermanently)
}
