// Copyright 2022 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
func fetchNamedUnitBenchmark(ctx context.Context, qc api.QueryAPI, start, end time.Time, name, unit string) (*BenchmarkJSON, error) {
	if err := validateFluxString(name); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}
	if err := validateFluxString(unit); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}

	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: %s, stop: %s)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["name"] == "%s")
  |> filter(fn: (r) => r["unit"] == "%s")
  |> filter(fn: (r) => r["branch"] == "master")
  |> filter(fn: (r) => r["goos"] == "linux")
  |> filter(fn: (r) => r["goarch"] == "amd64")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, start.Format(time.RFC3339), end.Format(time.RFC3339), name, unit)

	res, err := qc.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %W", err)
	}

	b, err := groupBenchmarkResults(res, false)
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
func fetchDefaultBenchmarks(ctx context.Context, qc api.QueryAPI, start, end time.Time) ([]*BenchmarkJSON, error) {
	// Keep benchmarks with the same name grouped together, which is
	// assumed by the JS.
	benchmarks := []struct{ name, unit string }{
		{
			name: "Tile38WithinCircle100kmRequest",
			unit: "sec/op",
		},
		{
			name: "Tile38WithinCircle100kmRequest",
			unit: "p50-latency-sec",
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
			name: "BleveQuery",
			unit: "sec/op",
		},
		{
			name: "BleveQuery",
			unit: "average-RSS-bytes",
		},
		{
			name: "BleveQuery",
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
		{
			name: "RegexMatch-8",
			unit: "sec/op",
		},
		{
			name: "BuildJSON-8",
			unit: "sec/op",
		},
		{
			name: "ZapJSON-8",
			unit: "sec/op",
		},
	}

	ret := make([]*BenchmarkJSON, 0, len(benchmarks))
	for _, bench := range benchmarks {
		b, err := fetchNamedUnitBenchmark(ctx, qc, start, end, bench.name, bench.unit)
		if err != nil {
			return nil, fmt.Errorf("error fetching benchmark %s/%s: %w", bench.name, bench.unit, err)
		}
		ret = append(ret, b)
	}

	return ret, nil
}

// fetchNamedBenchmark queries Influx for all benchmark results with the passed
// name (for all units).
func fetchNamedBenchmark(ctx context.Context, qc api.QueryAPI, start, end time.Time, name string) ([]*BenchmarkJSON, error) {
	if err := validateFluxString(name); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}

	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: %s, stop: %s)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["name"] == "%s")
  |> filter(fn: (r) => r["branch"] == "master")
  |> filter(fn: (r) => r["goos"] == "linux")
  |> filter(fn: (r) => r["goarch"] == "amd64")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, start.Format(time.RFC3339), end.Format(time.RFC3339), name)

	res, err := qc.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %W", err)
	}

	b, err := groupBenchmarkResults(res, false)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errBenchmarkNotFound
	}
	return b, nil
}

// fetchAllBenchmarks queries Influx for all benchmark results.
func fetchAllBenchmarks(ctx context.Context, qc api.QueryAPI, regressions bool, start, end time.Time) ([]*BenchmarkJSON, error) {
	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: %s, stop: %s)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["branch"] == "master")
  |> filter(fn: (r) => r["goos"] == "linux")
  |> filter(fn: (r) => r["goarch"] == "amd64")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, start.Format(time.RFC3339), end.Format(time.RFC3339))

	res, err := qc.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %W", err)
	}

	return groupBenchmarkResults(res, regressions)
}

type regression struct {
	change         float64 // endpoint regression, if any
	deltaIndex     int     // index at which largest increase of regression occurs
	deltaScore     float64 // score of that change (in 95%ile boxes)
	delta          float64 // size of that changes
	unit           string
	ignoredBecause string
}

// queryToJson process a QueryTableResult into a slice of BenchmarkJSON,
// with that slice in no particular order (i.e., it needs to be sorted or
// run-to-run results will vary).  For each benchmark in the slice, however,
// results are sorted into commit-date order.
func queryToJson(res *api.QueryTableResult) ([]*BenchmarkJSON, error) {
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
		// Ensure that the benchmarks are commit-date ordered.
		sort.Slice(b.Values, func(i, j int) bool {
			return b.Values[i].CommitDate.Before(b.Values[j].CommitDate)
		})
		s = append(s, b)
	}

	return s, nil
}

