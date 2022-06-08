// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/workflow"
)

// fileServerHandler returns a http.Handler for serving static assets.
//
// The returned handler sets the appropriate Content-Type and
// Cache-Control headers for the returned file.
func fileServerHandler(fs fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(r.URL.Path)))
		w.Header().Set("Cache-Control", "no-cache, private, max-age=0")
		s := http.FileServer(http.FS(fs))
		s.ServeHTTP(w, r)
	})
}

// SiteHeader configures the relui site header.
type SiteHeader struct {
	Title    string // Site title. For example, "Go Releases".
	CSSClass string // Site header CSS class name. Optional.
}

// Server implements the http handlers for relui.
type Server struct {
	db      *pgxpool.Pool
	m       *httprouter.Router
	w       *Worker
	baseURL *url.URL // nil means "/".
	header  SiteHeader
	// mux used if baseURL is set
	bm *http.ServeMux

	homeTmpl        *template.Template
	newWorkflowTmpl *template.Template
}

// NewServer initializes a server with the provided connection pool,
// worker, base URL and site header.
//
// The base URL may be nil, which is the same as "/".
func NewServer(p *pgxpool.Pool, w *Worker, baseURL *url.URL, header SiteHeader) *Server {
	s := &Server{
		db:      p,
		m:       httprouter.New(),
		w:       w,
		baseURL: baseURL,
		header:  header,
	}
	helpers := map[string]interface{}{
		"baseLink":  s.BaseLink,
		"hasPrefix": strings.HasPrefix,
	}
	layout := template.Must(template.New("layout.html").Funcs(helpers).ParseFS(templates, "templates/layout.html"))
	s.homeTmpl = template.Must(template.Must(layout.Clone()).Funcs(helpers).ParseFS(templates, "templates/home.html"))
	s.newWorkflowTmpl = template.Must(template.Must(layout.Clone()).Funcs(helpers).ParseFS(templates, "templates/new_workflow.html"))
	s.m.POST("/workflows/:id/tasks/:name/retry", s.retryTaskHandler)
	s.m.POST("/workflows/:id/tasks/:name/approve", s.approveTaskHandler)
	s.m.Handler(http.MethodGet, "/workflows/new", http.HandlerFunc(s.newWorkflowHandler))
	s.m.Handler(http.MethodPost, "/workflows", http.HandlerFunc(s.createWorkflowHandler))
	s.m.Handler(http.MethodGet, "/static/*path", fileServerHandler(static))
	s.m.Handler(http.MethodGet, "/", http.HandlerFunc(s.homeHandler))
	if baseURL != nil && baseURL.Path != "/" && baseURL.Path != "" {
		nosuffix := strings.TrimSuffix(baseURL.Path, "/")
		s.bm = new(http.ServeMux)
		s.bm.Handle(nosuffix+"/", http.StripPrefix(nosuffix, s.m))
		s.bm.Handle("/", s.m)
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.bm != nil {
		s.bm.ServeHTTP(w, r)
		return
	}
	s.m.ServeHTTP(w, r)
}

func (s *Server) BaseLink(target string) string {
	if s.baseURL == nil {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		log.Printf("BaseLink: url.Parse(%q) = %v, %v", target, u, err)
		return target
	}
	if u.IsAbs() {
		return u.String()
	}
	u.Scheme = s.baseURL.Scheme
	u.Host = s.baseURL.Host
	u.Path = path.Join(s.baseURL.Path, u.Path)
	return u.String()
}

type homeResponse struct {
	SiteHeader    SiteHeader
	Workflows     []db.Workflow
	WorkflowTasks map[uuid.UUID][]db.Task
	TaskLogs      map[uuid.UUID]map[string][]db.TaskLog
}

func (h *homeResponse) Logs(workflow uuid.UUID, task string) []db.TaskLog {
	t := h.TaskLogs[workflow]
	if t == nil {
		return nil
	}
	return t[task]
}

func (h *homeResponse) WorkflowParams(wf db.Workflow) map[string]string {
	params := make(map[string]string)
	json.Unmarshal([]byte(wf.Params.String), &params)
	return params
}

// homeHandler renders the homepage.
func (s *Server) homeHandler(w http.ResponseWriter, r *http.Request) {
	resp, err := s.buildHomeResponse(r.Context())
	if err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	out := bytes.Buffer{}
	if err := s.homeTmpl.Execute(&out, resp); err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

func (s *Server) buildHomeResponse(ctx context.Context) (*homeResponse, error) {
	q := db.New(s.db)
	ws, err := q.Workflows(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := q.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	wfTasks := make(map[uuid.UUID][]db.Task, len(ws))
	for _, t := range tasks {
		wfTasks[t.WorkflowID] = append(wfTasks[t.WorkflowID], t)
	}
	tlogs, err := q.TaskLogs(ctx)
	if err != nil {
		return nil, err
	}
	wftlogs := make(map[uuid.UUID]map[string][]db.TaskLog)
	for _, l := range tlogs {
		if wftlogs[l.WorkflowID] == nil {
			wftlogs[l.WorkflowID] = make(map[string][]db.TaskLog)
		}
		wftlogs[l.WorkflowID][l.TaskName] = append(wftlogs[l.WorkflowID][l.TaskName], l)
	}
	return &homeResponse{SiteHeader: s.header, Workflows: ws, WorkflowTasks: wfTasks, TaskLogs: wftlogs}, nil
}

type newWorkflowResponse struct {
	SiteHeader  SiteHeader
	Definitions map[string]*workflow.Definition
	Name        string
}

func (n *newWorkflowResponse) Selected() *workflow.Definition {
	return n.Definitions[n.Name]
}

// newWorkflowHandler presents a form for creating a new workflow.
func (s *Server) newWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	out := bytes.Buffer{}
	resp := &newWorkflowResponse{
		SiteHeader:  s.header,
		Definitions: s.w.dh.Definitions(),
		Name:        r.FormValue("workflow.name"),
	}
	if err := s.newWorkflowTmpl.Execute(&out, resp); err != nil {
		log.Printf("newWorkflowHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

// createWorkflowHandler persists a new workflow in the datastore, and
// starts the workflow in a goroutine.
func (s *Server) createWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("workflow.name")
	d := s.w.dh.Definition(name)
	if d == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	params := make(map[string]interface{})
	for _, p := range d.Parameters() {
		switch p.Type.String() {
		case "string":
			v := r.FormValue(fmt.Sprintf("workflow.params.%s", p.Name))
			if p.RequireNonZero() && v == "" {
				http.Error(w, fmt.Sprintf("parameter %q must have non-zero value", p.Name), http.StatusBadRequest)
				return
			}
			params[p.Name] = v
		case "[]string":
			v := r.Form[fmt.Sprintf("workflow.params.%s", p.Name)]
			if p.RequireNonZero() && len(v) == 0 {
				http.Error(w, fmt.Sprintf("parameter %q must have non-zero value", p.Name), http.StatusBadRequest)
				return
			}
			params[p.Name] = v
		default:
			http.Error(w, fmt.Sprintf("parameter %q has an unsupported type %q", p.Name, p.Type), http.StatusInternalServerError)
			return
		}
	}
	if _, err := s.w.StartWorkflow(r.Context(), name, d, params); err != nil {
		log.Printf("s.w.StartWorkflow(%v, %v, %v): %v", r.Context(), d, params, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.BaseLink("/"), http.StatusSeeOther)
}

func (s *Server) retryTaskHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := uuid.Parse(params.ByName("id"))
	if err != nil {
		log.Printf("retryTaskHandler(_, _, %v) uuid.Parse(%v): %v", params, params.ByName("id"), err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if err := s.retryTask(r.Context(), id, params.ByName("name")); err != nil {
		log.Printf("s.retryTask(_, %q, %q): %v", id, params.ByName("id"), err)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if err := s.w.Resume(r.Context(), id); err != nil {
		log.Printf("s.w.Resume(_, %q): %v", id, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.BaseLink("/"), http.StatusSeeOther)
}

func (s *Server) retryTask(ctx context.Context, id uuid.UUID, name string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("tx.Begin(): %w", err)
	}
	defer tx.Rollback(ctx)
	q := db.New(s.db).WithTx(tx)
	wf, err := q.Workflow(ctx, id)
	if err != nil {
		return fmt.Errorf("q.Workflow: %w", err)
	}
	task, err := q.Task(ctx, db.TaskParams{WorkflowID: id, Name: name})
	if err != nil {
		return fmt.Errorf("q.Task: %w", err)
	}
	if _, err := q.ResetTask(ctx, db.ResetTaskParams{WorkflowID: id, Name: name, UpdatedAt: time.Now()}); err != nil {
		return fmt.Errorf("q.ResetTask: %w", err)
	}
	if _, err := q.ResetWorkflow(ctx, db.ResetWorkflowParams{ID: id, UpdatedAt: time.Now()}); err != nil {
		return fmt.Errorf("q.ResetWorkflow: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("tx.Commit: %w", err)
	}
	l := s.w.l.Logger(id, name)
	l.Printf("task reset. Previous state: %#v", task)
	l.Printf("workflow reset. Previous state: %#v", wf)
	return nil
}

func (s *Server) approveTaskHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := uuid.Parse(params.ByName("id"))
	if err != nil {
		log.Printf("approveTaskHandler(_, _, %v) uuid.Parse(%v): %v", params, params.ByName("id"), err)
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	q := db.New(s.db)
	t, err := q.Task(r.Context(), db.TaskParams{WorkflowID: id, Name: params.ByName("name")})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("q.Task(_, %q): %v", id, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	// This log entry serves as approval.
	s.w.l.Logger(id, t.Name).Printf("USER-APPROVED")
	http.Redirect(w, r, s.BaseLink("/"), http.StatusSeeOther)
}
