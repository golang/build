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
	"log"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/build/internal/criadb"
	"golang.org/x/build/internal/metrics"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/exp/slices"
)

const DatetimeLocalLayout = "2006-01-02T15:04"

// SiteHeader configures the relui site header.
type SiteHeader struct {
	Title     string // Site title. For example, "Go Releases".
	CSSClass  string // Site header CSS class name. Optional.
	Subtitle  string
	NameParam string
}

// Server implements the http handlers for relui.
type Server struct {
	db        db.PGDBTX
	m         *metricsRouter
	w         *Worker
	scheduler *Scheduler
	baseURL   *url.URL // nil means "/".
	header    SiteHeader
	// mux used if baseURL is set
	bm   *http.ServeMux
	cria *criadb.AuthDatabase // nil means all workflows are unrestricted.

	templates       *template.Template
	homeTmpl        *template.Template
	newWorkflowTmpl *template.Template
}

// NewServer initializes a server with the provided connection pool,
// worker, base URL and site header.
//
// The base URL may be nil, which is the same as "/".
//
// cria may be nil, in which case workflows are unrestricted, this is
// mainly intended to ease development.
func NewServer(p db.PGDBTX, w *Worker, baseURL *url.URL, header SiteHeader, ms *metrics.Service, cria *criadb.AuthDatabase) *Server {
	s := &Server{
		db:        p,
		m:         &metricsRouter{router: httprouter.New()},
		w:         w,
		scheduler: NewScheduler(p, w),
		baseURL:   baseURL,
		header:    header,
		cria:      cria,
	}
	if err := s.scheduler.Resume(context.Background()); err != nil {
		log.Fatalf("s.scheduler.Resume() = %v", err)
	}
	helpers := map[string]interface{}{
		"allWorkflowsCount":     s.allWorkflowsCount,
		"baseLink":              s.BaseLink,
		"hasPrefix":             strings.HasPrefix,
		"pathBase":              path.Base,
		"prettySize":            prettySize,
		"sidebarWorkflows":      s.sidebarWorkflows,
		"unmarshalResultDetail": unmarshalResultDetail,
	}
	s.templates = template.Must(template.New("").Funcs(helpers).ParseFS(templates, "templates/*.html"))
	s.homeTmpl = s.mustLookup("home.html")
	s.newWorkflowTmpl = s.mustLookup("new_workflow.html")
	s.m.GET("/workflows/:id", s.showWorkflowHandler)
	s.m.POST("/workflows/:id/stop", s.stopWorkflowHandler)
	s.m.POST("/workflows/:id/tasks/:name/retry", s.retryTaskHandler)
	s.m.POST("/workflows/:id/tasks/:name/approve", s.approveTaskHandler)
	s.m.POST("/schedules/:id/delete", s.deleteScheduleHandler)
	s.m.Handler(http.MethodGet, "/metrics", ms)
	s.m.Handler(http.MethodGet, "/new_workflow", http.HandlerFunc(s.newWorkflowHandler))
	s.m.Handler(http.MethodPost, "/workflows", http.HandlerFunc(s.createWorkflowHandler))
	s.m.ServeFiles("/static/*filepath", http.FS(static))
	s.m.Handler(http.MethodGet, "/", http.HandlerFunc(s.homeHandler))
	if baseURL != nil && baseURL.Path != "/" && baseURL.Path != "" {
		nosuffix := strings.TrimSuffix(baseURL.Path, "/")
		s.bm = new(http.ServeMux)
		s.bm.Handle(nosuffix+"/", http.StripPrefix(nosuffix, s.m))
		s.bm.Handle("/", s.m)
	}
	return s
}

func (s *Server) allWorkflowsCount() int64 {
	count, err := db.New(s.db).WorkflowCount(context.Background())
	if err != nil {
		panic(fmt.Sprintf("allWorkflowsCount: %q", err))
	}
	return count
}

