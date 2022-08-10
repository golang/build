// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
	"github.com/julienschmidt/httprouter"
	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"golang.org/x/build/internal/relui/db"
)

var (
	kDBQueryName = tag.MustNewKey("go-build/relui/keys/db/query-name")
	mDBLatency   = stats.Float64("go-build/relui/db/latency", "Database query latency by query name", stats.UnitMilliseconds)
)

// Views should contain all measurements. All *view.View added to this
// slice will be registered and exported to the metric service.
var Views = []*view.View{
	{
		Name:        "go-build/relui/http/server/latency",
		Description: "Latency distribution of HTTP requests",
		Measure:     ochttp.ServerLatency,
		TagKeys:     []tag.Key{ochttp.KeyServerRoute},
		Aggregation: ochttp.DefaultLatencyDistribution,
	},
	{
		Name:        "go-build/relui/http/server/response_count_by_status_code",
		Description: "Server response count by status code",
		TagKeys:     []tag.Key{ochttp.StatusCode, ochttp.KeyServerRoute},
		Measure:     ochttp.ServerLatency,
		Aggregation: view.Count(),
	},
	{
		Name:        "go-build/relui/db/query_latency",
		Description: "Latency distribution of database queries",
		TagKeys:     []tag.Key{kDBQueryName},
		Measure:     mDBLatency,
		Aggregation: ochttp.DefaultLatencyDistribution,
	},
}

// metricsRouter wraps an *httprouter.Router with telemetry.
type metricsRouter struct {
	router *httprouter.Router
}

// GET is shorthand for Handle(http.MethodGet, path, handle)
func (r *metricsRouter) GET(path string, handle httprouter.Handle) {
	r.Handle(http.MethodGet, path, handle)
}

// HEAD is shorthand for Handle(http.MethodHead, path, handle)
func (r *metricsRouter) HEAD(path string, handle httprouter.Handle) {
	r.Handle(http.MethodHead, path, handle)
}

// OPTIONS is shorthand for Handle(http.MethodOptions, path, handle)
func (r *metricsRouter) OPTIONS(path string, handle httprouter.Handle) {
	r.Handle(http.MethodOptions, path, handle)
}

// POST is shorthand for Handle(http.MethodPost, path, handle)
func (r *metricsRouter) POST(path string, handle httprouter.Handle) {
	r.Handle(http.MethodPost, path, handle)
}

// PUT is shorthand for Handle(http.MethodPut, path, handle)
func (r *metricsRouter) PUT(path string, handle httprouter.Handle) {
	r.Handle(http.MethodPut, path, handle)
}

// PATCH is shorthand for Handle(http.MethodPatch, path, handle)
func (r *metricsRouter) PATCH(path string, handle httprouter.Handle) {
	r.Handle(http.MethodPatch, path, handle)
}

// DELETE is shorthand for Handle(http.MethodDelete, path, handle)
func (r *metricsRouter) DELETE(path string, handle httprouter.Handle) {
	r.Handle(http.MethodDelete, path, handle)
}

// Handler wraps *httprouter.Handler with recorded metrics.
func (r *metricsRouter) Handler(method, path string, handler http.Handler) {
	r.router.Handler(method, path, ochttp.WithRouteTag(handler, path))
}

// HandlerFunc wraps *httprouter.HandlerFunc with recorded metrics.
func (r *metricsRouter) HandlerFunc(method, path string, handler http.HandlerFunc) {
	r.Handler(method, path, handler)
}

// ServeFiles serves files at the specified root. The provided path
// must end in /*filepath.
//
// Unlike *httprouter.ServeFiles, this method sets a Content-Type and
// Cache-Control to "no-cache, private, max-age=0". This handler
// also does not strip the prefix of the request path.
func (r *metricsRouter) ServeFiles(p string, root http.FileSystem) {
	if len(p) < 10 || p[len(p)-10:] != "/*filepath" {
		panic(fmt.Sprintf("p must end with /*filepath in path %q", p))
	}

	s := http.FileServer(root)
	r.GET(p, func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(req.URL.Path)))
		w.Header().Set("Cache-Control", "no-cache, private, max-age=0")
		s.ServeHTTP(w, req)
	})
}

// Lookup wraps *httprouter.Lookup.
func (r *metricsRouter) Lookup(method, path string) (httprouter.Handle, httprouter.Params, bool) {
	return r.router.Lookup(method, path)
}

// ServeHTTP wraps *httprouter.ServeHTTP.
func (r *metricsRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.router.ServeHTTP(w, req)
}

// Handle calls *httprouter.ServeHTTP with additional metrics reporting.
func (r *metricsRouter) Handle(method, path string, handle httprouter.Handle) {
	r.router.Handle(method, path, func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		ochttp.WithRouteTag(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handle(w, r, params)
		}), path).ServeHTTP(w, r)
	})
}

type MetricsDB struct {
	db.PGDBTX
}

func (m *MetricsDB) Exec(ctx context.Context, s string, i ...any) (pgconn.CommandTag, error) {
	defer recordDB(ctx, time.Now(), queryName(s))
	return m.PGDBTX.Exec(ctx, s, i...)
}

func (m *MetricsDB) Query(ctx context.Context, s string, i ...any) (pgx.Rows, error) {
	defer recordDB(ctx, time.Now(), queryName(s))
	return m.PGDBTX.Query(ctx, s, i...)
}

func (m *MetricsDB) QueryRow(ctx context.Context, s string, i ...any) pgx.Row {
	defer recordDB(ctx, time.Now(), queryName(s))
	return m.PGDBTX.QueryRow(ctx, s, i...)
}

func recordDB(ctx context.Context, start time.Time, name string) {
	stats.RecordWithTags(ctx, []tag.Mutator{tag.Upsert(kDBQueryName, name)},
		mDBLatency.M(float64(time.Since(start))/float64(time.Millisecond)))
}

func queryName(s string) string {
	prefix := "-- name: "
	if !strings.HasPrefix(s, prefix) {
		return "Unknown"
	}
	rest := s[len(prefix):]
	return rest[:strings.IndexRune(rest, ' ')]
}
