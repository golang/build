// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build cgo

// Localperfdata runs an HTTP server for benchmark perfdata.
//
// Usage:
//
//	localperfdata [-addr address] [-view_url_base url] [-base_dir ../appengine] [-dsn file.db]
package main

import (
	"flag"
	"log"
	"net/http"

	"golang.org/x/build/internal/basedir"
	"golang.org/x/build/perfdata/app"
	"golang.org/x/build/perfdata/db"
	_ "golang.org/x/build/perfdata/db/sqlite3"
	"golang.org/x/build/perfdata/fs"
	"golang.org/x/build/perfdata/fs/local"
)

var (
	addr        = flag.String("addr", ":8080", "serve HTTP on `address`")
	viewURLBase = flag.String("view_url_base", "", "/upload response with `URL` for viewing")
	dsn         = flag.String("dsn", ":memory:", "sqlite `dsn`")
	data        = flag.String("data", "", "data `directory` (in-memory if empty)")
	baseDir     = flag.String("base_dir", basedir.Find("golang.org/x/build/perfdata/appengine"), "base `directory` for static files")
)

func main() {
	flag.Parse()

	if *baseDir == "" {
		log.Print("base_dir is required and could not be automatically found")
		flag.Usage()
	}

	db, err := db.OpenSQL("sqlite3", *dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	var fs fs.FS = fs.NewMemFS()

	if *data != "" {
		fs = local.NewFS(*data)
	}

	app := &app.App{
		DB:          db,
		FS:          fs,
		ViewURLBase: *viewURLBase,
		Auth:        func(http.ResponseWriter, *http.Request) (string, error) { return "", nil },
		BaseDir:     *baseDir,
	}
	app.RegisterOnMux(http.DefaultServeMux)

	log.Printf("Listening on %s", *addr)

	log.Fatal(http.ListenAndServe(*addr, nil))
}
