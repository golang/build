// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

// relui is a web interface for managing the release process of Go.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/jackc/pgx/v4/pgxpool"
	"golang.org/x/build/internal/relui"
)

var (
	pgConnect   = flag.String("pg-connect", "", "Postgres connection string or URI. If empty, libpq connection defaults are used.")
	onlyMigrate = flag.Bool("only-migrate", false, "Exit after running migrations. Migrations are run by default.")
)

func main() {
	flag.Parse()
	ctx := context.Background()
	if err := relui.InitDB(ctx, *pgConnect); err != nil {
		log.Fatalf("relui.InitDB() = %v", err)
	}
	if *onlyMigrate {
		return
	}
	db, err := pgxpool.Connect(ctx, *pgConnect)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	s := relui.NewServer(db)
	if err != nil {
		log.Fatalf("relui.NewServer() = %v", err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Listening on :" + port)
	log.Fatal(s.Serve(port))
}
