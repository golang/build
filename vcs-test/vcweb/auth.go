// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	pathpkg "path"
	"strings"
)

type accessToken struct {
	Username, Password string
	StatusCode         int // defaults to 401.
	Message            string
}

// newAuthHandler serves authenticated data from within dir.
//
// For each request, the handler looks for a file in a parent directory (still
// within dir) named ".access" and parses it as a JSON-serialized accessToken.
// If the credentials from the request match the accessToken, the file is served
// normally; otherwise, it is rejected with the StatusCode and Message provided
// by the token.
func newAuthHandler(dir http.FileSystem) http.Handler {
	return &authHandler{dir}
}

type authHandler struct {
	dir http.FileSystem
}

func (h *authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/auth/") {
		http.Error(w, "path does not start with /auth/", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/auth/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(pathpkg.Base(path), ".") {
		http.Error(w, "filename contains leading dot", http.StatusBadRequest)
		return
	}

	f, err := h.dir.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	accessDir := path
	if fi, err := f.Stat(); err == nil && !fi.IsDir() {
		accessDir = pathpkg.Dir(path)
	}
	f.Close()

	var accessFile http.File
	for {
		var err error
		accessFile, err = h.dir.Open(pathpkg.Join(accessDir, ".access"))
		if err == nil {
			break
		}

		if !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if accessDir == "." {
			http.Error(w, "failed to locate access file", http.StatusInternalServerError)
			return
		}
		accessDir = pathpkg.Dir(accessDir)
	}

	data, err := ioutil.ReadAll(accessFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var token accessToken
	if err := json.Unmarshal(data, &token); err != nil {
		log.Print(err)
		http.Error(w, "malformed access file", http.StatusInternalServerError)
		return
	}
	if username, password, ok := r.BasicAuth(); !ok || username != token.Username || password != token.Password {
		code := token.StatusCode
		if code == 0 {
			code = http.StatusUnauthorized
		}
		if code == http.StatusUnauthorized {
			w.Header().Add("WWW-Authenticate", fmt.Sprintf("basic realm=%s", accessDir))
		}
		http.Error(w, token.Message, code)
		return
	}

	http.StripPrefix("/auth/", http.FileServer(h.dir)).ServeHTTP(w, r)
}
