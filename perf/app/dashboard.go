// Copyright 2022 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api"
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

// fetch queries Influx to fill Values. Name and Unit must be set.
//
// WARNING: Name and Unit are not sanitized. DO NOT pass user input.
func (b *BenchmarkJSON) fetch(ctx context.Context, qc api.QueryAPI) error {
	if b.Name == "" {
		return fmt.Errorf("Name must be set")
	}
	if b.Unit == "" {
		return fmt.Errorf("Unit must be set")
	}

	// TODO(prattmic): Adjust UI to comfortably display more than 7d of
	// data.
	query := fmt.Sprintf(`
from(bucket: "perf")
  |> range(start: -7d)
  |> filter(fn: (r) => r["_measurement"] == "benchmark-result")
  |> filter(fn: (r) => r["name"] == "%s")
  |> filter(fn: (r) => r["unit"] == "%s")
  |> filter(fn: (r) => r["branch"] == "master")
  |> filter(fn: (r) => r["goos"] == "linux")
  |> filter(fn: (r) => r["goarch"] == "amd64")
  |> pivot(columnKey: ["_field"], rowKey: ["_time"], valueColumn: "_value")
  |> yield(name: "last")
`, b.Name, b.Unit)

	ir, err := qc.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("error performing query: %W", err)
	}

	for ir.Next() {
		rec := ir.Record()

		low, ok := rec.ValueByKey("low").(float64)
		if !ok {
			return fmt.Errorf("record %s low value got type %T want float64", rec, rec.ValueByKey("low"))
		}

		center, ok := rec.ValueByKey("center").(float64)
		if !ok {
			return fmt.Errorf("record %s center value got type %T want float64", rec, rec.ValueByKey("center"))
		}

		high, ok := rec.ValueByKey("high").(float64)
		if !ok {
			return fmt.Errorf("record %s high value got type %T want float64", rec, rec.ValueByKey("high"))
		}

		commit, ok := rec.ValueByKey("experiment-commit").(string)
		if !ok {
			return fmt.Errorf("record %s experiment-commit value got type %T want float64", rec, rec.ValueByKey("experiment-commit"))
		}

		b.Values = append(b.Values, ValueJSON{
			CommitDate: rec.Time(),
			CommitHash: commit,
			Low:        low - 1,
			Center:     center - 1,
			High:       high - 1,
		})
	}

	sort.Slice(b.Values, func(i, j int) bool {
		return b.Values[i].CommitDate.Before(b.Values[j].CommitDate)
	})

	return nil
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

	// Keep benchmarks with the same name grouped together, which is
	// assumed by the JS.
	//
	// WARNING: Name and Unit are not sanitized. DO NOT pass user input.
	benchmarks := []BenchmarkJSON{
		{
			Name: "Tile38WithinCircle100kmRequest",
			Unit: "sec/op",
		},
		{
			Name: "Tile38WithinCircle100kmRequest",
			Unit: "p90-latency-sec",
		},
		{
			Name: "Tile38WithinCircle100kmRequest",
			Unit: "average-RSS-bytes",
		},
		{
			Name: "Tile38WithinCircle100kmRequest",
			Unit: "peak-RSS-bytes",
		},
		{
			Name: "GoBuildKubelet",
			Unit: "sec/op",
		},
		{
			Name: "GoBuildKubeletLink",
			Unit: "sec/op",
		},
	}

	for i := range benchmarks {
		b := &benchmarks[i]
		// WARNING: Name and Unit are not sanitized. DO NOT pass user
		// input.
		if err := b.fetch(ctx, qc); err != nil {
			log.Printf("Error fetching benchmark %s/%s: %v", b.Name, b.Unit, err)
			http.Error(w, "Error fetching benchmark", 500)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	e := json.NewEncoder(w)
	e.SetIndent("", "\t")
	e.Encode(benchmarks)
}
