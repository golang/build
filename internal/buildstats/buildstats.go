// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package buildstats contains code to sync the coordinator's build
// logs from Datastore to BigQuery.
package buildstats // import "golang.org/x/build/internal/buildstats"
import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/datastore"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/types"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

// Verbose controls logging verbosity.
var Verbose = false

// SyncBuilds syncs the datastore "Build" entities to the BigQuery "Builds" table.
// This stores information on each build as a whole, without details.
func SyncBuilds(ctx context.Context, env *buildenv.Environment) error {
	bq, err := bigquery.NewClient(ctx, env.ProjectName)
	if err != nil {
		return err
	}
	defer bq.Close()

	buildsTable := bq.Dataset("builds").Table("Builds")
	meta, err := buildsTable.Metadata(ctx)
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == 404 {
		if Verbose {
			log.Printf("Creating table Builds...")
		}
		err = buildsTable.Create(ctx, nil)
		if err == nil {
			meta, err = buildsTable.Metadata(ctx)
		}
	}
	if err != nil {
		return fmt.Errorf("getting Builds table metadata: %v", err)
	}
	if Verbose {
		log.Printf("buildstats: Builds metadata: %#v", meta)
	}
	if len(meta.Schema) == 0 {
		if Verbose {
			log.Printf("buildstats: builds table has empty schema")
		}
		schema, err := bigquery.InferSchema(types.BuildRecord{})
		if err != nil {
			return fmt.Errorf("InferSchema: %v", err)
		}
		blindWrite := ""
		meta, err = buildsTable.Update(ctx, bigquery.TableMetadataToUpdate{Schema: schema}, blindWrite)
		if err != nil {
			return fmt.Errorf("table.Update schema: %v", err)
		}
	}
	if Verbose {
		for i, fs := range meta.Schema {
			log.Printf("  schema[%v]: %+v", i, fs)
			for j, fs := range fs.Schema {
				log.Printf("     .. schema[%v]: %+v", j, fs)
			}
		}
	}

	q := bq.Query("SELECT MAX(EndTime) FROM builds.Builds")
	it, err := q.Read(ctx)
	if err != nil {
		return fmt.Errorf("Read: %v", err)
	}
	var values []bigquery.Value
	err = it.Next(&values)
	if err == iterator.Done {
		return fmt.Errorf("No result.")
	}
	if err != nil {
		return fmt.Errorf("Next: %v", err)
	}
	var since time.Time
	switch t := values[0].(type) {
	case nil:
		// NULL. No rows.
		if Verbose {
			log.Printf("buildstats: syncing Builds from the beginning")
		}
	case time.Time:
		since = values[0].(time.Time)
	default:
		return fmt.Errorf("MAX(EndType) = %T: want nil or time.Time", t)
	}

	if Verbose {
		log.Printf("Max is %v (%v)", since, since.Location())
	}

	ds, err := datastore.NewClient(ctx, env.ProjectName)
	if err != nil {
		return fmt.Errorf("datastore.NewClient: %v", err)
	}
	defer ds.Close()

	up := buildsTable.Uploader()

	if Verbose {
		log.Printf("buildstats: Builds max time: %v", since)
	}
	dsq := datastore.NewQuery("Build")
	if !since.IsZero() {
		dsq = dsq.Filter("EndTime >", since).Filter("EndTime <", since.Add(24*90*time.Hour))
	} else {
		// Ignore rows without endtime.
		dsq = dsq.Filter("EndTime >", time.Unix(1, 0))
	}
	dsq = dsq.Order("EndTime")
	dsit := ds.Run(ctx, dsq)
	var maxPut time.Time
	for {
		n := 0
		var rows []*bigquery.ValuesSaver
		for {
			var s types.BuildRecord
			key, err := dsit.Next(&s)
			if err == iterator.Done {
				break
			}
			n++
			if err != nil {
				return fmt.Errorf("error querying max EndTime: %v", err)
			}
			if s.EndTime.IsZero() {
				return fmt.Errorf("got zero EndTime")
			}

			var row []bigquery.Value
			var putSchema bigquery.Schema
			rv := reflect.ValueOf(s)
			for _, fs := range meta.Schema {
				if fs.Name[0] == '_' {
					continue
				}
				putSchema = append(putSchema, fs)
				row = append(row, rv.FieldByName(fs.Name).Interface())
				maxPut = s.EndTime
			}

			rows = append(rows, &bigquery.ValuesSaver{
				Schema:   putSchema,
				InsertID: key.Encode(),
				Row:      row,
			})
			if len(rows) == 1000 {
				break
			}
		}
		if n == 0 {
			return nil
		}
		err = up.Put(ctx, rows)
		log.Printf("buildstats: Build sync put %d rows, up to %v. error = %v", len(rows), maxPut, err)
		if err != nil {
			return err
		}
	}
}

