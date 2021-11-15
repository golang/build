// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// relui is a web interface for managing the release process of Go.
package main

import (
	"context"
	"flag"
	"log"
	"net/url"

	"github.com/jackc/pgx/v4/pgxpool"
	"golang.org/x/build/internal/https"
	"golang.org/x/build/internal/relui"
)

var (
	baseURL     = flag.String("base-url", "", "Prefix URL for routing and links.")
	downUp      = flag.Bool("migrate-down-up", false, "Run all Up migration steps, then the last down migration step, followed by the final up migration. Exits after completion.")
	migrateOnly = flag.Bool("migrate-only", false, "Exit after running migrations. Migrations are run by default.")
	pgConnect   = flag.String("pg-connect", "", "Postgres connection string or URI. If empty, libpq connection defaults are used.")
)

func main() {
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()
	ctx := context.Background()
	if err := relui.InitDB(ctx, *pgConnect); err != nil {
		log.Fatalf("relui.InitDB() = %v", err)
	}
	if *migrateOnly {
		return
	}
	if *downUp {
		if err := relui.MigrateDB(*pgConnect, true); err != nil {
			log.Fatalf("relui.MigrateDB() = %v", err)
		}
		return
	}
	db, err := pgxpool.Connect(ctx, *pgConnect)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	w := relui.NewWorker(db, relui.NewPGListener(db))
	go w.Run(ctx)
	if err := w.ResumeAll(ctx); err != nil {
		log.Printf("w.ResumeAll() = %v", err)
	}
	var base *url.URL
	if *baseURL != "" {
		base, err = url.Parse(*baseURL)
		if err != nil {
			log.Fatalf("url.Parse(%q) = %v, %v", *baseURL, base, err)
		}
	}
	s := relui.NewServer(db, w, base)
	if err != nil {
		log.Fatalf("relui.NewServer() = %v", err)
	}
	log.Fatalln(https.ListenAndServe(ctx, s))
}
