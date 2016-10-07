// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The buildstats command syncs build logs from Datastore to Bigquery.
//
// It will eventually also do more stats.
package main // import "golang.org/x/build/cmd/buildstats"

import (
	"context"
	"flag"
	"log"
	"os"
	"reflect"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/datastore"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/types"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

var (
	doSync = flag.Bool("sync", false, "sync build stats data from Datastore to BigQuery")
)

var env *buildenv.Environment

func main() {
	buildenv.RegisterFlags()
	flag.Parse()

	env = buildenv.FromFlags()

	ctx := context.Background()
	if *doSync {
		syncBuilds(ctx)
		syncSpans(ctx)
	} else {
		log.Fatalf("the buildstats command doesn't yet do anything except the --sync mode")
	}

}

func syncBuilds(ctx context.Context) {
	bq, err := bigquery.NewClient(ctx, env.ProjectName)
	if err != nil {
		log.Fatal(err)
	}
	buildsTable := bq.Dataset("builds").Table("Builds")
	meta, err := buildsTable.Metadata(ctx)
	if err != nil {
		log.Fatalf("Metadata: %v", err)
	}
	log.Printf("Metadata: %#v", meta)
	for i, fs := range meta.Schema {
		log.Printf("  schema[%v]: %+v", i, fs)
		for j, fs := range fs.Schema {
			log.Printf("     .. schema[%v]: %+v", j, fs)
		}
	}

	q := bq.Query("SELECT MAX(EndTime) FROM [symbolic-datum-552:builds.Builds]")
	it, err := q.Read(ctx)
	if err != nil {
		log.Fatalf("Read: %v", err)
	}
	var values []bigquery.Value
	err = it.Next(&values)
	if err == iterator.Done {
		log.Fatalf("No result.")
	}
	if err != nil {
		log.Fatalf("Next: %v", err)
	}
	t, ok := values[0].(time.Time)
	if !ok {
		log.Fatalf("not a time")
	}
	log.Printf("Max is %v (%v)", t, t.Location())

	ds, err := datastore.NewClient(ctx, env.ProjectName)
	if err != nil {
		log.Fatalf("datastore.NewClient: %v", err)
	}

	up := buildsTable.Uploader()

	log.Printf("Max: %v", t)
	dsit := ds.Run(ctx, datastore.NewQuery("Build").Filter("EndTime >", t).Filter("EndTime <", t.Add(24*90*time.Hour)).Order("EndTime"))
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
				log.Fatal(err)
			}
			if s.EndTime.IsZero() {
				log.Fatalf("got zero endtime")
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
			log.Printf("Done.")
			return
		}
		err = up.Put(ctx, rows)
		log.Printf("Put %d rows, up to %v. error = %v", len(rows), maxPut, err)
		if err != nil {
			os.Exit(1)
		}
	}
}

func syncSpans(ctx context.Context) {
	bq, err := bigquery.NewClient(ctx, env.ProjectName)
	if err != nil {
		log.Fatal(err)
	}
	table := bq.Dataset("builds").Table("Spans")
	meta, err := table.Metadata(ctx)
	if ae, ok := err.(*googleapi.Error); ok && ae.Code == 404 {
		log.Printf("Creating table Spans...")
		err = table.Create(ctx)
		if err == nil {
			meta, err = table.Metadata(ctx)
		}
	}
	if err != nil {
		log.Fatalf("Metadata: %#v", err)
	}
	log.Printf("Metadata: %#v", meta)
	schema := meta.Schema
	if len(schema) == 0 {
		log.Printf("EMPTY SCHEMA")
		schema, err = bigquery.InferSchema(types.SpanRecord{})
		if err != nil {
			log.Fatalf("InferSchema: %v", err)
		}
		meta, err := table.Update(ctx, bigquery.TableMetadataToUpdate{Schema: schema})
		if err != nil {
			log.Fatalf("table.Update schema: %v", err)
		}
		schema = meta.Schema
	}
	for i, fs := range schema {
		log.Printf("  schema[%v]: %+v", i, fs)
		for j, fs := range fs.Schema {
			log.Printf("     .. schema[%v]: %+v", j, fs)
		}
	}

	q := bq.Query("SELECT MAX(EndTime) FROM [symbolic-datum-552:builds.Spans]")
	it, err := q.Read(ctx)
	if err != nil {
		log.Fatalf("Read: %v", err)
	}

	var since time.Time
	var values []bigquery.Value
	if err := it.Next(&values); err != nil {
		if err == iterator.Done {
			log.Fatalf("Expected at least one row fro MAX(EndTime) query; got none.")
		}
		log.Fatalf("Next: %v", err)
	}
	switch t := values[0].(type) {
	case nil:
		// NULL. No rows.
		log.Printf("starting from the beginning...")
	case time.Time:
		since = values[0].(time.Time)
	default:
		log.Fatalf("MAX(EndType) = %T: want nil or time.Time", t)
	}
	if since.IsZero() {
		since = time.Unix(1, 0) // arbitrary
	}

	ds, err := datastore.NewClient(ctx, env.ProjectName)
	if err != nil {
		log.Fatalf("datastore.NewClient: %v", err)
	}

	up := table.Uploader()

	log.Printf("Max: %v", since)
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
				log.Fatalf("got zero endtime")
			}
			//log.Printf("need to add %s: %+v", key.Encode(), s)

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
			log.Printf("Done.")
			return
		}
		err = up.Put(ctx, rows)
		log.Printf("Put %d rows, up to %v. error = %v", len(rows), maxPut, err)
		if err != nil {
			os.Exit(1)
		}
	}
}
