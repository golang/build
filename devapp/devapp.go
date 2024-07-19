// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Devapp is the server running dev.golang.org. It shows open bugs and code
// reviews and other useful dashboards for Go developers.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"golang.org/x/build/internal/https"
)

var (
	staticDir   = flag.String("static-dir", "./static/", "location of static directory relative to binary location")
	templateDir = flag.String("template-dir", "./templates/", "location of templates directory relative to binary location")
	reload      = flag.Bool("reload", false, "reload content on each page load")
)

func init() {
	flag.Usage = func() {
		os.Stderr.WriteString("devapp generates the dashboard that powers dev.golang.org.\n")
		flag.PrintDefaults()
	}
}

func main() {
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()

	s := newServer(http.NewServeMux(), *staticDir, *templateDir, *reload)
	ctx := context.Background()
	if err := s.initCorpus(ctx); err != nil {
		log.Fatalf("Could not init corpus: %v", err)
	}
	go s.corpusUpdateLoop(ctx)

	log.Fatalln(https.ListenAndServe(ctx, s))
}