// reorderRegressionsFirst sorts the benchmarks in s so that those with the
// largest detectable regressions come first, and returns a map from benchmark name
// to the worst regression for that name (across all units)
func reorderRegressionsFirst(s []*BenchmarkJSON) map[string]regression {
	r := make(map[string]regression) // worst regression for each benchmark name, across all units for that benchmark

	// Compute per-benchmark estimates of point where the most interesting regression happened.
	// TODO This information might be worth adding to the graphs in general, once we do that for the regressions view.
	for _, b := range s {
		if worst := worstRegression(b); worst.deltaScore > 1 && worst.delta > r[b.Name].delta {
			r[b.Name] = worst
		} else if _, ok := r[b.Name]; !ok { // don't overwrite success for one unit w/ failure explanation for another
			r[b.Name] = worst // This is for regression ordering debugging and might end up removed.
		}
	}

	// Sort benchmarks with detectable regressions first, ordered by
	// size of regression at end of sample.  Also sort the remaining
	// benchmarks into end-of-sample regression order.  Keep benchmarks
	// with the same name grouped together, which is assumed by the
	// graph presentation server.
	sort.Slice(s, func(i, j int) bool {
		if s[i].Name == s[j].Name {
			return s[i].Unit < s[j].Unit
		}
		ri, iok := r[s[i].Name]
		rj, jok := r[s[j].Name]
		if iok != jok {
			// a name with regression information attached comes first.
			return iok
		}
		// regressions w/ a delta index come first
		if (ri.deltaIndex < 0) != (rj.deltaIndex < 0) {
			return rj.deltaIndex < 0
		}
		if ri.change != rj.change {
			// put larger regression first.
			return ri.change > rj.change
		}
		return s[i].Name < s[j].Name
	})

	return r
}

// renameBenchmarksWithRegressions changes the names of benchmarks to include information about regressions,
// and returns the number of regression points that were detected.
// TODO(drchase, mpratt) better if this can be done in the graph or surrounding text.
func renameBenchmarksWithRegressions(s []*BenchmarkJSON, r map[string]regression) {
	detected := 0
	// This injects regression information into the titles.
	for i, b := range s {
		if change, ok := r[b.Name]; ok {
			if change.deltaIndex >= 0 {
				detected++
				v := b.Values[change.deltaIndex]
				commit, date := v.CommitHash[:7], v.CommitDate.Format(time.RFC3339)
				s[i].Name = fmt.Sprintf("%s %s %3.1f%% regression, %3.1f%%point change at %s on %s (score %3.1f)", b.Name, change.unit, 100*change.change, 100*change.delta, commit, date, change.deltaScore)
			} else {
				s[i].Name = fmt.Sprintf("%s %s not ranked because %s", b.Name, change.unit, change.ignoredBecause)
			}
		}
	}
}

// groupBenchmarkResults groups all benchmark results from the passed query.
// if byRegression is true, order the benchmarks with largest current regressions
// with detectable points first.
func groupBenchmarkResults(res *api.QueryTableResult, byRegression bool) ([]*BenchmarkJSON, error) {
	s, err := queryToJson(res)
	if err != nil {
		return nil, err
	}

	if !byRegression {
		// Keep benchmarks with the same name grouped together, which is
		// assumed by the JS.
		sort.Slice(s, func(i, j int) bool {
			if s[i].Name == s[j].Name {
				return s[i].Unit < s[j].Unit
			}
			return s[i].Name < s[j].Name
		})
		return s, nil
	}

	regressions := reorderRegressionsFirst(s)
	renameBenchmarksWithRegressions(s, regressions)

	return s, nil
}

// changeScore returns an indicator of the change and direction.
// This is a heuristic measure of the lack of overlap between
// two confidence intervals; minimum lack of overlap (i.e., same
// confidence intervals) is zero.  Exact non-overlap, meaning
// the high end of one interval is equal to the low end of the
// other, is one.  A gap of size G between the two intervals
// yields a score of 1 + G/M where M is the size of the larger
// interval (this suppresses changescores adjacent to noise).
// A partial overlap of size G yields a score of
// 1 - G/M.
//
// Empty confidence intervals are problematic and produces infinities
// or NaNs.
func changeScore(l1, c1, h1, l2, c2, h2 float64) float64 {
	sign := 1.0
	if c1 > c2 {
		l1, c1, h1, l2, c2, h2 = l2, c2, h2, l1, c1, h1
		sign = -sign
	}
	r := math.Max(h1-l1, h2-l2)
	// we know l1 < c1 < h1, c1 < c2, l2 < c2 < h2
	// therefore l1 < c1 < c2 < h2
	if h1 > l2 { // overlap
		overlapHigh, overlapLow := h1, l2
		if overlapHigh > h2 {
			overlapHigh = h2
		}
		if overlapLow < l1 {
			overlapLow = l1
		}
		return sign * (1 - (overlapHigh-overlapLow)/r) // perfect overlap == 0
	} else { // no overlap
		return sign * (1 + (l2-h1)/r) // just touching, l2 == h1, magnitude == 1, and then increases w/ the gap between intervals.
	}
}

