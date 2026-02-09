// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	_ "embed"
	"html/template"
	"log"
	"maps"
	"net/http"
	"time"

	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/pool/queue"
)

//go:embed templates/queues.html
var queuesTemplateStr string

var queuesTemplate = template.Must(baseTmpl.New("queues.html").Funcs(map[string]any{
	"timeSince":     timeSince,
	"humanDuration": humanDuration,
}).Parse(queuesTemplateStr))

type QueuesResponse struct {
	Queues map[string]*queue.QuotaStats
}

func handleQueues(w http.ResponseWriter, _ *http.Request) {
	resp := QueuesResponse{Queues: map[string]*queue.QuotaStats{}}
	mergeStats := func(qs map[string]*queue.QuotaStats) {
		maps.Copy(resp.Queues, qs)
	}
	mergeStats(pool.ReversePool().QuotaStats())
	mergeStats(pool.EC2BuildetPool().QuotaStats())
	mergeStats(pool.NewGCEConfiguration().BuildletPool().QuotaStats())
	if err := queuesTemplate.Execute(w, resp); err != nil {
		log.Printf("handleQueues: %v", err)
	}
}

func timeSince(t time.Time) time.Duration {
	return time.Since(t)
}

// humanDuration is largely time.Duration's formatting, but modified
// to imprecisely format days, even though days may vary in length
// due to daylight savings time. Sub-second durations are
// represented as 0s.
func humanDuration(d time.Duration) string {
	var buf [32]byte
	w := len(buf)

	u := uint64(d)
	neg := d < 0
	if neg {
		u = -u
	}
	w--
	buf[w] = 's'

	_, u = fmtFrac(buf[:w], u, 9)

	// u is now integer seconds
	w = fmtInt(buf[:w], u%60)
	u /= 60

	// u is now integer minutes
	if u > 0 {
		w--
		buf[w] = 'm'
		w = fmtInt(buf[:w], u%60)
		u /= 60

		// u is now integer hours
		if u > 0 {
			w--
			buf[w] = 'h'
			w = fmtInt(buf[:w], u%24)
			u /= 24
			if u > 0 {
				w--
				buf[w] = 'd'
				w = fmtInt(buf[:w], u)
			}
		}
	}

	if neg {
		w--
		buf[w] = '-'
	}
	return string(buf[w:])
}

// fmtFrac is identical to fmtFrac in the time package.
func fmtFrac(buf []byte, v uint64, prec int) (nw int, nv uint64) {
	// Omit trailing zeros up to and including decimal point.
	w := len(buf)
	print := false
	for range prec {
		digit := v % 10
		print = print || digit != 0
		if print {
			w--
			buf[w] = byte(digit) + '0'
		}
		v /= 10
	}
	if print {
		w--
		buf[w] = '.'
	}
	return w, v
}

// fmtInt is identical to fmtInt in the time package.
func fmtInt(buf []byte, v uint64) int {
	w := len(buf)
	if v == 0 {
		w--
		buf[w] = '0'
	} else {
		for v > 0 {
			w--
			buf[w] = byte(v%10) + '0'
			v /= 10
		}
	}
	return w
}
