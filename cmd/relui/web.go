// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package main

import (
	"bytes"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	reluipb "golang.org/x/build/cmd/relui/protos"
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

// server implements the http handlers for relui.
type server struct {
	// configs are all configured release workflows.
	configs []*reluipb.Workflow

	// store is for persisting application state.
	store store
}

type homeResponse struct {
	Workflows []*reluipb.Workflow
}

// homeHandler renders the homepage.
func (s *server) homeHandler(w http.ResponseWriter, _ *http.Request) {
	out := bytes.Buffer{}
	if err := homeTmpl.Execute(&out, homeResponse{Workflows: s.store.Workflows()}); err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

// newWorkflowHandler presents a form for creating a new workflow.
func (s *server) newWorkflowHandler(w http.ResponseWriter, _ *http.Request) {
	out := bytes.Buffer{}
	if err := newWorkflowTmpl.Execute(&out, nil); err != nil {
		log.Printf("newWorkflowHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

// createWorkflowHandler persists a new workflow in the datastore.
func (s *server) createWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ref := r.Form.Get("workflow.revision")
	if ref == "" {
		// TODO(golang.org/issue/40279) - render a better error in the form.
		http.Error(w, "workflow revision is required", http.StatusBadRequest)
		return
	}
	if len(s.configs) == 0 {
		http.Error(w, "Unable to create workflow: no workflows configured", http.StatusInternalServerError)
		return
	}
	// Always create the first workflow for now, until we have more.
	wf := proto.Clone(s.configs[0]).(*reluipb.Workflow)
	wf.Id = uuid.New().String()
	for _, t := range wf.GetBuildableTasks() {
		t.Id = uuid.New().String()
	}
	wf.GitSource = &reluipb.GitSource{Ref: ref}
	if err := s.store.AddWorkflow(wf); err != nil {
		log.Printf("Error adding workflow: s.store.AddWorkflow(%v) = %v", wf, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) startTaskHandler(w http.ResponseWriter, r *http.Request) {
	bt := s.store.BuildableTask(r.PostFormValue("workflow.id"), r.PostFormValue("task.id"))
	if bt == nil {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
