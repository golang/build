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

	"golang.org/x/build/internal/relui"
)

var (
	pgConnect = flag.String("pg-connect", "host=/var/run/postgresql user=postgres database=relui-dev", "Postgres connection string or URI")
)

func main() {
	ctx := context.Background()
	s, err := relui.NewServer(ctx, *pgConnect)
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
