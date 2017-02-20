// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package devapp implements a simple App Engine app for generating
// and serving Go project release dashboards using the godash
// command/library.
package devapp

import (
	"bytes"
	"fmt"
	"io"
	stdlog "log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/godash"
	"golang.org/x/net/context"
)

const entityPrefix = "DevApp"

var gerritTransport http.RoundTripper

func init() {
	for _, page := range []string{"release", "cl"} {
		page := page
		http.Handle("/"+page, hstsHandler(func(w http.ResponseWriter, r *http.Request) { servePage(w, r, page) }))
	}
	http.Handle("/dash", hstsHandler(showDash))
	http.Handle("/update", ctxHandler(update))
	// Defined in stats.go
	http.HandleFunc("/stats/raw", rawHandler)
	http.HandleFunc("/stats/svg", svgHandler)
	http.Handle("/stats/release", ctxHandler(release))
	http.Handle("/stats/release/data.js", ctxHandler(releaseData))
	http.Handle("/update/stats", ctxHandler(updateStats))
}

// hstsHandler wraps an http.HandlerFunc such that it sets the HSTS header.
func hstsHandler(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; preload")
		fn(w, r)
	})
}

func ctxHandler(fn func(ctx context.Context, w http.ResponseWriter, r *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := getContext(r)
		if err := fn(ctx, w, r); err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
}

func logFn(ctx context.Context, w io.Writer) func(string, ...interface{}) {
	logger := stdlog.New(w, "", stdlog.Lmicroseconds)
	return func(format string, args ...interface{}) {
		logger.Printf(format, args...)
		log.Infof(ctx, format, args...)
	}
}

type Page struct {
	// Content is the complete HTML of the page.
	Content []byte
}

func servePage(w http.ResponseWriter, r *http.Request, page string) {
	ctx := r.Context()
	entity, err := getPage(ctx, page)
	if err != nil {
		http.Error(w, "page not found", 404)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(entity.Content)
}

func update(ctx context.Context, w http.ResponseWriter, _ *http.Request) error {
	token, err := getToken(ctx)
	if err != nil {
		return err
	}
	gzdata, _ := getCache(ctx, "gzdata")
	gh := godash.NewGitHubClient("golang/go", token, newTransport(ctx))
	ger := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	// Without a deadline, urlfetch will use a 5s timeout which is too slow for Gerrit.
	gerctx, cancel := context.WithTimeout(ctx, 9*time.Minute)
	defer cancel()
	ger.HTTPClient = newHTTPClient(gerctx)

	data, err := parseData(gzdata)
	if err != nil {
		return err
	}

	if err := data.Reviewers.LoadGithub(ctx, gh); err != nil {
		return err
	}
	l := logFn(ctx, w)
	if err := data.FetchData(gerctx, gh, ger, l, 7, false, false); err != nil {
		log.Criticalf(ctx, "failed to fetch data: %v", err)
		return err
	}

	for _, cls := range []bool{false, true} {
		var output bytes.Buffer
		kind := "release"
		if cls {
			kind = "CL"
		}
		fmt.Fprintf(&output, "Go %s dashboard\n", kind)
		fmt.Fprintf(&output, "%v\n\n", time.Now().UTC().Format(time.UnixDate))
		fmt.Fprintf(&output, "HOWTO\n\n")
		if cls {
			data.PrintCLs(&output)
		} else {
			fmt.Fprintf(&output, fmt.Sprintf(`<a href="/stats/release?cycle=%d">Go 1.%d Issue Stats Dashboard</a>`, data.GoReleaseCycle, data.GoReleaseCycle))
			data.PrintIssues(&output)
		}
		var html bytes.Buffer
		godash.PrintHTML(&html, output.String())

		if err := writePage(ctx, strings.ToLower(kind), html.Bytes()); err != nil {
			return err
		}
	}
	return writeCache(ctx, "gzdata", &data)
}
