// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"golang.org/x/build/internal/influx"
	"golang.org/x/build/perf/app/internal/benchtab"
	"golang.org/x/build/perfdata"
	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
	"golang.org/x/perf/benchproc"
	"golang.org/x/perf/benchunit"
	"google.golang.org/api/idtoken"
)

const (
	backfillWindow = 30 * 24 * time.Hour // 30 days.
)

func (a *App) influxClient(ctx context.Context) (influxdb2.Client, error) {
	if a.InfluxHost == "" {
		return nil, fmt.Errorf("Influx host unknown (set INFLUX_HOST?)")
	}

	token, err := a.findInfluxToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("error finding Influx token: %w", err)
	}

	return influxdb2.NewClient(a.InfluxHost, token), nil
}

// syncInflux handles /cron/syncinflux, which updates an InfluxDB instance with
// the latest data from perfdata.golang.org (i.e. storage), or backfills it.
func (a *App) syncInflux(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if a.AuthCronEmail != "" {
		if err := checkCronAuth(ctx, r, a.AuthCronEmail); err != nil {
			log.Printf("Dropping invalid request to /cron/syncinflux: %v", err)
			http.Error(w, err.Error(), 403)
			return
		}
	}

	ifxc, err := a.influxClient(ctx)
	if err != nil {
		log.Printf("Error getting Influx client: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	defer ifxc.Close()

	log.Printf("Connecting to influx...")

	lastPush, err := latestInfluxTimestamp(ctx, ifxc)
	if err != nil {
		log.Printf("Error getting latest Influx timestamp: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}
	if lastPush.IsZero() {
		// Pick the backfill window.
		lastPush = time.Now().Add(-backfillWindow)
	}

	log.Printf("Last push to influx: %v", lastPush)

	uploads, err := a.uploadsSince(ctx, lastPush)
	if err != nil {
		log.Printf("Error getting new uploads: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}

	log.Printf("Uploads since last push: %d", len(uploads))

	var errs []error
	for _, u := range uploads {
		log.Printf("Processing upload %s...", u.UploadID)
		if err := a.pushRunToInflux(ctx, ifxc, u); err != nil {
			errs = append(errs, err)
			log.Printf("Error processing upload %s: %v", u.UploadID, err)
		}
	}
	if len(errs) > 0 {
		var failures strings.Builder
		for _, err := range errs {
			failures.WriteString(err.Error())
			failures.WriteString("\n")
		}
		http.Error(w, failures.String(), 500)
	}
}

func checkCronAuth(ctx context.Context, r *http.Request, wantEmail string) error {
	const audience = "/cron/syncinflux"

	const authHeaderPrefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, authHeaderPrefix) {
		return fmt.Errorf("missing Authorization header")
	}
	token := authHeader[len(authHeaderPrefix):]

	p, err := idtoken.Validate(ctx, token, audience)
	if err != nil {
		return err
	}

	if p.Issuer != "https://accounts.google.com" {
		return fmt.Errorf("issuer must be https://accounts.google.com, but is %s", p.Issuer)
	}

	e, ok := p.Claims["email"]
	if !ok {
		return fmt.Errorf("email missing from token")
	}
	email, ok := e.(string)
	if !ok {
		return fmt.Errorf("email unexpected type %T", e)
	}

	if email != wantEmail {
		return fmt.Errorf("email got %s want %s", email, wantEmail)
	}

	return nil
}

func (a *App) findInfluxToken(ctx context.Context) (string, error) {
	if a.InfluxToken != "" {
		return a.InfluxToken, nil
	}

	var project string
	if a.InfluxProject != "" {
		project = a.InfluxProject
	} else {
		var err error
		project, err = metadata.ProjectID()
		if err != nil {
			return "", fmt.Errorf("error determining GCP project ID (set INFLUX_TOKEN or INFLUX_PROJECT?): %w", err)
		}
	}

	log.Printf("Fetching Influx token from %s...", project)

	token, err := fetchInfluxToken(ctx, project)
	if err != nil {
		return "", fmt.Errorf("error fetching Influx token: %w", err)
	}

	return token, nil
}

func fetchInfluxToken(ctx context.Context, project string) (string, error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("error creating secret manager client: %w", err)
	}
	defer client.Close()

	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: "projects/" + project + "/secrets/" + influx.AdminTokenSecretName + "/versions/latest",
	}

	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to access secret version: %w", err)
	}

	return string(result.Payload.Data), nil
}

func latestInfluxTimestamp(ctx context.Context, ifxc influxdb2.Client) (time.Time, error) {
	qc := ifxc.QueryAPI(influx.Org)
	// Find the latest upload in the last month.
	q := fmt.Sprintf(`from(bucket:%q)
		|> range(start: -%dh)
		|> filter(fn: (r) => r["_measurement"] == "benchmark-result")
		|> filter(fn: (r) => r["_field"] == "upload-time")
		|> group()
		|> sort(columns: ["_value"], desc: true)
		|> limit(n: 1)`, influx.Bucket, backfillWindow/time.Hour)
	result, err := influxQuery(ctx, qc, q)
	if err != nil {
		return time.Time{}, err
	}
	for result.Next() {
		// Except for the point timestamp, all other timestamps are stored as strings, specifically
		// as the RFC3339Nano format.
		//
		// We only care about the first result, and there should be just one.
		return time.Parse(time.RFC3339Nano, result.Record().Value().(string))
	}
	return time.Time{}, result.Err()
}

func (a *App) uploadsSince(ctx context.Context, since time.Time) ([]perfdata.UploadInfo, error) {
	query := strings.Join([]string{
		// Limit results to the window from since to now.
		"upload-time>" + since.UTC().Format(time.RFC3339),
		// Only take results generated by the coordinator. This ensures that nobody can
		// just upload data to perfdata.golang.org and spoof us (accidentally or intentionally).
		"by:public-worker-builder@golang-ci-luci.iam.gserviceaccount.com",
		// Only take results that were generated from post-submit runs, not trybots.
		"post-submit:true",
	}, " ")
	uploadList := a.StorageClient.ListUploads(
		ctx,
		query,
		nil,
		500, // TODO(mknyszek): page results if this isn't enough.
	)
	defer uploadList.Close()

	var uploads []perfdata.UploadInfo
	for uploadList.Next() {
		uploads = append(uploads, uploadList.Info())
	}
	if err := uploadList.Err(); err != nil {
		return nil, err
	}
	return uploads, nil
}

func (a *App) pushRunToInflux(ctx context.Context, ifxc influxdb2.Client, u perfdata.UploadInfo) error {
	s, err := a.StorageClient.Query(ctx, fmt.Sprintf("upload:%s", u.UploadID))
	if err != nil {
		return err
	}

	// We need to read the upload multiple times via benchfmt.Reader, so
	// copy to a buffer we can seek back to the beginning.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, s); err != nil {
		return fmt.Errorf("error reading upload: %w", err)
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("error closing upload: %w", err)
	}

	// Iterate over the comparisons, extract the results, and push them to Influx.
	wapi := ifxc.WriteAPIBlocking(influx.Org, influx.Bucket)
	for _, cfg := range []comparisonConfig{pgoOff, pgoOn, pgoVs} {
		r := bytes.NewReader(buf.Bytes())
		forAll, err := benchCompare(r, u.UploadID, cfg)
		if err != nil {
			return fmt.Errorf("performing comparison for %s: %v", u.UploadID, err)
		}
		// TODO(mknyszek): Use new iterators when possible.
		forAll(func(c comparison, err error) bool {
			comparisonID := c.keys["experiment-commit"]
			if err != nil {
				// Just log this error. We don't want to quit early if we have other good comparisons.
				log.Printf("error: %s: %s: %s: %v", comparisonID, c.benchmarkName, c.unit, err)
				return true
			}
			measurement := "benchmark-result"               // measurement
			benchmarkName := c.benchmarkName + cfg.suffix   // tag
			timestamp := c.keys["experiment-commit-time"]   // time
			center, low, high := c.center, c.lo, c.hi       // fields
			unit := c.unit                                  // tag
			uploadTime := c.keys["upload-time"]             // field
			cpu := c.keys["cpu"]                            // tag
			goarch := c.keys["goarch"]                      // tag
			goos := c.keys["goos"]                          // tag
			benchmarksCommit := c.keys["benchmarks-commit"] // field
			baselineCommit := c.keys["baseline-commit"]     // field
			experimentCommit := c.keys["experiment-commit"] // field
			repository := c.keys["repository"]              // tag
			branch := c.keys["branch"]                      // tag

			// cmd/bench didn't set repository prior to
			// CL 413915. Older runs are all against go.
			if repository == "" {
				repository = "go"
			}

			// Push to influx.
			t, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				log.Printf("error: %s: %s: %s: parsing experiment-commit-time: %v", comparisonID, c.benchmarkName, c.unit, err)
				return true
			}
			fields := map[string]any{
				"center":            center,
				"low":               low,
				"high":              high,
				"upload-time":       uploadTime,
				"benchmarks-commit": benchmarksCommit,
				"baseline-commit":   baselineCommit,
				"experiment-commit": experimentCommit,
			}
			tags := map[string]string{
				"name":       benchmarkName,
				"unit":       unit,
				"cpu":        cpu,
				"goarch":     goarch,
				"goos":       goos,
				"repository": repository,
				"branch":     branch,
				// TODO(mknyszek): Revisit adding pkg, now that we're not using benchseries.
			}
			p := influxdb2.NewPoint(measurement, tags, fields, t)
			if err := wapi.WritePoint(ctx, p); err != nil {
				log.Printf("%s: %s: %s: error writing point: %v", comparisonID, c.benchmarkName, c.unit, err)
				return true
			}
			return true
		})
	}
	return nil
}

