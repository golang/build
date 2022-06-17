// Copyright 2022 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/query"
	"golang.org/x/build/internal/influx"
	"golang.org/x/build/third_party/bandchart"
)

// /dashboard/ displays a dashboard of benchmark results over time for
// performance monitoring.

//go:embed dashboard/*
var dashboardFS embed.FS

// dashboardRegisterOnMux registers the dashboard URLs on mux.
func (a *App) dashboardRegisterOnMux(mux *http.ServeMux) {
	mux.Handle("/dashboard/", http.FileServer(http.FS(dashboardFS)))
	mux.Handle("/dashboard/third_party/bandchart/", http.StripPrefix("/dashboard/third_party/bandchart/", http.FileServer(http.FS(bandchart.FS))))
	mux.HandleFunc("/dashboard/data.json", a.dashboardData)
}

// BenchmarkJSON contains the timeseries values for a single benchmark name +
// unit.
//
// We could try to shoehorn this into benchfmt.Result, but that isn't really
// the best fit for a graph.
type BenchmarkJSON struct {
	Name string
	Unit string

	// These will be sorted by CommitDate.
	Values []ValueJSON
}

type ValueJSON struct {
	CommitHash string
	CommitDate time.Time

	// These are pre-formatted as percent change.
	Low    float64
	Center float64
	High   float64
}

func fluxRecordToValue(rec *query.FluxRecord) (ValueJSON, error) {
	low, ok := rec.ValueByKey("low").(float64)
	if !ok {
		return ValueJSON{}, fmt.Errorf("record %s low value got type %T want float64", rec, rec.ValueByKey("low"))
	}

	center, ok := rec.ValueByKey("center").(float64)
	if !ok {
		return ValueJSON{}, fmt.Errorf("record %s center value got type %T want float64", rec, rec.ValueByKey("center"))
	}

	high, ok := rec.ValueByKey("high").(float64)
	if !ok {
		return ValueJSON{}, fmt.Errorf("record %s high value got type %T want float64", rec, rec.ValueByKey("high"))
	}

	commit, ok := rec.ValueByKey("experiment-commit").(string)
	if !ok {
		return ValueJSON{}, fmt.Errorf("record %s experiment-commit value got type %T want float64", rec, rec.ValueByKey("experiment-commit"))
	}

	return ValueJSON{
		CommitDate: rec.Time(),
		CommitHash: commit,
		Low:        low - 1,
		Center:     center - 1,
		High:       high - 1,
	}, nil
}

// validateRe is an allowlist of characters for a Flux string literal for
// benchmark names. The string will be quoted, so we must not allow ending the
// quote sequence.
var validateRe = regexp.MustCompile(`[a-zA-Z/_:-]+`)

func validateFluxString(s string) error {
	if !validateRe.MatchString(s) {
		return fmt.Errorf("malformed value %q", s)
	}
	return nil
}

var errBenchmarkNotFound = errors.New("benchmark not found")

// fetchNamedUnitBenchmark queries Influx for a specific name + unit benchmark.
func fetchNamedUnitBenchmark(ctx context.Context, qc api.QueryAPI, name, unit string) (*BenchmarkJSON, error) {
	if err := validateFluxString(name); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}
	if err := validateFluxString(unit); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}

	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: -30d)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["name"] == "%s")
  |> filter(fn: (r) => r["unit"] == "%s")
  |> filter(fn: (r) => r["branch"] == "master")
  |> filter(fn: (r) => r["goos"] == "linux")
  |> filter(fn: (r) => r["goarch"] == "amd64")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, name, unit)

	res, err := qc.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %W", err)
	}

	b, err := groupBenchmarkResults(res)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errBenchmarkNotFound
	}
	if len(b) > 1 {
		return nil, fmt.Errorf("query returned too many benchmarks: %+v", b)
	}
	return b[0], nil
}

// fetchDefaultBenchmarks queries Influx for the default benchmark set.
func fetchDefaultBenchmarks(ctx context.Context, qc api.QueryAPI) ([]*BenchmarkJSON, error) {
	// Keep benchmarks with the same name grouped together, which is
	// assumed by the JS.
	benchmarks := []struct{ name, unit string }{
		{
			name: "Tile38WithinCircle100kmRequest",
			unit: "sec/op",
		},
		{
			name: "Tile38WithinCircle100kmRequest",
			unit: "p90-latency-sec",
		},
		{
			name: "Tile38WithinCircle100kmRequest",
			unit: "average-RSS-bytes",
		},
		{
			name: "Tile38WithinCircle100kmRequest",
			unit: "peak-RSS-bytes",
		},
		{
			name: "GoBuildKubelet",
			unit: "sec/op",
		},
		{
			name: "GoBuildKubeletLink",
			unit: "sec/op",
		},
	}

	ret := make([]*BenchmarkJSON, 0, len(benchmarks))
	for _, bench := range benchmarks {
		b, err := fetchNamedUnitBenchmark(ctx, qc, bench.name, bench.unit)
		if err != nil {
			return nil, fmt.Errorf("error fetching benchmark %s/%s: %w", bench.name, bench.unit, err)
		}
		ret = append(ret, b)
	}

	return ret, nil
}