func worstRegression(b *BenchmarkJSON) regression {
	values := b.Values
	l := len(values)
	ninf := math.Inf(-1)

	sign := 1.0
	// TODO unit "good direction" information must be available from somewhere else, but for now, do this.
	switch b.Unit {
	case "B/s", "ops/s":
		sign = -1
	}

	min := sign * values[l-1].Center
	worst := regression{deltaScore: ninf, deltaIndex: -1, change: min, unit: b.Unit}

	if len(values) < 4 {
		worst.ignoredBecause = "too few values"
		return worst
	}

	scores := []float64{}

	// First classify benchmarks that are too darn noisy, and get a feel for noisiness.
	for i := l - 1; i > 0; i-- {
		v1, v0 := values[i-1], values[i]
		scores = append(scores, math.Abs(changeScore(v1.Low, v1.Center, v1.High, v0.Low, v0.Center, v0.High)))
	}

	sort.Float64s(scores)
	median := (scores[len(scores)/2] + scores[(len(scores)-1)/2]) / 2

	// MAGIC NUMBER "1".  Removing this added 25% to the "detected regressions", but they were all junk.
	if median > 1 {
		worst.ignoredBecause = "median change score > 1"
		return worst
	}

	if math.IsNaN(median) {
		worst.ignoredBecause = "median is NaN"
		return worst
	}

	// MAGIC NUMBER "1.2".  Smaller than that tends to admit junky benchmarks.
	magicScoreThreshold := math.Max(2*median, 1.2)

	// Scan backwards looking for most recent outlier regression
	for i := l - 1; i > 0; i-- {
		v1, v0 := values[i-1], values[i]
		score := sign * changeScore(v1.Low, v1.Center, v1.High, v0.Low, v0.Center, v0.High)

		if score > magicScoreThreshold && sign*v1.Center < min && score > worst.deltaScore {
			worst.deltaIndex = i
			worst.deltaScore = score
			worst.delta = sign * (v0.Center - v1.Center)
		}

		min = math.Min(sign*v0.Center, min)
	}

	if worst.deltaIndex == -1 {
		worst.ignoredBecause = "didn't detect outlier regression"
	}

	return worst
}

type gzipResponseWriter struct {
	http.ResponseWriter
	w *gzip.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.w.Write(b)
}

const (
	defaultDays = 30
	maxDays     = 366
)

// search handles /dashboard/data.json.
//
// TODO(prattmic): Consider caching Influx results in-memory for a few mintures
// to reduce load on Influx.
func (a *App) dashboardData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	days := uint64(defaultDays)
	dayParam := r.FormValue("days")
	if dayParam != "" {
		var err error
		days, err = strconv.ParseUint(dayParam, 10, 32)
		if err != nil {
			log.Printf("Error parsing days %q: %v", dayParam, err)
			http.Error(w, fmt.Sprintf("day parameter must be a positive integer less than or equal to %d", maxDays), http.StatusBadRequest)
			return
		}
		if days == 0 || days > maxDays {
			log.Printf("days %d too large", days)
			http.Error(w, fmt.Sprintf("day parameter must be a positive integer less than or equal to %d", maxDays), http.StatusBadRequest)
			return
		}
	}

	end := time.Now()
	endParam := r.FormValue("end")
	if endParam != "" {
		var err error
		// Quirk: Browsers don't have an easy built-in way to deal with
		// timezone in input boxes. The datetime input type yields a
		// string in this form, with no timezone (either local or UTC).
		// Thus, we just treat this as UTC.
		end, err = time.Parse("2006-01-02T15:04", endParam)
		if err != nil {
			log.Printf("Error parsing end %q: %v", endParam, err)
			http.Error(w, "end parameter must be a timestamp similar to RFC3339 without a time zone, like 2000-12-31T15:00", http.StatusBadRequest)
			return
		}
	}

	start := end.Add(-24 * time.Hour * time.Duration(days))

	methStart := time.Now()
	defer func() {
		log.Printf("Dashboard total query time: %s", time.Since(methStart))
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
		benchmarks, err = fetchDefaultBenchmarks(ctx, qc, start, end)
	} else if benchmark == "all" {
		benchmarks, err = fetchAllBenchmarks(ctx, qc, false, start, end)
	} else if benchmark == "regressions" {
		benchmarks, err = fetchAllBenchmarks(ctx, qc, true, start, end)
	} else {
		benchmarks, err = fetchNamedBenchmark(ctx, qc, start, end, benchmark)
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

	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		w = &gzipResponseWriter{w: gz, ResponseWriter: w}
	}

	w.WriteHeader(http.StatusOK)
	e := json.NewEncoder(w)
	e.Encode(benchmarks)
}