type comparisonConfig struct {
	suffix     string
	columnExpr string
	filter     string
	ignore     string
}

var (
	pgoOff = comparisonConfig{
		// Default: toolchain:baseline vs experiment without PGO
		columnExpr: "toolchain@(baseline experiment)",
		filter:     "-pgo:on", // "off" or unset (bent doesn't set pgo).
		ignore:     "pgo",     // Ignore pgo entirely; bent doesn't set it, which messes up the geomean.
	}
	pgoOn = comparisonConfig{
		// toolchain:baseline vs experiment with PGO
		suffix:     "/pgo=on,toolchain:baseline-vs-experiment",
		columnExpr: "toolchain@(baseline experiment)",
		filter:     "pgo:on",
	}
	pgoVs = comparisonConfig{
		// pgo:off vs on with experiment toolchain (impact of enabling PGO)
		suffix:     "/toolchain:experiment,pgo=off-vs-on",
		columnExpr: "pgo@(off on)",
		filter:     "toolchain:experiment",
	}
)

type comparison struct {
	benchmarkName  string
	unit           string
	keys           map[string]string
	lo, center, hi float64
}

// benchCompare reads r, assuming it contains benchmark data, and performs the provided comparison
// on the data, returning an iterator over all the resulting summaries.
func benchCompare(rr io.Reader, name string, c comparisonConfig) (func(func(comparison, error) bool), error) {
	r := benchfmt.NewReader(rr, name)

	filter, err := benchproc.NewFilter(c.filter)
	if err != nil {
		return nil, fmt.Errorf("parsing filter: %s", err)
	}

	var parser benchproc.ProjectionParser
	var parseErr error
	mustParse := func(name, val string, unit bool) *benchproc.Projection {
		var proj *benchproc.Projection
		var err error
		if unit {
			proj, _, err = parser.ParseWithUnit(val, filter)
		} else {
			proj, err = parser.Parse(val, filter)
		}
		if err != nil && parseErr == nil {
			parseErr = fmt.Errorf("parsing %s: %s", name, err)
		}
		return proj
	}
	tableBy := mustParse("table", ".config", true)
	rowBy := mustParse("row", ".fullname", false)
	colBy := mustParse("col", c.columnExpr, false)
	ignore := "go,tip,base,bentstamp,shortname,suite,pkg"
	if c.ignore != "" {
		ignore += "," + c.ignore
	}
	mustParse("ignore", ignore, false)
	residue := parser.Residue()

	// Check parse error.
	if parseErr != nil {
		return nil, fmt.Errorf("internal error: failed to parse projections for configuration: %v", parseErr)
	}

	// Scan the results into a benchseries builder.
	stat := benchtab.NewBuilder(tableBy, rowBy, colBy, residue)
	for r.Scan() {
		switch rec := r.Result(); rec := rec.(type) {
		case *benchfmt.SyntaxError:
			// Non-fatal result parse error. Warn
			// but keep going.
			log.Printf("Parse error: %v", err)
		case *benchfmt.Result:
			if ok, _ := filter.Apply(rec); !ok {
				continue
			}
			stat.Add(rec)
		}
	}
	if err := r.Err(); err != nil {
		return nil, err
	}

	// Prepopulate some assumptions about binary size units.
	// bent does emit these, but they get stripped by perfdata.
	// TODO(mknyszek): Remove this once perfdata stops doing that.
	units := r.Units()
	assumeExact := func(unit string) {
		_, tidyUnit := benchunit.Tidy(1, unit)
		key := benchfmt.UnitMetadataKey{Unit: tidyUnit, Key: "assume"}
		if _, ok := units[key]; ok {
			return // There was an assumption in the benchmark data.
		}
		units[key] = &benchfmt.UnitMetadata{
			UnitMetadataKey: key,
			OrigUnit:        unit,
			Value:           "exact",
		}
	}
	assumeExact("total-bytes")
	assumeExact("text-bytes")
	assumeExact("data-bytes")
	assumeExact("rodata-bytes")
	assumeExact("pclntab-bytes")
	assumeExact("debug-bytes")

	// Build the comparison table.
	const confidence = 0.95
	thresholds := benchmath.DefaultThresholds
	tables := stat.ToTables(benchtab.TableOpts{
		Confidence: confidence,
		Thresholds: &thresholds,
		Units:      r.Units(),
	})

	// Iterate over the comparisons and extract the results
	return func(yield func(sum comparison, err error) bool) {
		for t, table := range tables.Tables {
			// All the other keys, which should be identical, are captured as
			// sub-fields of .config, our table projection.
			keys := make(map[string]string)
			for _, f := range tableBy.Fields()[0].Sub {
				keys[f.Name] = tables.Keys[t].Get(f)
			}
			for _, row := range table.Rows {
				benchmarkName := row.StringValues()
				for _, col := range table.Cols {
					cell, ok := table.Cells[benchtab.TableKey{Row: row, Col: col}]
					if !ok {
						// Cell not present due to missing data.
						err := fmt.Errorf("summary not defined %s", benchmarkName)
						if !yield(comparison{}, err) {
							return
						}
						continue
					}
					if cell.Baseline == nil {
						// Non-comparison cell.
						continue
					}
					if len(cell.Summary.Warnings) != 0 {
						// TODO(mknyszek): Make this an actual failure once it stops failing for x/tools.
						// x/tools has 5 runs per benchmark, but we need 6 for 0.95 confidence.
						log.Printf("warning: %s: %s: %s: %v", name, benchmarkName, table.Unit, errors.Join(cell.Summary.Warnings...))
					}
					lo, center, hi := ratioSummary(cell.Baseline.Sample, cell.Sample, confidence, 1000)
					if !yield(comparison{benchmarkName, table.Unit, keys, lo, center, hi}, nil) {
						return
					}
				}
			}
			// Emit the geomean as a separate timeline.
			//
			// The "benchmark name" for this will look something like "geomean/go/gotip/vs_release/c2s16".
			builder, ok := keys["builder"]
			if !ok {
				log.Printf("warning: not emitting summary, no builder value found")
				continue
			}
			repo, mod, machineType, err := parseBuilder(builder)
			if err != nil {
				log.Printf("warning: not emitting summary, can't interpret builder: %v", err)
				continue
			}
			if table.SummaryLabel != "geomean" {
				log.Printf("warning: not emitting summary, summary is not a geomean")
				continue
			}
			emitted := false
			for _, col := range table.Cols {
				sum, ok := table.Summary[col]
				if !ok {
					continue
				}
				if sum.HasRatio {
					geomeanName := table.SummaryLabel + "/" + repo + "/" + mod + "/" + machineType
					if !yield(comparison{geomeanName, table.Unit, keys, sum.Ratio, sum.Ratio, sum.Ratio}, nil) {
						return
					}
					emitted = true
					break
				}
			}
			if !emitted {
				log.Printf("warning: not emitting summary, failed to find ratio")
			}
		}
	}, nil
}

