// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"golang.org/x/build/internal/influx"
	"golang.org/x/build/perfdata"
	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchseries"
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
			if err != nil {
				// Just log this error. We don't want to quit early if we have other good comparisons.
				log.Printf("%s: %s: %v", c.series, c.benchmarkName, err)
				return true
			}
			measurement := "benchmark-result"                   // measurement
			benchmarkName := c.benchmarkName + cfg.suffix       // tag
			series := c.series                                  // time
			center, low, high := c.Center, c.Low, c.High        // fields
			unit := c.unit                                      // tag
			uploadTime := c.residues["upload-time"]             // field
			cpu := c.residues["cpu"]                            // tag
			goarch := c.residues["goarch"]                      // tag
			goos := c.residues["goos"]                          // tag
			benchmarksCommit := c.residues["benchmarks-commit"] // field
			baselineCommit := c.hashPairs[series].DenHash       // field
			experimentCommit := c.hashPairs[series].NumHash     // field
			repository := c.residues["repository"]              // tag
			branch := c.residues["branch"]                      // tag

			// cmd/bench didn't set repository prior to
			// CL 413915. Older runs are all against go.
			if repository == "" {
				repository = "go"
			}

			// Push to influx.
			t, err := benchseries.ParseNormalizedDateString(series)
			if err != nil {
				log.Printf("%s: %s: error parsing normalized date: %v", c.series, c.benchmarkName, err)
				return true
			}
			fields := map[string]interface{}{
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
				// TODO(prattmic): Add pkg, which
				// benchseries currently can't handle.
			}
			p := influxdb2.NewPoint(measurement, tags, fields, t)
			if err := wapi.WritePoint(ctx, p); err != nil {
				log.Printf("%s: %s: error writing point: %v", c.series, c.benchmarkName, err)
				return true
			}
			return true
		})
	}
	return nil
}

type comparisonConfig struct {
	suffix      string
	compare     string
	numerator   string
	denominator string
	filter      string
}

var (
	pgoOff = comparisonConfig{
		// Default: toolchain:baseline vs experiment without PGO
		compare:     "toolchain",
		numerator:   "experiment",
		denominator: "baseline",
		filter:      "-pgo:on", // "off" or unset (bent doesn't set pgo).
	}
	pgoOn = comparisonConfig{
		// toolchain:baseline vs experiment with PGO
		suffix:      "/pgo=on,toolchain:baseline-vs-experiment",
		compare:     "toolchain",
		numerator:   "experiment",
		denominator: "baseline",
		filter:      "pgo:on",
	}
	pgoVs = comparisonConfig{
		// pgo:off vs on with experiment toolchain (impact of enabling PGO)
		suffix:      "/toolchain:experiment,pgo=off-vs-on",
		compare:     "pgo",
		numerator:   "on",
		denominator: "off",
		filter:      "toolchain:experiment",
	}
)

type comparison struct {
	series        string
	benchmarkName string
	unit          string
	residues      map[string]string
	hashPairs     map[string]benchseries.ComparisonHashes
	*benchseries.ComparisonSummary
}

// benchCompare reads r, assuming it contains benchmark data, and performs the provided comparison
// on the data, returning an iterator over all the resulting summaries.
func benchCompare(rr io.Reader, name string, c comparisonConfig) (func(func(comparison, error) bool), error) {
	r := benchfmt.NewReader(rr, name)

	// Use the default comparisons. Namely:
	// 1. Build a series out of commit dates (in our case, this is length 1).
	// 2. Split out comparisons by benchmark name (unit we get for free).
	//
	// Copy the options for mutation.
	opts := *benchseries.DefaultBuilderOptions()
	opts.Compare = c.compare
	opts.Numerator = c.numerator
	opts.Denominator = c.denominator
	if opts.Filter == "" {
		opts.Filter = c.filter
	} else {
		opts.Filter += " " + c.filter
	}

	// Scan the results into a benchseries builder.
	builder, err := benchseries.NewBuilder(&opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create benchseries builder: %v", err)
	}
	for r.Scan() {
		rec := r.Result()
		if err, ok := rec.(*benchfmt.SyntaxError); ok {
			// Non-fatal result parse error. Warn
			// but keep going.
			log.Printf("Parse error: %v", err)
			continue
		}
		res := rec.(*benchfmt.Result)
		builder.Add(res)
	}
	if err := r.Err(); err != nil {
		return nil, err
	}

	// Run the comparison. We don't have any existing results so our
	// duplicate policy doesn't matter here. Just pick replacement.
	comparisons, err := builder.AllComparisonSeries(nil, benchseries.DUPE_REPLACE)
	if err != nil {
		return nil, fmt.Errorf("failed to creation comparison series: %w", err)
	}

	const (
		confidence = 0.95
		bootstrap  = 1000
	)

	// Iterate over the comparisons and extract the results
	return func(yield func(sum comparison, err error) bool) {
	comparisonLoop:
		for _, cs := range comparisons {
			cs.AddSummaries(confidence, bootstrap)

			summaries := cs.Summaries

			// Build a map of residues with single values. Our benchmark pipeline enforces
			// that the only key that has a differing value across benchmark runs of the same
			// name and unit is "toolchain."
			//
			// Most other keys are singular for *all* benchmarks in a run (like "goos") but
			// even those that are not (like "pkg") remain the same even if "toolchain" differs.
			//
			// We build a map instead of just using them because we need to decide at upload
			// time whether the key is an Influx tag or field.
			residues := make(map[string]string)
			for _, r := range cs.Residues {
				if len(r.Slice) > 1 {
					err := fmt.Errorf("found non-singular key %q with values %v; comparison may be invalid, skipping...", r.S, r.Slice)
					if !yield(comparison{}, err) {
						return
					}
					continue comparisonLoop
				}
				residues[r.S] = r.Slice[0]
			}

			// N.B. In our case Series should have length 1, because we're processing
			// a single result here. By default the string value here is the commit date.
			for i, series := range cs.Series {
				for j, benchmarkName := range cs.Benchmarks {
					sum := summaries[i][j]
					if !sum.Defined() {
						err := fmt.Errorf("summary not defined for %s %s", series, benchmarkName)
						if !yield(comparison{}, err) {
							return
						}
						continue
					}
					if !yield(comparison{series, benchmarkName, cs.Unit, residues, cs.HashPairs, sum}, nil) {
						return
					}
				}
			}
		}
	}, nil
}
