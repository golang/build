// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/query"
	"go.chromium.org/luci/common/api/gitiles"
	gpb "go.chromium.org/luci/common/proto/gitiles"
	"golang.org/x/build/internal/influx"
	maintnerpb "golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/third_party/bandchart"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	gitilesHost  = "go.googlesource.com"
	maintnerHost = "maintner.golang.org:443"
)

// /dashboard/ displays a dashboard of benchmark results over time for
// performance monitoring.

//go:embed dashboard/*
var dashboardFS embed.FS

// dashboardRegisterOnMux registers the dashboard URLs on mux.
func (a *App) dashboardRegisterOnMux(mux *http.ServeMux) {
	mux.Handle("/dashboard/", http.FileServer(http.FS(dashboardFS)))
	mux.Handle("/dashboard/third_party/bandchart/", http.StripPrefix("/dashboard/third_party/bandchart/", http.FileServer(http.FS(bandchart.FS))))
	mux.HandleFunc("/dashboard/benchmarks.json", a.benchmarkList)
	mux.HandleFunc("/dashboard/data.json", a.dashboardData)
	mux.HandleFunc("/dashboard/formfields.json", a.formFields)
}

// BenchmarksJSON is the result of accessing the benchmarks.json endpoint.
type BenchmarksJSON struct {
	Benchmarks []BenchmarkKey
	Commits    []Commit
}

type BenchmarkKey struct {
	Name       string
	Repository string
}

// DataJSON is the result of accessing the data.json endpoint.
type DataJSON struct {
	Benchmarks []*BenchmarkJSON
}

// BenchmarkJSON contains the timeseries values for a single benchmark name +
// unit.
//
// We could try to shoehorn this into benchfmt.Result, but that isn't really
// the best fit for a graph.
type BenchmarkJSON struct {
	Name           string
	Unit           string
	Platform       string
	HigherIsBetter bool

	// These will be sorted by CommitDate.
	Values []ValueJSON

	Regression *RegressionJSON
}

type ValueJSON struct {
	CommitHash           string
	CommitDate           time.Time
	BaselineCommitHash   string
	BenchmarksCommitHash string

	// These are pre-formatted as percent change.
	Low    float64
	Center float64
	High   float64
}

// filter is a set of parameters used to filter influx data.
type filter struct {
	start, end time.Time // Required.
	repository string    // Required.
	goos       string    // Optional.
	goarch     string    // Optional.
	goBranch   string    // Required.
}

func fluxRecordToValueJSON(rec *query.FluxRecord) (ValueJSON, error) {
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

	baselineCommit, ok := rec.ValueByKey("baseline-commit").(string)
	if !ok {
		return ValueJSON{}, fmt.Errorf("record %s experiment-commit value got type %T want float64", rec, rec.ValueByKey("baseline-commit"))
	}

	benchmarksCommit, ok := rec.ValueByKey("benchmarks-commit").(string)
	if !ok {
		return ValueJSON{}, fmt.Errorf("record %s experiment-commit value got type %T want float64", rec, rec.ValueByKey("benchmarks-commit"))
	}

	return ValueJSON{
		CommitDate:           rec.Time(),
		CommitHash:           commit,
		BaselineCommitHash:   baselineCommit,
		BenchmarksCommitHash: benchmarksCommit,
		Low:                  low - 1,
		Center:               center - 1,
		High:                 high - 1,
	}, nil
}

// validateRe is an allowlist of characters for a Flux string literal. The
// string will be quoted, so we must not allow ending the quote sequence.
var validateRe = regexp.MustCompile(`^[a-zA-Z0-9(),=/_:;.-]+$`)

func validateFluxString(s string) error {
	if !validateRe.MatchString(s) {
		return fmt.Errorf("malformed value %q", s)
	}
	return nil
}

func influxQuery(ctx context.Context, qc api.QueryAPI, query string) (*api.QueryTableResult, error) {
	log.Printf("InfluxDB query: %s", query)
	return qc.Query(ctx, query)
}

var errBenchmarkNotFound = errors.New("benchmark not found")