func parseBuilder(builder string) (repo, mod, machineType string, err error) {
	if strings.HasPrefix(builder, "x_") || strings.HasPrefix(builder, "z_") {
		// Repository.
		m := strings.SplitN(builder, "-", 2)
		if len(m) != 2 {
			return "", "", "", fmt.Errorf("unexpected builder name: %s", builder)
		}
		repo = m[0]
		builder = m[1]
	} else {
		repo = "go"
	}
	components := strings.SplitN(builder, "-", 4)
	// components[0] -> Go branch name, already handled elsewhere
	// components[1] -> GOOS, already handled elsewhere
	// components[2] -> GOARCH, mostly handled, but there's a machine specifier here after an _
	// components[3] -> rest of the builder name; perf builders will have this start with -perf.
	if len(components) != 4 {
		return "", "", "", fmt.Errorf("unexpected builder name: %s", builder)
	}

	// Extract the machine type (platform specifier).
	m := strings.SplitN(components[2], "_", 2)
	if len(m) == 2 {
		machineType = m[1]
	}
	// Extract the run mods suffix.
	if rest, ok := strings.CutPrefix(components[3], "perf_"); ok {
		mod = rest
	} else {
		return "", "", "", fmt.Errorf("unexpected builder name: missing -perf as first run mod: %s", builder)
	}
	return
}

