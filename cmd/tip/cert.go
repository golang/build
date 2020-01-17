// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"strings"

	"cloud.google.com/go/storage"
	"golang.org/x/build/autocertcache"
	"golang.org/x/crypto/acme/autocert"
)

var autocertManager *autocert.Manager

func certInit() {
	var cache autocert.Cache
	if b := *autoCertCacheBucket; b != "" {
		sc, err := storage.NewClient(context.Background())
		if err != nil {
			log.Fatalf("storage.NewClient: %v", err)
		}
		cache = autocertcache.NewGoogleCloudStorageCache(sc, b)
	}
	autocertManager = &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(strings.Split(*autoCertDomain, ",")...),
		Cache:      cache,
	}
}

func runHTTPS(h http.Handler) error {
	s := &http.Server{
		Addr:    ":https",
		Handler: h,
		TLSConfig: &tls.Config{
			GetCertificate: autocertManager.GetCertificate,
		},
	}
	return s.ListenAndServeTLS("", "")
}

func wrapHTTPMux(h http.Handler) http.Handler {
	return autocertManager.HTTPHandler(h)
}
