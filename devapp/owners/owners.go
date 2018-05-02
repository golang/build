// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package owners

import (
	"bytes"
	"encoding/json"
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

// Handler serves Entry structs as JSON responses given a URL path of a repo and
// file. For example, /owners/go/src/archive/tar/reader.go should return a
// single Entry for that file or 404 if it can't find an owner.
// TODO: Ability to POST a list of paths to match.
func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	p := r.URL.Path
	if strings.HasPrefix(p, URLPathPrefix) {
		p = r.URL.Path[len(URLPathPrefix):]
	}
	e := match(p)
	if e == nil {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(e); err != nil {
		http.Error(w, "unable to encode response", http.StatusInternalServerError)
		return
	}
	w.Write(buf.Bytes())
}
