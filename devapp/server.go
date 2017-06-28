// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"
	"net/http"
	"path"
	"sync"
)

// A server is an http.Handler that serves content within staticDir at root and
// the dynamically-generated dashboards at their respective endpoints.
type server struct {
	mux       *http.ServeMux
	staticDir string
}

func newServer(mux *http.ServeMux, staticDir string) *server {
	s := &server{
		mux:       mux,
		staticDir: staticDir,
	}
	s.mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
	s.mux.HandleFunc("/favicon.ico", s.handleFavicon)
	s.mux.HandleFunc("/release", handleRelease)
	return s
}

func (s *server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	// Need to specify content type for consistent tests, without this it's
	// determined from mime.types on the box the test is running on
	w.Header().Set("Content-Type", "image/x-icon")
	http.ServeFile(w, r, path.Join(s.staticDir, "/favicon.ico"))
}

// ServeHTTP satisfies the http.Handler interface.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS != nil {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; preload")
	}
	s.mux.ServeHTTP(w, r)
}

var (
	pageStoreMu sync.Mutex
	pageStore   = map[string][]byte{}
)

func getPage(name string) ([]byte, error) {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	p, ok := pageStore[name]
	if ok {
		return p, nil
	}
	return nil, fmt.Errorf("page key %s not found", name)
}

func writePage(key string, content []byte) error {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	pageStore[key] = content
	return nil
}

func servePage(w http.ResponseWriter, r *http.Request, key string) {
	b, err := getPage(key)
	if err != nil {
		log.Printf("getPage(%q) = %v", key, err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func handleRelease(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, "release")
}
