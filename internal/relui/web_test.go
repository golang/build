// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package relui

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"golang.org/x/build/internal"
	"golang.org/x/build/internal/relui/db"
)

// testStatic is our static web server content.
//go:embed testing
var testStatic embed.FS

func TestFileServerHandler(t *testing.T) {
	h := fileServerHandler(testStatic, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Home"))
	}))

	cases := []struct {
		desc        string
		path        string
		wantCode    int
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			desc:     "fallback to next handler",
			path:     "/",
			wantCode: http.StatusOK,
			wantBody: "Home",
		},
		{
			desc:     "sets headers and returns file",
			path:     "/testing/test.css",
			wantCode: http.StatusOK,
			wantBody: ".Header { font-size: 10rem; }\n",
			wantHeaders: map[string]string{
				"Content-Type":  "text/css; charset=utf-8",
				"Cache-Control": "no-cache, private, max-age=0",
			},
		},
		{
			desc:     "handles missing file",
			path:     "/foo.js",
			wantCode: http.StatusNotFound,
			wantBody: "404 page not found\n",
			wantHeaders: map[string]string{
				"Content-Type": "text/plain; charset=utf-8",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != c.wantCode {
				t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, c.wantCode)
			}
			b, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Errorf("resp.Body = _, %v, wanted no error", err)
			}
			if string(b) != c.wantBody {
				t.Errorf("resp.Body = %q, %v, wanted %q, %v", b, err, c.wantBody, nil)
			}
			for k, v := range c.wantHeaders {
				if resp.Header.Get(k) != v {
					t.Errorf("resp.Header.Get(%q) = %q, wanted %q", k, resp.Header.Get(k), v)
				}
			}
		})
	}
}

func TestServerHomeHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := testDB(ctx, t)

	q := db.New(p)
	wf := db.CreateWorkflowParams{ID: uuid.New()}
	if _, err := q.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow(_, %v) = _, %v, wanted no error", wf, err)
	}
	tp := db.CreateTaskParams{WorkflowID: wf.ID, Name: "TestTask"}
	if _, err := q.CreateTask(ctx, tp); err != nil {
		t.Fatalf("CreateTask(_, %v) = _, %v, wanted no error", tp, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	s := NewServer(p)
	s.homeHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServerNewWorkflowHandler(t *testing.T) {
	cases := []struct {
		desc     string
		params   url.Values
		wantCode int
	}{
		{
			desc:     "No selection",
			wantCode: http.StatusOK,
		},
		{
			desc:     "valid workflow",
			params:   url.Values{"workflow.name": []string{"echo"}},
			wantCode: http.StatusOK,
		},
		{
			desc:     "invalid workflow",
			params:   url.Values{"workflow.name": []string{"this workflow does not exist"}},
			wantCode: http.StatusOK,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			u := url.URL{Path: "/workflows/new", RawQuery: c.params.Encode()}
			req := httptest.NewRequest(http.MethodGet, u.String(), nil)
			w := httptest.NewRecorder()

			s := &Server{}
			s.newWorkflowHandler(w, req)
			resp := w.Result()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
			}
		})
	}
}

func TestServerCreateWorkflowHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cases := []struct {
		desc          string
		params        url.Values
		wantCode      int
		wantHeaders   map[string]string
		wantWorkflows []db.Workflow
		wantTasks     []db.Task
	}{
		{
			desc:     "no params",
			wantCode: http.StatusBadRequest,
		},
		{
			desc:     "invalid workflow name",
			params:   url.Values{"workflow.name": []string{"invalid"}},
			wantCode: http.StatusBadRequest,
		},
		{
			desc:     "missing workflow params",
			params:   url.Values{"workflow.name": []string{"echo"}},
			wantCode: http.StatusBadRequest,
		},
		{
			desc: "successful creation",
			params: url.Values{
				"workflow.name":            []string{"echo"},
				"workflow.params.greeting": []string{"abc"},
			},
			wantCode: http.StatusSeeOther,
			wantHeaders: map[string]string{
				"Location": "/",
			},
			wantWorkflows: []db.Workflow{
				{
					ID:        uuid.New(), // SameUUIDVariant
					Params:    nullString(`{"greeting": "abc"}`),
					Name:      nullString(`Echo`),
					CreatedAt: time.Now(), // cmpopts.EquateApproxTime
					UpdatedAt: time.Now(), // cmpopts.EquateApproxTime
				},
			},
			wantTasks: []db.Task{
				{
					Name:      "echo",
					Finished:  false,
					Error:     sql.NullString{},
					CreatedAt: time.Now(), // cmpopts.EquateApproxTime
					UpdatedAt: time.Now(), // cmpopts.EquateApproxTime
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			p := testDB(ctx, t)
			req := httptest.NewRequest(http.MethodPost, "/workflows/create", strings.NewReader(c.params.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			q := db.New(p)

			s := NewServer(p)
			s.createWorkflowHandler(w, req)
			resp := w.Result()

			if resp.StatusCode != c.wantCode {
				t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, c.wantCode)
			}
			for k, v := range c.wantHeaders {
				if resp.Header.Get(k) != v {
					t.Errorf("resp.Header.Get(%q) = %q, wanted %q", k, resp.Header.Get(k), v)
				}
			}
			if c.wantCode == http.StatusBadRequest {
				return
			}
			wfs, err := q.Workflows(ctx)
			if err != nil {
				t.Fatalf("q.Workflows() = %v, %v, wanted no error", wfs, err)
			}
			if diff := cmp.Diff(c.wantWorkflows, wfs, SameUUIDVariant(), cmpopts.EquateApproxTime(time.Minute)); diff != "" {
				t.Fatalf("q.Workflows() mismatch (-want +got):\n%s", diff)
			}
			var tasks []db.Task
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			internal.PeriodicallyDo(ctx, 10*time.Millisecond, func(ctx context.Context, _ time.Time) {
				tasks, err = q.TasksForWorkflow(ctx, wfs[0].ID)
				if err != nil {
					t.Fatalf("q.TasksForWorkflow(_, %q) = %v, %v, wanted no error", wfs[0].ID, tasks, err)
				}
				if len(tasks) > 0 {
					cancel()
				}
			})
			if len(c.wantTasks) > 0 {
				c.wantTasks[0].WorkflowID = wfs[0].ID
			}
			if diff := cmp.Diff(c.wantTasks, tasks, cmpopts.EquateApproxTime(time.Minute), cmpopts.IgnoreFields(db.Task{}, "Finished", "Result")); diff != "" {
				t.Errorf("q.TasksForWorkflow(_, %q) mismatch (-want +got):\n%s", wfs[0].ID, diff)
			}
		})
	}
}

// resetDB truncates the db connected to in the pgxpool.Pool
// connection.
//
// All tables in the public schema of the connected database will be
// truncated, with the exception of the migrations table.
func resetDB(ctx context.Context, t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	tableQuery := `SELECT table_name FROM information_schema.tables WHERE table_schema='public'`
	rows, err := p.Query(ctx, tableQuery)
	if err != nil {
		t.Fatalf("p.Query(_, %q, %q) = %v, %v, wanted no error", tableQuery, "public", rows, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("rows.Scan() = %v, wanted no error", err)
		}
		if name == "migrations" {
			continue
		}
		truncQ := fmt.Sprintf("TRUNCATE %s CASCADE", pgx.Identifier{name}.Sanitize())
		c, err := p.Exec(ctx, truncQ)
		if err != nil {
			t.Fatalf("p.Exec(_, %q) = %v, %v", truncQ, c, err)
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows.Err() = %v, wanted no error", err)
	}
}

var testPoolOnce sync.Once
var testPool *pgxpool.Pool

// testDB connects, creates, and migrates a database in preparation
// for testing, and returns a connection pool to the prepared
// database.
//
// The connection pool is closed as part of a t.Cleanup handler.
// Database connections are expected to be configured through libpq
// compatible environment variables. If no PGDATABASE is specified,
// relui-test will be used.
//
// https://www.postgresql.org/docs/current/libpq-envars.html
func testDB(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping database tests in short mode.")
	}
	testPoolOnce.Do(func() {
		pgdb := url.QueryEscape(os.Getenv("PGDATABASE"))
		if pgdb == "" {
			pgdb = "relui-test"
		}
		if err := InitDB(ctx, fmt.Sprintf("database=%v", pgdb)); err != nil {
			t.Skipf("Skipping database integration test: %v", err)
		}
		p, err := pgxpool.Connect(ctx, fmt.Sprintf("database=%v", pgdb))
		if err != nil {
			t.Skipf("Skipping database integration test: %v", err)
		}
		testPool = p
	})
	if testPool == nil {
		t.Skip("Skipping database integration test: testdb = nil. See first error for details.")
		return nil
	}
	t.Cleanup(func() {
		resetDB(context.Background(), t, testPool)
	})
	return testPool
}

// SameUUIDVariant considers UUIDs equal if they are both the same
// uuid.Variant. Zero-value uuids are considered equal.
func SameUUIDVariant() cmp.Option {
	return cmp.Transformer("SameVariant", func(v uuid.UUID) uuid.Variant {
		return v.Variant()
	})
}

func TestSameUUIDVariant(t *testing.T) {
	cases := []struct {
		desc string
		x    uuid.UUID
		y    uuid.UUID
		want bool
	}{
		{
			desc: "both set",
			x:    uuid.New(),
			y:    uuid.New(),
			want: true,
		},
		{
			desc: "both unset",
			want: true,
		},
		{
			desc: "just x",
			x:    uuid.New(),
			want: false,
		},
		{
			desc: "just y",
			y:    uuid.New(),
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := cmp.Equal(c.x, c.y, SameUUIDVariant()); got != c.want {
				t.Fatalf("cmp.Equal(%v, %v, SameUUIDVariant()) = %t, wanted %t", c.x, c.y, got, c.want)
			}
		})
	}
}

// nullString returns a sql.NullString for a string.
func nullString(val string) sql.NullString {
	return sql.NullString{String: val, Valid: true}
}