// fetchNamedUnitBenchmark queries Influx for a specific name + unit benchmark.
func fetchNamedUnitBenchmark(ctx context.Context, qc api.QueryAPI, f *filter, name, unit string) (*BenchmarkJSON, error) {
	if err := validateFluxString(f.repository); err != nil {
		return nil, fmt.Errorf("invalid repository name: %w", err)
	}
	if f.goos != "" {
		if err := validateFluxString(f.goos); err != nil {
			return nil, fmt.Errorf("invalid GOOS: %w", err)
		}
	}
	if f.goarch != "" {
		if err := validateFluxString(f.goarch); err != nil {
			return nil, fmt.Errorf("invalid GOOS: %w", err)
		}
	}
	if err := validateFluxString(f.goBranch); err != nil {
		return nil, fmt.Errorf("invalid go branch name: %w", err)
	}
	if err := validateFluxString(name); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}
	if err := validateFluxString(unit); err != nil {
		return nil, fmt.Errorf("invalid unit name: %w", err)
	}

	// Note that very old points are missing the "repository" field. fill()
	// sets repository=go on all points missing that field, as they were
	// all runs of the go repo.
	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: %s, stop: %s)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["name"] == "%s")
  |> filter(fn: (r) => r["unit"] == "%s")
  |> filter(fn: (r) => r["branch"] == "%s")
  |> filter(fn: (r) => ("%s" != "" and r["goos"] == "%s") or "%s" == "")
  |> filter(fn: (r) => ("%s" != "" and r["goarch"] == "%s") or "%s" == "")
  |> fill(column: "repository", value: "go")
  |> filter(fn: (r) => r["repository"] == "%s")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, f.start.Format(time.RFC3339), f.end.Format(time.RFC3339), name, unit, f.goBranch, f.goos, f.goos, f.goos, f.goarch, f.goarch, f.goarch, f.repository)

	res, err := influxQuery(ctx, qc, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %w", err)
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

func fetchBenchmarkKeys(ctx context.Context, qc api.QueryAPI, start, end time.Time) ([]BenchmarkKey, error) {
	// Find any points in the time range and group by only the name and repository to get the set of unique name/repository combinations.
	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: %s, stop: %s)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["_field"] == "upload-time")
  |> group(columns: ["name", "repository"])
  |> unique(column: "repository")
  |> yield(name: "unique")
