// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package relui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/workflow"
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
	db *pgxpool.Pool
	m  *http.ServeMux
}

// NewServer initializes a server with the provided connection pool.
func NewServer(p *pgxpool.Pool) *Server {
	s := &Server{db: p, m: &http.ServeMux{}}
	s.m.Handle("/workflows/create", http.HandlerFunc(s.createWorkflowHandler))
	s.m.Handle("/workflows/new", http.HandlerFunc(s.newWorkflowHandler))
	s.m.Handle("/", fileServerHandler(static, http.HandlerFunc(s.homeHandler)))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.m.ServeHTTP(w, r)
}

func (s *Server) Serve(port string) error {
	return http.ListenAndServe(":"+port, s.m)
}

type homeResponse struct {
	Workflows     []db.Workflow
	WorkflowTasks map[uuid.UUID][]db.Task
}

// homeHandler renders the homepage.
func (s *Server) homeHandler(w http.ResponseWriter, r *http.Request) {
	q := db.New(s.db)
	ws, err := q.Workflows(r.Context())
	if err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	tasks, err := q.Tasks(r.Context())
	if err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	wfTasks := make(map[uuid.UUID][]db.Task, len(ws))
	for _, t := range tasks {
		wfTasks[t.WorkflowID] = append(wfTasks[t.WorkflowID], t)
	}
	out := bytes.Buffer{}
	if err := homeTmpl.Execute(&out, homeResponse{Workflows: ws, WorkflowTasks: wfTasks}); err != nil {
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

// createWorkflowHandler persists a new workflow in the datastore, and
// starts the workflow in a goroutine.
func (s *Server) createWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ref := r.Form.Get("workflow.params.revision")
	if ref == "" {
		http.Error(w, "workflow revision is required", http.StatusBadRequest)
		return
	}
	params := map[string]string{"greeting": ref}
	wf, err := workflow.Start(newEchoWorkflow(ref), params)
	if err != nil {
		log.Printf("createWorkflowHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	err = s.db.BeginFunc(r.Context(), func(tx pgx.Tx) error {
		q := db.New(tx)
		m, err := json.Marshal(params)
		if err != nil {
			return err
		}
		updated := time.Now()
		_, err = q.CreateWorkflow(r.Context(), db.CreateWorkflowParams{
			ID:        wf.ID,
			Name:      sql.NullString{String: "Echo", Valid: true},
			Params:    sql.NullString{String: string(m), Valid: len(m) > 0},
			CreatedAt: updated,
			UpdatedAt: updated,
		})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		log.Printf("createWorkflowHandler: %v", err)
		http.Error(w, "Error creating workflow", http.StatusInternalServerError)
	}
	go func(wf *workflow.Workflow, db *pgxpool.Pool) {
		result, err := wf.Run(context.TODO(), &listener{db})
		log.Printf("wf.Run() = %v, %v", result, err)
	}(wf, s.db)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// listener implements workflow.Listener for recording workflow state.
type listener struct {
	db *pgxpool.Pool
}

// TaskStateChanged is called whenever a task is updated by the
// workflow. The workflow.TaskState is persisted as a db.Task,
// creating or updating a row as necessary.
func (l *listener) TaskStateChanged(workflowID uuid.UUID, taskID string, state *workflow.TaskState) error {
	log.Printf("TaskStateChanged(%q, %q, %v)", workflowID, taskID, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := json.Marshal(state.Result)
	if err != nil {
		return err
	}
	err = l.db.BeginFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		updated := time.Now()
		_, err := q.UpsertTask(ctx, db.UpsertTaskParams{
			WorkflowID: workflowID,
			Name:       taskID,
			Finished:   state.Finished,
			Result:     sql.NullString{String: string(result), Valid: len(result) > 0},
			Error:      sql.NullString{},
			CreatedAt:  updated,
			UpdatedAt:  updated,
		})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		log.Printf("TaskStateChanged(%q, %q, %v) = %v", workflowID, taskID, state, err)
	}
	return err
}

func (l *listener) Logger(workflowID uuid.UUID, taskID string) workflow.Logger {
	return &stdoutLogger{WorkflowID: workflowID, TaskID: taskID}
}

type stdoutLogger struct {
	WorkflowID uuid.UUID
	TaskID     string
}

func (l *stdoutLogger) Printf(format string, v ...interface{}) {
	log.Printf("%q(%q): %v", l.WorkflowID, l.TaskID, fmt.Sprintf(format, v...))
}

// newEchoWorkflow returns a runnable workflow.Definition for
// development.
func newEchoWorkflow(greeting string) *workflow.Definition {
	wd := workflow.New()
	gt := wd.Task("echo", echo, wd.Constant(greeting))
	wd.Output("greeting", gt)
	return wd
}

func echo(_ context.Context, arg string) (string, error) {
	return arg, nil
}
