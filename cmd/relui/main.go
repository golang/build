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
	"net/http"
	"os"
)

var (
	pgConnect = flag.String("pg-connect", "host=/var/run/postgresql user=postgres database=relui-dev", "Postgres connection string or URI")
)

func main() {
	ctx := context.Background()
	d := new(pgStore)
	if err := d.Connect(ctx, *pgConnect); err != nil {
		log.Fatalf("d.Connect() = %v", err)
	}
	defer d.Close()
	s := &server{
		store: d,
	}
	http.Handle("/workflows/create", http.HandlerFunc(s.createWorkflowHandler))
	http.Handle("/workflows/new", http.HandlerFunc(s.newWorkflowHandler))
	http.Handle("/tasks/start", http.HandlerFunc(s.startTaskHandler))
	http.Handle("/", fileServerHandler(static, http.HandlerFunc(s.homeHandler)))
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Listening on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, http.DefaultServeMux))
}
