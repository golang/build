// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/golang/protobuf/proto"
	reluipb "golang.org/x/build/cmd/relui/protos"
)

// fileServerHandler returns a http.Handler rooted at root. It will call the next handler provided for requests to "/".
//
// The returned handler sets the appropriate Content-Type and Cache-Control headers for the returned file.
func fileServerHandler(root string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}
		// http.FileServer would correctly return a 404, but we need to check that the file exists
		// before calculating the Content-Type header.
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

var (
	homeTmpl        = template.Must(template.Must(layoutTmpl.Clone()).ParseFiles(relativeFile("templates/home.html")))
	layoutTmpl      = template.Must(template.ParseFiles(relativeFile("templates/layout.html")))
	newWorkflowTmpl = template.Must(template.Must(layoutTmpl.Clone()).ParseFiles(relativeFile("templates/new_workflow.html")))
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
	if err := homeTmpl.Execute(&out, homeResponse{Workflows: s.store.GetWorkflows()}); err != nil {
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
	if wf.GetParams() == nil {
		wf.Params = map[string]string{}
	}
	wf.Params["GitObject"] = ref
	if err := s.store.AddWorkflow(wf); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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
