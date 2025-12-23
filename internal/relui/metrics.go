// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
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

// metricsRouter wraps an *http.ServeMux with telemetry.
type metricsRouter struct {
	mux *http.ServeMux
}

// Handle is like (*http.ServeMux).Handle but with additional metrics reporting.
func (r *metricsRouter) Handle(pattern string, handler http.Handler) {
	r.mux.Handle(pattern, ochttp.WithRouteTag(handler, pattern))
}

// HandleFunc is like (*http.ServeMux).HandleFunc but with additional metrics reporting.
func (r *metricsRouter) HandleFunc(pattern string, handler http.HandlerFunc) {
	r.Handle(pattern, handler)
}

// ServeFiles serves files at the specified root with the
// Cache-Control header set to "no-cache, private, max-age=0".
func (r *metricsRouter) ServeFiles(pattern string, root fs.FS) {
	fs := http.FileServerFS(root)
	r.mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, private, max-age=0")
		fs.ServeHTTP(w, req)
	})
}

// ServeHTTP implements http.Handler.
func (r *metricsRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
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