// fetchNamedBenchmark queries Influx for all benchmark results with the passed
// name (for all units).
func fetchNamedBenchmark(ctx context.Context, qc api.QueryAPI, name string) ([]*BenchmarkJSON, error) {
	if err := validateFluxString(name); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}

	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: -30d)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["name"] == "%s")
  |> filter(fn: (r) => r["branch"] == "master")
  |> filter(fn: (r) => r["goos"] == "linux")
  |> filter(fn: (r) => r["goarch"] == "amd64")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, name)

	res, err := qc.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %W", err)
	}

	b, err := groupBenchmarkResults(res)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errBenchmarkNotFound
	}
	return b, nil
}

// fetchAllBenchmarks queries Influx for all benchmark results.
func fetchAllBenchmarks(ctx context.Context, qc api.QueryAPI) ([]*BenchmarkJSON, error) {
	const query = `
from(bucket: "perf")
  |> range(start: -30d)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["branch"] == "master")
  |> filter(fn: (r) => r["goos"] == "linux")
  |> filter(fn: (r) => r["goarch"] == "amd64")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`

	res, err := qc.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %W", err)
	}

	return groupBenchmarkResults(res)
}

// groupBenchmarkResults groups all benchmark results from the passed query.
func groupBenchmarkResults(res *api.QueryTableResult) ([]*BenchmarkJSON, error) {
	type key struct {
		name string
		unit string
	}
	m := make(map[key]*BenchmarkJSON)

	for res.Next() {
		rec := res.Record()

		name, ok := rec.ValueByKey("name").(string)
		if !ok {
			return nil, fmt.Errorf("record %s name value got type %T want string", rec, rec.ValueByKey("name"))
		}

		unit, ok := rec.ValueByKey("unit").(string)
		if !ok {
			return nil, fmt.Errorf("record %s unit value got type %T want string", rec, rec.ValueByKey("unit"))
		}

		k := key{name, unit}
		b, ok := m[k]
		if !ok {
			b = &BenchmarkJSON{
				Name: name,
				Unit: unit,
			}
			m[k] = b
		}

		v, err := fluxRecordToValue(res.Record())
		if err != nil {
			return nil, err
		}

		b.Values = append(b.Values, v)
	}

	s := make([]*BenchmarkJSON, 0, len(m))
	for _, b := range m {
		s = append(s, b)
	}
	// Keep benchmarks with the same name grouped together, which is
	// assumed by the JS.
	sort.Slice(s, func(i, j int) bool {
		if s[i].Name == s[j].Name {
			return s[i].Unit < s[j].Unit
		}
		return s[i].Name < s[j].Name
	})

	for _, b := range s {
		sort.Slice(b.Values, func(i, j int) bool {
			return b.Values[i].CommitDate.Before(b.Values[j].CommitDate)
		})
	}

	return s, nil
}

// search handles /dashboard/data.json.
//
// TODO(prattmic): Consider caching Influx results in-memory for a few mintures
// to reduce load on Influx.
func (a *App) dashboardData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	start := time.Now()
	defer func() {
		log.Printf("Dashboard total query time: %s", time.Since(start))
	}()

	ifxc, err := a.influxClient(ctx)
	if err != nil {
		log.Printf("Error getting Influx client: %v", err)
		http.Error(w, "Error connecting to Influx", 500)
		return
	}
	defer ifxc.Close()

	qc := ifxc.QueryAPI(influx.Org)

	benchmark := r.FormValue("benchmark")
	var benchmarks []*BenchmarkJSON
	if benchmark == "" {
		benchmarks, err = fetchDefaultBenchmarks(ctx, qc)
	} else if benchmark == "all" {
		benchmarks, err = fetchAllBenchmarks(ctx, qc)
	} else {
		benchmarks, err = fetchNamedBenchmark(ctx, qc, benchmark)
	}
	if err == errBenchmarkNotFound {
		log.Printf("Benchmark not found: %q", benchmark)
		http.Error(w, "Benchmark not found", 404)
		return
	}
	if err != nil {
		log.Printf("Error fetching benchmarks: %v", err)
		http.Error(w, "Error fetching benchmarks", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	e := json.NewEncoder(w)
	e.SetIndent("", "\t")
	e.Encode(benchmarks)
}