func (s *Server) sidebarWorkflows(nameParam string) []db.WorkflowSidebarRow {
	sb, err := db.New(s.db).WorkflowSidebar(context.Background())
	if err != nil {
		panic(fmt.Sprintf("sidebarWorkflows: %q", err))
	}
	var filtered []db.WorkflowSidebarRow
	others := db.WorkflowSidebarRow{Name: sql.NullString{String: "Others", Valid: true}}
	for _, row := range sb {
		if s.w.dh.Definition(row.Name.String) == nil {
			others.Count += row.Count
			continue
		}
		filtered = append(filtered, row)
	}
	filtered = append(filtered, others)
	// Add a new row when on the newWorkflowsHandler if the workflow has never been run.
	if s.w.dh.Definition(nameParam) != nil && slices.IndexFunc(filtered, func(row db.WorkflowSidebarRow) bool { return row.Name.String == nameParam }) == -1 {
		filtered = append(filtered, db.WorkflowSidebarRow{Name: sql.NullString{String: nameParam, Valid: true}})
	}
	return filtered
}

func (s *Server) mustLookup(name string) *template.Template {
	t := template.Must(template.Must(s.templates.Clone()).ParseFS(templates, path.Join("templates", name))).Lookup(name)
	if t == nil {
		panic(fmt.Errorf("template %q not found", name))
	}
	return t
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var h http.Handler = s.m
	if s.bm != nil {
		h = s.bm
	}

	h.ServeHTTP(w, r)
}

func BaseLink(baseURL *url.URL) func(target string, extras ...string) string {
	return func(target string, extras ...string) string {
		u, err := url.Parse(target)
		if err != nil {
			log.Printf("BaseLink: url.Parse(%q) = %v, %v", target, u, err)
			return path.Join(append([]string{target}, extras...)...)
		}
		u.Path = path.Join(append([]string{u.Path}, extras...)...)
		if baseURL == nil || u.IsAbs() {
			return u.String()
		}
		u.Scheme = baseURL.Scheme
		u.Host = baseURL.Host
		u.Path = path.Join(baseURL.Path, u.Path)
		return u.String()
	}
}

func (s *Server) BaseLink(target string, extras ...string) string {
	return BaseLink(s.baseURL)(target, extras...)
}

type homeResponse struct {
	SiteHeader        SiteHeader
	ActiveWorkflows   []db.Workflow
	InactiveWorkflows []db.Workflow
	Schedules         []ScheduleEntry
}

