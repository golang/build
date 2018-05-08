// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package owners

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type Owner struct {
	GitHubUsername string `json:"githubUsername"`
	GerritEmail    string `json:"gerritEmail"`
}

type Entry struct {
	Primary   []Owner `json:"primary"`
	Secondary []Owner `json:"secondary,omitempty"`
}

type Request struct {
	Payload struct {
		Paths []string `json:"paths"`
	} `json:"payload"`
	Version int `json:"v"` // API version
}

type Response struct {
	Payload struct {
		Entries map[string]*Entry `json:"entries"` // paths in request -> Entry
	} `json:"payload"`
}

// match takes a path consisting of the repo name and full path of a file or
// directory within that repo and returns the deepest Entry match in the file
// hierarchy for the given resource.
func match(path string) *Entry {
	var deepestPath string
	for p := range entries {
		if strings.HasPrefix(path, p) && len(p) > len(deepestPath) {
			deepestPath = p
		}
	}
	return entries[deepestPath]
}

// URLPathPrefix is the prefix that should be used when registering Handler.
const URLPathPrefix = "/owners/"

// Handler takes one or more paths and returns a map of each to a matching
// Entry struct. If no Entry is matched for the path, the value for the key
// is nil.
func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var (
		resp  Response
		paths []string
	)
	switch r.Method {
	case "GET":
		p := r.URL.Path
		if strings.HasPrefix(p, URLPathPrefix) {
			paths = append(paths, r.URL.Path[len(URLPathPrefix):])
		}
	case "POST":
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "unable to decode request", http.StatusInternalServerError)
			// TODO: increment expvar for monitoring.
			log.Printf("unable to decode owners request: %v", err)
			return
		}
		paths = append(paths, req.Payload.Paths...)
	case "OPTIONS":
		// Likely a CORS preflight request; leave resp.Payload empty.
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	resp.Payload.Entries = make(map[string]*Entry)
	for _, p := range paths {
		resp.Payload.Entries[p] = match(p)
	}
	w.Header().Set("Content-Type", "application/json")
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		http.Error(w, "unable to encode response", http.StatusInternalServerError)
		// TODO: increment expvar for monitoring.
		log.Printf("unable to encode owners response: %v", err)
		return
	}
	w.Write(buf.Bytes())
}
