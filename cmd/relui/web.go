// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"html/template"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
)

func fileServerHandler(root string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}
		if _, err := os.Stat(path.Join(root, r.URL.Path)); os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(r.URL.Path)))
		w.Header().Set("Cache-Control", "no-cache, private, max-age=0")

		fs := http.FileServer(http.Dir(root))
		fs.ServeHTTP(w, r)
	})
}

var homeTemplate = template.Must(template.ParseFiles(relativeFile("index.html")))

func homeHandler(w http.ResponseWriter, _ *http.Request) {
	if err := homeTemplate.Execute(w, nil); err != nil {
		log.Printf("homeHandlerFunc: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

// relativeFile returns the path to the provided file or directory,
// conditionally prepending a relative path depending on the environment.
//
// In tests the current directory is ".", but the command may be running from the module root.
func relativeFile(base string) string {
	// Check to see if it is in "." first.
	if _, err := os.Stat(base); err == nil {
		return base
	}
	return filepath.Join("cmd/relui", base)
}