// homeHandler renders the homepage.
func (s *Server) homeHandler(w http.ResponseWriter, r *http.Request) {
	q := db.New(s.db)

	names, err := q.WorkflowNames(r.Context())
	if err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	var others []string
	for _, name := range names {
		if s.w.dh.Definition(name) != nil {
			continue
		}
		others = append(others, name)
	}

	name := r.URL.Query().Get("name")
	hr := &homeResponse{SiteHeader: s.header}
	hr.SiteHeader.NameParam = name
	var ws []db.Workflow
	switch name {
	case "all", "All", "":
		ws, err = q.Workflows(r.Context())
		hr.SiteHeader.NameParam = "All Workflows"
		hr.Schedules = s.scheduler.Entries()
	case "others", "Others":
		ws, err = q.WorkflowsByNames(r.Context(), others)
		hr.SiteHeader.NameParam = "Others"
		hr.Schedules = s.scheduler.Entries(others...)
	default:
		ws, err = q.WorkflowsByName(r.Context(), sql.NullString{String: name, Valid: true})
		hr.Schedules = s.scheduler.Entries(name)
	}
	if err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	for _, w := range ws {
		if ok := s.w.workflowRunning(w.ID); ok {
			hr.ActiveWorkflows = append(hr.ActiveWorkflows, w)
			continue
		}
		hr.InactiveWorkflows = append(hr.InactiveWorkflows, w)
	}
	out := bytes.Buffer{}
	if err := s.homeTmpl.Execute(&out, hr); err != nil {
		log.Printf("homeHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

type showWorkflowResponse struct {
	SiteHeader SiteHeader
	Workflow   db.Workflow
	Tasks      []db.TasksForWorkflowSortedRow
	// TaskLogs is a map of all logs for a db.Task, keyed on
	// (db.Task).Name
	TaskLogs map[string][]db.TaskLog
}

func (s *Server) showWorkflowHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := uuid.Parse(params.ByName("id"))
	if err != nil {
		log.Printf("showWorkflowHandler(_, _, %v) uuid.Parse(%v): %v", params, params.ByName("id"), err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	resp, err := s.buildShowWorkflowResponse(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("showWorkflowHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	out := bytes.Buffer{}
	if err := s.mustLookup("show_workflow.html").Execute(&out, resp); err != nil {
		log.Printf("showWorkflowHandler: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	io.Copy(w, &out)
}

func (s *Server) buildShowWorkflowResponse(ctx context.Context, id uuid.UUID) (*showWorkflowResponse, error) {
	q := db.New(s.db)
	w, err := q.Workflow(ctx, id)
	if err != nil {
		return nil, err
	}
	tasks, err := q.TasksForWorkflowSorted(ctx, id)
	if err != nil {
		return nil, err
	}
	tlogs, err := q.TaskLogsForWorkflow(ctx, id)
	if err != nil {
		return nil, err
	}
	sr := &showWorkflowResponse{
		SiteHeader: s.header,
		TaskLogs:   make(map[string][]db.TaskLog),
		Tasks:      tasks,
		Workflow:   w,
	}
	sr.SiteHeader.Subtitle = w.Name.String
	sr.SiteHeader.NameParam = w.Name.String
	for _, l := range tlogs {
		sr.TaskLogs[l.TaskName] = append(sr.TaskLogs[l.TaskName], l)
	}
	return sr, nil
}

type newWorkflowResponse struct {
	SiteHeader      SiteHeader
	Definitions     map[string]*workflow.Definition
	Name            string
	ScheduleTypes   []ScheduleType
	Schedule        ScheduleType
	ScheduleMinTime string
}

func (n *newWorkflowResponse) Selected() *workflow.Definition {
	return n.Definitions[n.Name]
}

// newWorkflowHandler presents a form for creating a new workflow.
func (s *Server) newWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	out := bytes.Buffer{}
	name := r.FormValue("workflow.name")
	resp := &newWorkflowResponse{
		SiteHeader: s.header,
		// TODO: we may want to filter the workflows presented here to just ones
		// the user is authorized to create.
		Definitions:     s.w.dh.Definitions(),
		Name:            name,
		ScheduleTypes:   ScheduleTypes,
		Schedule:        ScheduleImmediate,
		ScheduleMinTime: time.Now().UTC().Format(DatetimeLocalLayout),
	}
	resp.SiteHeader.NameParam = name
	selectedSchedule := ScheduleType(r.FormValue("workflow.schedule"))
	if slices.Contains(ScheduleTypes, selectedSchedule) {
		resp.Schedule = selectedSchedule
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
	if !s.authorizedForWorkflow(r.Context(), d, w, r) {
		// authorizedForWorkflow writes errors to w itself.
		return
	}
	params := make(map[string]interface{})
	for _, p := range d.Parameters() {
		switch p.Type().String() {
		case "string":
			v := r.FormValue(fmt.Sprintf("workflow.params.%s", p.Name()))
			if err := p.Valid(v); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			params[p.Name()] = v
		case "[]string":
			v := r.Form[fmt.Sprintf("workflow.params.%s", p.Name())]
			if err := p.Valid(v); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			params[p.Name()] = v
		case "task.Date":
			t, err := time.Parse("2006-01-02", r.FormValue(fmt.Sprintf("workflow.params.%s", p.Name())))
			if err != nil {
				http.Error(w, fmt.Sprintf("parameter %q parsing error: %v", p.Name(), err), http.StatusBadRequest)
				return
			}
			v := task.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
			if err := p.Valid(v); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			params[p.Name()] = v
		case "bool":
			vStr := r.FormValue(fmt.Sprintf("workflow.params.%s", p.Name()))
			var v bool
			switch vStr {
			case "on":
				v = true
			case "":
				v = false
			default:
				http.Error(w, fmt.Sprintf("parameter %q has an unexpected value %q", p.Name(), vStr), http.StatusBadRequest)
				return
			}
			if err := p.Valid(v); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			params[p.Name()] = v
		default:
			http.Error(w, fmt.Sprintf("parameter %q has an unsupported type %q", p.Name(), p.Type()), http.StatusInternalServerError)
			return
		}
	}
	sched := Schedule{Type: ScheduleType(r.FormValue("workflow.schedule"))}
	if sched.Type != ScheduleImmediate {
		switch sched.Type {
		case ScheduleOnce:
			t, err := time.ParseInLocation(DatetimeLocalLayout, r.FormValue("workflow.schedule.datetime"), time.UTC)
			if err != nil || t.Before(time.Now()) {
				http.Error(w, fmt.Sprintf("parameter %q parsing error: %v", "workflow.schedule.datetime", err), http.StatusBadRequest)
				return
			}
			sched.Once = t
		case ScheduleCron:
			sched.Cron = r.FormValue("workflow.schedule.cron")
		}
		if err := sched.Valid(); err != nil {
			http.Error(w, fmt.Sprintf("parameter %q parsing error: %v", "workflow.schedule", err), http.StatusBadRequest)
			return
		}
		if _, err := s.scheduler.Create(r.Context(), sched, name, params); err != nil {
			http.Error(w, fmt.Sprintf("failed to create schedule: %v", err), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, s.BaseLink("/"), http.StatusSeeOther)
		return
	}
	id, err := s.w.StartWorkflow(r.Context(), name, params, 0)
	if err != nil {
		log.Printf("s.w.StartWorkflow(%v, %v, %v): %v", r.Context(), d, params, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.BaseLink("/workflows", id.String()), http.StatusSeeOther)
}

func (s *Server) retryTaskHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := uuid.Parse(params.ByName("id"))
	if err != nil {
		log.Printf("retryTaskHandler(_, _, %v) uuid.Parse(%v): %v", params, params.ByName("id"), err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	q := db.New(s.db)
	workflow, err := q.Workflow(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("retryTaskHandler(_, _, %v): Workflow(%d): %v", params, id, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	d := s.w.dh.Definition(workflow.Name.String)
	if d == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.authorizedForWorkflow(r.Context(), d, w, r) {
		// authorizedForWorkflow writes errors to w itself.
		return
	}
	if err := s.w.RetryTask(r.Context(), id, params.ByName("name")); err != nil {
		log.Printf("s.w.RetryTask(_, %q): %v", id, err)
	}
	http.Redirect(w, r, s.BaseLink("/workflows", id.String()), http.StatusSeeOther)
}

func (s *Server) approveTaskHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := uuid.Parse(params.ByName("id"))
	if err != nil {
		log.Printf("approveTaskHandler(_, _, %v) uuid.Parse(%v): %v", params, params.ByName("id"), err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	q := db.New(s.db)
	workflow, err := q.Workflow(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("approveTaskHandler(_, _, %v): Workflow(%d): %v", params, id, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	d := s.w.dh.Definition(workflow.Name.String)
	if d == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.authorizedForWorkflow(r.Context(), d, w, r) {
		// authorizedForWorkflow writes errors to w itself.
		return
	}
	t, err := q.ApproveTask(r.Context(), db.ApproveTaskParams{
		WorkflowID: id,
		Name:       params.ByName("name"),
		ApprovedAt: sql.NullTime{Time: time.Now(), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("q.ApproveTask(_, %q) = %v, %v", id, t, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	s.w.l.Logger(id, t.Name).Printf("USER-APPROVED")
	http.Redirect(w, r, s.BaseLink("/workflows", id.String()), http.StatusSeeOther)
}

func (s *Server) stopWorkflowHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := uuid.Parse(params.ByName("id"))
	if err != nil {
		log.Printf("stopWorkflowHandler(_, _, %v) uuid.Parse(%v): %v", params, params.ByName("id"), err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	q := db.New(s.db)
	workflow, err := q.Workflow(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("stopWorkflowHandler(_, _, %v): Workflow(%d): %v", params, id, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	d := s.w.dh.Definition(workflow.Name.String)
	if d == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.authorizedForWorkflow(r.Context(), d, w, r) {
		// authorizedForWorkflow writes errors to w itself.
		return
	}
	if !s.w.cancelWorkflow(id) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, s.BaseLink("/"), http.StatusSeeOther)
}

func (s *Server) deleteScheduleHandler(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := strconv.Atoi(params.ByName("id"))
	if err != nil {
		log.Printf("deleteScheduleHandler(_, _, %v) strconv.Atoi(%q) = %d, %v", params, params.ByName("id"), id, err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	q := db.New(s.db)
	rows, err := q.Schedules(r.Context())
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	var workflowName string
	for _, row := range rows {
		if row.ID == int32(id) {
			workflowName = row.WorkflowName
			break
		}
	}
	if workflowName == "" {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	d := s.w.dh.Definition(workflowName)
	if d == nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.authorizedForWorkflow(r.Context(), d, w, r) {
		// authorizedForWorkflow writes errors to w itself.
		return
	}
	err = s.scheduler.Delete(r.Context(), id)
	if err == ErrScheduleNotFound {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("deleteScheduleHandler(_, _, %v) s.scheduler.Delete(_, %d) = %v", params, id, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, s.BaseLink("/"), http.StatusSeeOther)
}

// resultDetail contains unmarshalled results from a workflow task, or
// workflow output. Only one field is expected to be populated.
//
// The UI implementation uses Kind to determine which result type to
// render.
type resultDetail struct {
	Artifact artifact
	Outputs  map[string]*resultDetail
	JSON     map[string]interface{}
	String   string
	Number   float64
	Slice    []*resultDetail
	Boolean  bool
	Unknown  interface{}
}

func (r *resultDetail) Kind() string {
	v := reflect.ValueOf(r)
	if v.IsZero() {
		return ""
	}
	v = v.Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			continue
		}
		return v.Type().Field(i).Name
	}
	return ""
}

func (r *resultDetail) UnmarshalJSON(result []byte) error {
	v := reflect.ValueOf(r).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if err := json.Unmarshal(result, f.Addr().Interface()); err == nil {
			if f.IsZero() {
				continue
			}
			return nil
		}
	}
	return errors.New("unknown result type")
}

func unmarshalResultDetail(result string) *resultDetail {
	ret := new(resultDetail)
	if err := json.Unmarshal([]byte(result), &ret); err != nil {
		ret.String = err.Error()
	}
	return ret
}

func prettySize(size int) string {
	const mb = 1 << 20
	if size == 0 {
		return ""
	}
	if size < mb {
		// All Go releases are >1mb, but handle this case anyway.
		return fmt.Sprintf("%v bytes", size)
	}
	return fmt.Sprintf("%.0fMiB", float64(size)/mb)
}

// authorizedForWorkflow checks if the authenticated user who sent request r is
// authorized to access workflow d, by checking if they are a member of any of
// the configured groups in d.acl. Group membership is determined by querying
// the CrIA authorization database. It writes a response to w if and only if
// it returns false, in which case the caller doesn't need to.
func (s *Server) authorizedForWorkflow(ctx context.Context, d *workflow.Definition, w http.ResponseWriter, r *http.Request) bool {
	if s.cria == nil {
		return true
	}
	authorizedGroups := d.AuthorizedGroups()
	if authorizedGroups == nil {
		return true
	}

	email := ctx.Value("email")
	if email == nil {
		log.Printf("request context did not contain expected 'email' value from IAP JWT")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return false
	}

	isMember, err := s.cria.IsMemberOfAny(ctx, fmt.Sprintf("user:%s", email), authorizedGroups)
	if err != nil {
		log.Printf("cria.IsMemberOfAny(user:%s) failed: %s", email, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return false
	}
	if !isMember {
		// TODO(roland): At some point we way want to provide a better UX for
		// this case. Currently it will just blast the user with the browser
		// default 403 status page.
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	return true
}