`, start.Format(time.RFC3339), end.Format(time.RFC3339))

	res, err := influxQuery(ctx, qc, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %w", err)
	}

	var keys []BenchmarkKey
	for res.Next() {
		rec := res.Record()

		name, ok := rec.ValueByKey("name").(string)
		if !ok {
			return nil, fmt.Errorf("record %s name got type %T want string", rec, rec.ValueByKey("name"))
		}
		repo, ok := rec.ValueByKey("repository").(string)
		if !ok {
			return nil, fmt.Errorf("record %s name got type %T want string", rec, rec.ValueByKey("repository"))
		}
		keys = append(keys, BenchmarkKey{Name: name, Repository: repo})
	}
	return keys, nil
}

// fetchNamedBenchmark queries Influx for all benchmark results with the passed
// name (for all units).
func fetchNamedBenchmark(ctx context.Context, qc api.QueryAPI, f *filter, name string, regressions bool) ([]*BenchmarkJSON, error) {
	if err := validateFluxString(f.repository); err != nil {
		return nil, fmt.Errorf("invalid repository name: %w", err)
	}
	if f.goos != "" {
		if err := validateFluxString(f.goos); err != nil {
			return nil, fmt.Errorf("invalid GOOS: %w", err)
		}
	}
	if f.goarch != "" {
		if err := validateFluxString(f.goarch); err != nil {
			return nil, fmt.Errorf("invalid GOOS: %w", err)
		}
	}
	if err := validateFluxString(f.goBranch); err != nil {
		return nil, fmt.Errorf("invalid go branch name: %w", err)
	}
	if err := validateFluxString(name); err != nil {
		return nil, fmt.Errorf("invalid benchmark name: %w", err)
	}

	// Note that very old points are missing the "repository" field. fill()
	// sets repository=go on all points missing that field, as they were
	// all runs of the go repo.
	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: %s, stop: %s)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["name"] == "%s")
  |> filter(fn: (r) => r["branch"] == "%s")
  |> filter(fn: (r) => ("%s" != "" and r["goos"] == "%s") or "%s" == "")
  |> filter(fn: (r) => ("%s" != "" and r["goarch"] == "%s") or "%s" == "")
  |> fill(column: "repository", value: "go")
  |> filter(fn: (r) => r["repository"] == "%s")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, f.start.Format(time.RFC3339), f.end.Format(time.RFC3339), name, f.goBranch, f.goos, f.goos, f.goos, f.goarch, f.goarch, f.goarch, f.repository)

	res, err := influxQuery(ctx, qc, query)
	if err != nil {
		return nil, fmt.Errorf("error performing query: %w", err)
	}

	b, err := groupBenchmarkResults(res, regressions)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errBenchmarkNotFound
	}
	return b, nil
}

type RegressionJSON struct {
	Change         float64 // endpoint regression, if any
	DeltaIndex     int     // index at which largest increase of regression occurs
	Delta          float64 // size of that changes
	IgnoredBecause string

	deltaScore float64 // score of that change (in 95%ile boxes)
}

// fluxRecordsToBenchmarkJSON process a QueryTableResult into a slice of BenchmarkJSON,
// with that slice in no particular order (i.e., it needs to be sorted or
// run-to-run results will vary).  For each benchmark in the slice, however,
// results are sorted into commit-date order.
func fluxRecordsToBenchmarkJSON(res *api.QueryTableResult) ([]*BenchmarkJSON, error) {
	type key struct {
		name   string
		unit   string
		goos   string
		goarch string
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

		goos, ok := rec.ValueByKey("goos").(string)
		if !ok {
			return nil, fmt.Errorf("record %s goos value got type %T want string", rec, rec.ValueByKey("goos"))
		}

		goarch, ok := rec.ValueByKey("goarch").(string)
		if !ok {
			return nil, fmt.Errorf("record %s goarch value got type %T want string", rec, rec.ValueByKey("goarch"))
		}

		k := key{name, unit, goos, goarch}
		b, ok := m[k]
		if !ok {
			b = &BenchmarkJSON{
				Name:           name,
				Unit:           unit,
				Platform:       goos + "/" + goarch,
				HigherIsBetter: isHigherBetter(unit),
			}
			m[k] = b
		}

		v, err := fluxRecordToValueJSON(res.Record())
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

// groupBenchmarkResults groups all benchmark results from the passed query.
// if byRegression is true, order the benchmarks with largest current regressions
// with detectable points first.
func groupBenchmarkResults(res *api.QueryTableResult, regression bool) ([]*BenchmarkJSON, error) {
	s, err := fluxRecordsToBenchmarkJSON(res)
	if err != nil {
		return nil, err
	}
	if regression {
		for _, b := range s {
			b.Regression = worstRegression(b)
			// TODO(mknyszek, drchase, mpratt): Filter out benchmarks once we're confident this
			// algorithm works OK.
		}
	}
	// Keep benchmarks with the same name grouped together, which is
	// assumed by the JS.
	sort.Slice(s, func(i, j int) bool {
		if s[i].Name != s[j].Name {
			return s[i].Name < s[j].Name
		}
		if s[i].Platform != s[j].Platform {
			return s[i].Platform < s[j].Platform
		}
		return s[i].Unit < s[j].Unit
	})
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

func isHigherBetter(unit string) bool {
	return unit == "B/s" || strings.HasSuffix(unit, "ops/s") || strings.HasSuffix(unit, "ops/sec") || strings.HasSuffix(unit, "ops")
}

func worstRegression(b *BenchmarkJSON) *RegressionJSON {
	values := b.Values
	l := len(values)
	ninf := math.Inf(-1)

	sign := 1.0
	if b.HigherIsBetter {
		sign = -1.0
	}

	min := sign * values[l-1].Center
	worst := &RegressionJSON{
		DeltaIndex: -1,
		Change:     min,
		deltaScore: ninf,
	}

	if len(values) < 4 {
		worst.IgnoredBecause = "too few values"
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
		worst.IgnoredBecause = "median change score > 1"
		return worst
	}

	if math.IsNaN(median) {
		worst.IgnoredBecause = "median is NaN"
		return worst
	}

	// MAGIC NUMBER "1.2".  Smaller than that tends to admit junky benchmarks.
	magicScoreThreshold := math.Max(2*median, 1.2)

	// Scan backwards looking for most recent outlier regression
	for i := l - 1; i > 0; i-- {
		v1, v0 := values[i-1], values[i]
		score := sign * changeScore(v1.Low, v1.Center, v1.High, v0.Low, v0.Center, v0.High)

		if score > magicScoreThreshold && sign*v1.Center < min && score > worst.deltaScore {
			worst.DeltaIndex = i
			worst.deltaScore = score
			worst.Delta = sign * (v0.Center - v1.Center)
		}

		min = math.Min(sign*v0.Center, min)
	}

	if worst.DeltaIndex == -1 {
		worst.IgnoredBecause = "didn't detect outlier regression"
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

func parseBenchmarkQueryParams(ctx context.Context, r *http.Request, log *log.Logger) (*filter, int, error) {
	days := uint64(defaultDays)
	dayParam := r.FormValue("days")
	if dayParam != "" {
		var err error
		days, err = strconv.ParseUint(dayParam, 10, 32)
		if err != nil {
			log.Printf("Error parsing days %q: %v", dayParam, err)
			return nil, http.StatusBadRequest, fmt.Errorf("day parameter must be a positive integer less than or equal to %d", maxDays)
		}
		if days == 0 || days > maxDays {
			log.Printf("days %d too large", days)
			return nil, http.StatusBadRequest, fmt.Errorf("day parameter must be a positive integer less than or equal to %d", maxDays)
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
			return nil, http.StatusBadRequest, fmt.Errorf("end parameter must be a timestamp similar to RFC3339 without a time zone, like 2000-12-31T15:00")
		}
	}
	start := end.Add(-24 * time.Hour * time.Duration(days))

	repository := r.FormValue("repository")
	if repository == "" {
		repository = "go"
	}
	branch := r.FormValue("branch")
	if branch == "" {
		branch = "master"
	} else if branch == "latest-release" {
		releases, err := goReleasesCache.Get(ctx)
		if err != nil {
			log.Printf("Fetching latest release: %v", err)
			return nil, http.StatusInternalServerError, fmt.Errorf("error fetching latest release")
		}
		branch = latestRelease(releases).BranchName
	}
	f := &filter{
		start:      start,
		end:        end,
		repository: repository,
		goBranch:   branch,
	}
	platform := r.FormValue("platform")
	if platform == "" {
		platform = "all"
	}
	if platform != "all" {
		goos, goarch, err := parsePlatform(platform)
		if err != nil {
			log.Printf("Invalid platform %q: %v", platform, err)
			return nil, http.StatusBadRequest, fmt.Errorf("error parsing platform")
		}
		f.goos = goos
		f.goarch = goarch
	}
	return f, http.StatusOK, nil
}

var defaultBenchmarks = map[string][]BenchmarkKey{
	"go": []BenchmarkKey{
		{"geomean/go/vs_release/c2s16", "go"},
		{"geomean/go/vs_release/c4as16", "go"},
		{"geomean/go/vs_release/c3h88", "go"},
		{"geomean/go/vs_release/c4ah72", "go"},
	},
	"tools": []BenchmarkKey{
		{"geomean/x_tools/vs_gopls_0_11/c2s16", "tools"},
		{"geomean/x_tools/vs_gopls_0_11/c4as16", "tools"},
	},
}

// benchmarkList handles /dashboard/benchmarks.json.
//
// TODO(prattmic): Consider caching Influx results in-memory for a few mintures
// to reduce load on Influx.
func (a *App) benchmarkList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	methStart := time.Now()
	defer func() {
		log.Printf("Benchmark list total query time: %s", time.Since(methStart))
	}()

	f, status, err := parseBenchmarkQueryParams(ctx, r, log.Default())
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	historyBranch := f.goBranch
	if f.repository != "go" {
		historyBranch = "master"
	}
	commits, err := fetchGitHistory(ctx, gitilesHost, f.repository, historyBranch, f.start, f.end)
	if err != nil {
		log.Printf("Fetching git history: %v", err)
		http.Error(w, "Error fetching git history", 500)
		return
	}
	// Commits come out newest-first, we want oldest-first.
	slices.Reverse(commits)

	ifxc, err := a.influxClient(ctx)
	if err != nil {
		log.Printf("Error getting Influx client: %v", err)
		http.Error(w, "Error connecting to Influx", 500)
		return
	}
	defer ifxc.Close()

	qc := ifxc.QueryAPI(influx.Org)

	// Fetch benchmark names, or use the default set.
	benchmarkKeys := defaultBenchmarks[f.repository]

	benchmark := r.FormValue("benchmark")
	if benchmark != "" {
		benchmarkKeys, err = fetchBenchmarkKeys(ctx, qc, f.start, f.end)
		if err != nil {
			log.Printf("Error fetching benchmarks: %v", err)
			http.Error(w, "Error fetching benchmarks", 500)
			return
		}
		// Filter out benchmark names that don't match.
		for i := 0; i < len(benchmarkKeys); {
			key := benchmarkKeys[i]
			if key.Repository != f.repository || (benchmark != "all" && !strings.Contains(key.Name, benchmark)) {
				benchmarkKeys[i] = benchmarkKeys[len(benchmarkKeys)-1]
				benchmarkKeys = benchmarkKeys[:len(benchmarkKeys)-1]
				continue
			}
			i++
		}
	}
	if len(benchmarkKeys) == 0 {
		log.Printf("No benchmarks like %s found", benchmark)
		http.Error(w, "No matching benchmarks found", 404)
		return
	}
	slices.SortFunc(benchmarkKeys, func(a, b BenchmarkKey) int {
		return strings.Compare(a.Name, b.Name)
	})

	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		w = &gzipResponseWriter{w: gz, ResponseWriter: w}
	}

	if err := json.NewEncoder(w).Encode(&BenchmarksJSON{Benchmarks: benchmarkKeys, Commits: commits}); err != nil {
		log.Printf("Error encoding results: %v", err)
		http.Error(w, "Internal error, see logs", 500)
	}
}

// dashboardData handles /dashboard/data.json.
//
// TODO(prattmic): Consider caching Influx results in-memory for a few mintures
// to reduce load on Influx.
func (a *App) dashboardData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	methStart := time.Now()
	defer func() {
		log.Printf("Dashboard total query time: %s", time.Since(methStart))
	}()

	f, status, err := parseBenchmarkQueryParams(ctx, r, log.Default())
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	ifxc, err := a.influxClient(ctx)
	if err != nil {
		log.Printf("Error getting Influx client: %v", err)
		http.Error(w, "Error connecting to Influx", 500)
		return
	}
	defer ifxc.Close()

	qc := ifxc.QueryAPI(influx.Org)

	regressions := r.FormValue("regressions")
	benchmark := r.FormValue("benchmark")
	unit := r.FormValue("unit")
	var benchmarks []*BenchmarkJSON
	if benchmark == "" {
		log.Printf("data.json: no benchmark name")
		http.Error(w, "No benchmark name specified", http.StatusBadRequest)
		return
	}
	if unit == "" {
		benchmarks, err = fetchNamedBenchmark(ctx, qc, f, benchmark, regressions == "true")
	} else {
		var result *BenchmarkJSON
		result, err = fetchNamedUnitBenchmark(ctx, qc, f, benchmark, unit)
		if result != nil && err == nil {
			benchmarks = []*BenchmarkJSON{result}
		}
	}
	if errors.Is(err, errBenchmarkNotFound) {
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

	if err := json.NewEncoder(w).Encode(&DataJSON{Benchmarks: benchmarks}); err != nil {
		log.Printf("Error encoding results: %v", err)
		http.Error(w, "Internal error, see logs", 500)
	}
}

type Commit struct {
	Hash string
	Date time.Time
}

func fetchGitHistory(ctx context.Context, gitilesHost, repository, branch string, start, end time.Time) ([]Commit, error) {
	log.Printf("Fetching git history for %s/%s @ %s [%s, %s]", gitilesHost, repository, branch, start, end)

	fetchStart := time.Now()
	defer func() {
		log.Printf("Git history query time: %s", time.Since(fetchStart))
	}()

	c := new(http.Client)
	client, err := gitiles.NewRESTClient(c, gitilesHost, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %v", err)
	}
	var commits []Commit
	var pageToken string
	for {
		resp, err := client.Log(ctx, &gpb.LogRequest{
			Project:    repository,
			Committish: "refs/heads/" + branch,
			PageSize:   500,
			PageToken:  pageToken,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to obtain log: %v", err)
		}
		for _, c := range resp.GetLog() {
			commitTime := c.GetCommitter().GetTime().AsTime()
			if commitTime.After(end) {
				continue
			}
			if commitTime.Before(start) {
				return commits, nil
			}
			commits = append(commits, Commit{
				Hash: c.GetId(),
				Date: commitTime,
			})
		}
		if resp.GetNextPageToken() == "" {
			break
		}
		pageToken = resp.GetNextPageToken()
	}
	return commits, nil
}

// formFields handles the formfields.json endpoint.
func (a *App) formFields(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Grab the releases.
	releases, err := goReleasesCache.Get(ctx)
	if err != nil {
		log.Printf("Error fetching releases: %v", err)
		http.Error(w, "Internal error, see logs", 500)
	}

	// Form the response.
	resp := FormFieldsJSON{
		Branches:            []string{"master"},
		LatestReleaseBranch: latestRelease(releases).BranchName,
	}
	for _, release := range releases {
		resp.Branches = append(resp.Branches, release.BranchName)
	}

	// Encode and write the response.
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(&resp); err != nil {
		log.Printf("Error encoding results: %v", err)
		http.Error(w, "Internal error, see logs", 500)
	}
}

type FormFieldsJSON struct {
	Branches            []string
	LatestReleaseBranch string
}

type goReleases struct {
	mu       sync.Mutex
	releases []*maintnerpb.GoRelease
	latest   *maintnerpb.GoRelease
	fetched  time.Time
}

func (r *goReleases) Get(ctx context.Context) ([]*maintnerpb.GoRelease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.fetched.IsZero() && time.Since(r.fetched) < time.Hour {
		return r.releases, nil
	}

	dialOpts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithTimeout(10 * time.Second),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{NextProtos: []string{"h2"}})),
	}
	cc, err := grpc.Dial(maintnerHost, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to dial %q: %w", maintnerHost, err)
	}
	maintnerClient := maintnerpb.NewMaintnerServiceClient(cc)

	resp, err := maintnerClient.ListGoReleases(ctx, &maintnerpb.ListGoReleasesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list go releases: %w", err)
	}
	r.releases = resp.GetReleases()
	r.fetched = time.Now()

	return r.releases, nil
}

var goReleasesCache goReleases

func latestRelease(releases []*maintnerpb.GoRelease) *maintnerpb.GoRelease {
	var highestMajor int32
	var latest *maintnerpb.GoRelease
	for _, release := range releases {
		if release.Major > highestMajor {
			highestMajor = release.Major
			latest = release
		}
	}
	return latest
}

func parsePlatform(platform string) (goos, goarch string, err error) {
	sp := strings.Split(platform, "/")
	switch {
	case len(sp) == 1:
		return "", "", fmt.Errorf("expected a '/'")
	case len(sp) > 2:
		return "", "", fmt.Errorf("expected only one '/'")
	}
	return sp[0], sp[1], nil
}
