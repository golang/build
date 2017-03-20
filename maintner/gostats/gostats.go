// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gostats command computes stats about the Go project.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"runtime"
	"time"

	"golang.org/x/build/maintner/godata"
)

var (
	last      = flag.Duration("last", 0, "restrict stats to this duration")
	loadStats = flag.Bool("time-load", false, "time the load of the corpus")

	topFiles = flag.Int("modified-files", 0, "if non-zero, show the top modified files")
)

func main() {
	flag.Parse()

	t0 := time.Now()
	corpus, err := godata.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if *loadStats {
		dur := time.Since(t0)
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		log.Printf("Loaded data in %v. Memory: %v MB", dur, ms.HeapAlloc>>20)
	}

	if *topFiles > 0 {
		top := corpus.QueryFrequentlyModifiedFiles(*topFiles)
		for _, fc := range top {
			fmt.Printf(" %5d %s\n", fc.Count, fc.File)
		}
		return
	}
}