// SyncSpans syncs the datastore "Span" entities to the BigQuery "Spans" table.
// These contain the fine-grained timing details of how a build ran.
func SyncSpans(ctx context.Context, env *buildenv.Environment) error {
	bq, err := bigquery.NewClient(ctx, env.ProjectName)
	if err != nil {
		log.Fatal(err)
	}
	defer bq.Close()

	table := bq.Dataset("builds").Table("Spans")
	meta, err := table.Metadata(ctx)
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == 404 {
		log.Printf("Creating table Spans...")
		err = table.Create(ctx, nil)
		if err == nil {
			meta, err = table.Metadata(ctx)
		}
	}
	if err != nil {
		return fmt.Errorf("Metadata: %#v", err)
	}
	if Verbose {
		log.Printf("buildstats: Spans metadata: %#v", meta)
	}
	schema := meta.Schema
	if len(schema) == 0 {
		if Verbose {
			log.Printf("EMPTY SCHEMA")
		}
		schema, err = bigquery.InferSchema(types.SpanRecord{})
		if err != nil {
			return fmt.Errorf("InferSchema: %v", err)
		}
		blindWrite := ""
		meta, err := table.Update(ctx, bigquery.TableMetadataToUpdate{Schema: schema}, blindWrite)
		if err != nil {
			return fmt.Errorf("table.Update schema: %v", err)
		}
		schema = meta.Schema
	}
	if Verbose {
		for i, fs := range schema {
			log.Printf("  schema[%v]: %+v", i, fs)
			for j, fs := range fs.Schema {
				log.Printf("     .. schema[%v]: %+v", j, fs)
			}
		}
	}

	q := bq.Query("SELECT MAX(EndTime) FROM builds.Spans")
	it, err := q.Read(ctx)
	if err != nil {
		return fmt.Errorf("Read: %v", err)
	}

	var since time.Time
	var values []bigquery.Value
	if err := it.Next(&values); err != nil {
		if err == iterator.Done {
			return fmt.Errorf("Expected at least one row fro MAX(EndTime) query; got none.")
		}
		return fmt.Errorf("Next: %v", err)
	}
	switch t := values[0].(type) {
	case nil:
		// NULL. No rows.
		log.Printf("starting from the beginning...")
	case time.Time:
		since = values[0].(time.Time)
	default:
		return fmt.Errorf("MAX(EndType) = %T: want nil or time.Time", t)
	}
	if since.IsZero() {
		since = time.Unix(1, 0) // arbitrary
	}

	ds, err := datastore.NewClient(ctx, env.ProjectName)
	if err != nil {
		return fmt.Errorf("datastore.NewClient: %v", err)
	}
	defer ds.Close()

	up := table.Uploader()

	if Verbose {
		log.Printf("buildstats: Span max time: %v", since)
	}
	dsit := ds.Run(ctx, datastore.NewQuery("Span").Filter("EndTime >", since).Order("EndTime"))
	var maxPut time.Time
	for {
		n := 0
		var rows []*bigquery.ValuesSaver
		for {
			var s types.SpanRecord
			key, err := dsit.Next(&s)
			if err == iterator.Done {
				break
			}
			n++
			if err != nil {
				log.Fatal(err)
			}
			if s.EndTime.IsZero() {
				return fmt.Errorf("got zero endtime")
			}

			var row []bigquery.Value
			var putSchema bigquery.Schema
			rv := reflect.ValueOf(s)
			for _, fs := range meta.Schema {
				if fs.Name[0] == '_' {
					continue
				}
				putSchema = append(putSchema, fs)
				row = append(row, rv.FieldByName(fs.Name).Interface())
				maxPut = s.EndTime
			}

			rows = append(rows, &bigquery.ValuesSaver{
				Schema:   putSchema,
				InsertID: key.Encode(),
				Row:      row,
			})
			if len(rows) == 1000 {
				break
			}
		}
		if n == 0 {
			return nil
		}
		err = up.Put(ctx, rows)
		log.Printf("buildstats: Spans sync put %d rows, up to %v. error = %v", len(rows), maxPut, err)
		if err != nil {
			return err
		}
	}
}

