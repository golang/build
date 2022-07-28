// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16 && (linux || darwin)
// +build go1.16
// +build linux darwin

package main

import (
	_ "embed"
	"html/template"
	"log"
	"net/http"

	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/pool/queue"
)

//go:embed templates/queues.html
var queuesTemplateStr string

var queuesTemplate = template.Must(baseTmpl.New("queues.html").Parse(queuesTemplateStr))

type QueuesResponse struct {
	Queues map[string]*queue.QuotaStats
}

func handleQueues(w http.ResponseWriter, _ *http.Request) {
	resp := QueuesResponse{Queues: map[string]*queue.QuotaStats{}}
	mergeStats := func(qs map[string]*queue.QuotaStats) {
		for name, stats := range qs {
			resp.Queues[name] = stats
		}
	}
	mergeStats(pool.ReversePool().QuotaStats())
	mergeStats(pool.EC2BuildetPool().QuotaStats())
	mergeStats(pool.NewGCEConfiguration().BuildletPool().QuotaStats())
	if err := queuesTemplate.Execute(w, resp); err != nil {
		log.Printf("handleQueues: %v", err)
	}
}