func ratioSummary(baseline, experiment *benchmath.Sample, confidence float64, bootstrapN int) (lo, center, hi float64) {
	ratios := make([]float64, bootstrapN)
	sampleNum := make([]float64, len(experiment.Values))
	sampleDen := make([]float64, len(baseline.Values))
	for i := range ratios {
		resampleInto(experiment.Values, sampleNum)
		resampleInto(baseline.Values, sampleDen)
		den := median(sampleDen)
		if den == 0 {
			num := median(sampleNum)
			if num >= 0 {
				ratios[i] = (num + 1)
			} else {
				ratios[i] = (num - 1)
			}
		} else {
			ratios[i] = median(sampleNum) / den
		}
	}
	slices.Sort(ratios)
	p := (1 - confidence) / 2
	lo = percentile(ratios, p)
	hi = percentile(ratios, 1-p)
	center = median(ratios)
	return
}

func percentile(a []float64, p float64) float64 {
	if len(a) == 0 {
		return math.NaN()
	}
	if p == 0 {
		return a[0]
	}
	n := len(a)
	if p == 1 {
		return a[n-1]
	}
	f := float64(float64(n) * p) // Suppress fused-multiply-add
	i := int(f)
	x := f - float64(i)
	r := a[i]
	if x > 0 && i+1 < len(a) {
		r = float64(r*(1-x)) + float64(a[i+1]*x) // Suppress fused-multiply-add
	}
	return r
}

func median(a []float64) float64 {
	l := len(a)
	if l&1 == 1 {
		return a[l/2]
	}
	return (a[l/2] + a[l/2-1]) / 2
}

func norm(a []float64, l float64) float64 {
	if len(a) == 0 {
		return math.NaN()
	}
	n := 0.0
	sum := 0.0
	for _, x := range a {
		if math.IsInf(x, 0) || math.IsNaN(x) {
			continue
		}
		sum += math.Pow(math.Abs(x), l)
		n++
	}
	return math.Pow(sum/n, 1/l)
}

func resampleInto(sample, dst []float64) {
	for i := range dst {
		dst[i] = sample[rand.N[int](len(sample))]
	}
	slices.Sort(dst)
}