// TestStats describes stats for a cmd/dist test on a particular build
// configuration (a "builder").
type TestStats struct {
	// AsOf is the time that the stats were queried from BigQuery.
	AsOf time.Time

	// BuilderTestStats maps from a builder name to that builder's
	// test stats.
	BuilderTestStats map[string]*BuilderTestStats
}

// Duration returns the median time to run testName on builder, if known.
// Otherwise it returns some non-zero default value.
func (ts *TestStats) Duration(builder, testName string) time.Duration {
	if ts != nil {
		if bs, ok := ts.BuilderTestStats[builder]; ok {
			if d, ok := bs.MedianDuration[testName]; ok {
				return d
			}
		}
	}
	return 3 * time.Second // some arbitrary value if unknown
}

func (ts *TestStats) Builders() []string {
	s := make([]string, 0, len(ts.BuilderTestStats))
	for k := range ts.BuilderTestStats {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

type BuilderTestStats struct {
	// Builder is which build configuration this is for.
	Builder string

	// Runs is how many times tests have run recently, for some
	// fuzzy definition of "recently".
	// The map key is a cmd/dist test name.
	Runs map[string]int

	// MedianDuration is the median duration for a test to
	// pass on this BuilderTestStat's Builder.
	// The map key is a cmd/dist test name.
	MedianDuration map[string]time.Duration
}

func (ts *BuilderTestStats) Tests() []string {
	s := make([]string, 0, len(ts.Runs))
	for k := range ts.Runs {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

// QueryTestStats returns stats on all tests for all builders.
func QueryTestStats(ctx context.Context, env *buildenv.Environment) (*TestStats, error) {
	ts := &TestStats{
		AsOf:             time.Now(),
		BuilderTestStats: map[string]*BuilderTestStats{},
	}
	bq, err := bigquery.NewClient(ctx, env.ProjectName)
	if err != nil {
		return nil, err
	}
	defer bq.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q := bq.Query(`
SELECT
    Builder, Event, APPROX_QUANTILES(Seconds, 100)[OFFSET(50)] as MedianSec, COUNT(*) as N
FROM
    builds.Spans
WHERE
    Error='' AND
    StartTime > TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 500 HOUR)
    AND Repo = "go"
    AND Event LIKE 'run_test:%'
GROUP BY 1, 2
`)
	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	n := 0
	for {
		var row struct {
			Builder   string
			Event     string
			MedianSec float64
			N         int
		}
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		n++
		if n > 50000 {
			break
		}
		bs := ts.BuilderTestStats[row.Builder]
		if bs == nil {
			bs = &BuilderTestStats{
				Builder:        row.Builder,
				Runs:           map[string]int{},
				MedianDuration: map[string]time.Duration{},
			}
			ts.BuilderTestStats[row.Builder] = bs
		}
		distTest := strings.TrimPrefix(row.Event, "run_test:")
		bs.Runs[distTest] = row.N
		bs.MedianDuration[distTest] = time.Duration(row.MedianSec * 1e9)
	}
	return ts, nil
}
