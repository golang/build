// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The relnote command works with release notes.
// It can be used to look for unfinished notes and to generate the
// final markdown file.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

var (
	verbose    = flag.Bool("v", false, "print verbose logging")
	goroot     = flag.String("goroot", runtime.GOROOT(), "root of Go repo containing docs")
	todosSince = flag.String("since", "", "earliest to look for TODOs, in YYYY-MM-DD format")
)

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "usage:\n")
	fmt.Fprintf(out, "   relnote [flags] generate\n")
	fmt.Fprintf(out, "      generate release notes from doc/next\n")
	fmt.Fprintf(out, "   relnote [flags] todo\n")
	fmt.Fprintf(out, "      report which release notes need to be written\n")
	fmt.Fprintln(out)
	flag.PrintDefaults()
}

func main() {
	log.SetPrefix("relnote: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	// Read internal/goversion to find the next release.
	data, err := os.ReadFile(filepath.Join(*goroot, "src/internal/goversion/goversion.go"))
	if err != nil {
		log.Fatal(err)
	}
	m := regexp.MustCompile(`Version = (\d+)`).FindStringSubmatch(string(data))
	if m == nil {
		log.Fatalf("cannot find Version in src/internal/goversion/goversion.go")
	}
	version := m[1]

	// Dispatch to a subcommand if one is provided.
	if cmd := flag.Arg(0); cmd != "" {
		switch cmd {
		case "generate":
			err = generate(version, *goroot)
		case "todo":
			var sinceDate time.Time
			if *todosSince != "" {
				sinceDate, err = time.Parse(time.DateOnly, *todosSince)
				if err != nil {
					log.Fatalf("-since flag: %v", err)
				}
			}
			err = todo(os.Stdout, *goroot, sinceDate)
		default:
			err = fmt.Errorf("unknown command %q", cmd)
		}
		if err != nil {
			log.Fatal(err)
		}
	} else {
		usage()
		log.Fatal("missing subcommand")
	}
}
