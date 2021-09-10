// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package relui

import (
	"bytes"
	"context"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
)

// fileServerHandler returns a http.Handler rooted at root. It will
// call the next handler provided for requests to "/".
//
// The returned handler sets the appropriate Content-Type and
// Cache-Control headers for the returned file.
func fileServerHandler(fs fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(r.URL.Path)))
		w.Header().Set("Cache-Control", "no-cache, private, max-age=0")
		s := http.FileServer(http.FS(fs))
		s.ServeHTTP(w, r)
	})
}

var (
	homeTmpl        = template.Must(template.Must(layoutTmpl.Clone()).ParseFS(templates, "templates/home.html"))
	layoutTmpl      = template.Must(template.ParseFS(templates, "templates/layout.html"))
	newWorkflowTmpl = template.Must(template.Must(layoutTmpl.Clone()).ParseFS(templates, "templates/new_workflow.html"))
)

// Server implements the http handlers for relui.
type Server struct {
	// store is for persisting application state.
	store store
}

// NewServer initializes a server with a database connection specified
// by pgConn. Any libpq compatible connection strings or URIs are
// supported.
func NewServer(ctx context.Context, st store) (*Server, error) {
	s := &Server{store: st}
	http.Handle("/workflows/create", http.HandlerFunc(s.createWorkflowHandler))
	http.Handle("/workflows/new", http.HandlerFunc(s.newWorkflowHandler))
	http.Handle("/", fileServerHandler(static, http.HandlerFunc(s.homeHandler)))
	return s, nil
}

func (s *Server) Serve(port string) error {
	return http.ListenAndServe(":"+port, http.DefaultServeMux)
}

type homeResponse struct {
	Workflows []interface{}
}

// homeHandler renders the homepage.
func (s *Server) homeHandler(w http.ResponseWriter, _ *http.Request) {
	out := bytes.Buffer{}
	if err := homeTmpl.Execute(&out, homeResponse{}); err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

// newWorkflowHandler presents a form for creating a new workflow.
func (s *Server) newWorkflowHandler(w http.ResponseWriter, _ *http.Request) {
	out := bytes.Buffer{}
	if err := newWorkflowTmpl.Execute(&out, nil); err != nil {
		log.Printf("newWorkflowHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

// createWorkflowHandler persists a new workflow in the datastore.
func (s *Server) createWorkflowHandler(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "Unable to create workflow: no workflows configured", http.StatusInternalServerError)
}
