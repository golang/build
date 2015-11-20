// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"text/template"

	"golang.org/x/build/types"
)

// handleDoSomeWork adds the last committed CL as work to do.
//
// Only available in dev mode.
func handleDoSomeWork(work chan<- builderRev) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			buf := new(bytes.Buffer)
			if err := tmplDoSomeWork.Execute(buf, reversePool.Modes()); err != nil {
				http.Error(w, fmt.Sprintf("dosomework: %v", err), http.StatusInternalServerError)
			}
			buf.WriteTo(w)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "dosomework only takes GET and POST", http.StatusBadRequest)
			return
		}

		mode := strings.TrimPrefix(r.URL.Path, "/dosomework/")
		log.Printf("looking for work: %q", mode)

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "looking for work for %s...\n", mode)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		rev, err := latestBuildableGoRev()
		if err != nil {
			fmt.Fprintf(w, "cannot find revision: %v", err)
			return
		}
		fmt.Fprintf(w, "found work: %s\n", rev)
		work <- builderRev{name: mode, rev: rev}
	}
}

func latestBuildableGoRev() (string, error) {
	var bs types.BuildStatus
	res, err := http.Get("https://build.golang.org/?mode=json")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&bs); err != nil {
		return "", err
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("unexpected build.golang.org http status %v", res.Status)
	}
	// Find first "ok" revision.
	for _, br := range bs.Revisions {
		if br.Repo == "go" {
			ok := false
			for _, res := range br.Results {
				if res == "ok" {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
			return br.Revision, nil
		}
	}
	// No "ok" revisions, return the first go revision.
	for _, br := range bs.Revisions {
		if br.Repo == "go" {
			return br.Revision, nil
		}
	}
	return "", errors.New("no revisions on build.golang.org")
}

var tmplDoSomeWork = template.Must(template.New("").Parse(`
<html><head><title>do some work</title></head><body>
<h1>do some work</h1>
{{range .}}
<form action="/dosomework/{{.}}" method="POST"><button>{{.}}</button></form><br\>
{{end}}
<form action="/dosomework/linux-amd64" method="POST"><button>linux-amd64</button></form><br\>
</body></html>
`))
