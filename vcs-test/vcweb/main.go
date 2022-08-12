// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux
// +build linux

package main

import (
	"flag"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/build/internal/https"
)

var (
	dir     = flag.String("d", "/tmp/vcweb", "directory holding vcweb data")
	staging = flag.Bool("staging", false, "use staging letsencrypt server")
)

var buildInfo string

func usage() {
	fmt.Fprintf(os.Stderr, "usage: vcsweb [-d dir] [-staging]\n")
	os.Exit(2)
}

var isLoadDir = map[string]bool{
	"auth":   true,
	"go":     true,
	"git":    true,
	"hg":     true,
	"svn":    true,
	"fossil": true,
	"bzr":    true,
}

func main() {
	flag.Usage = usage
	https.RegisterFlags(flag.CommandLine)
	flag.Parse()
	if flag.NArg() != 0 {
		usage()
	}

	if err := os.MkdirAll(*dir, 0777); err != nil {
		log.Fatal(err)
	}

	http.Handle("/go/", http.StripPrefix("/go/", http.FileServer(http.Dir(filepath.Join(*dir, "go")))))
	http.Handle("/git/", gitHandler())
	http.Handle("/hg/", hgHandler())
	http.Handle("/svn/", svnHandler())
	http.Handle("/fossil/", fossilHandler())
	http.Handle("/bzr/", bzrHandler())
	http.Handle("/insecure/", insecureRedirectHandler())
	http.Handle("/auth/", newAuthHandler(http.Dir(filepath.Join(*dir, "auth"))))

	handler := logger(http.HandlerFunc(loadAndHandle))
	log.Fatal(https.ListenAndServe(ctx, handler))
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

func loadAndHandle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		overview(w, r)
		return
	}
	elem := strings.Split(r.URL.Path, "/")
	if len(elem) >= 3 && elem[0] == "" && isLoadDir[elem[1]] && nameRE.MatchString(elem[2]) {
		loadFS(elem[1], elem[2], r.URL.Query().Get("vcweb-force-reload") == "1" || r.URL.Query().Get("go-get") == "1")
	}
	http.DefaultServeMux.ServeHTTP(w, r)
}

func overview(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "<html>\n")
	fmt.Fprintf(w, "<title>vcs-test.golang.org</title>\n<pre>\n")
	fmt.Fprintf(w, "<b>vcs-test.golang.org</b>\n\n")
	fmt.Fprintf(w, "This server serves various version control repos for testing the go command.\n\n")

	fmt.Fprintf(w, "Date: %s\n", time.Now().Format(time.UnixDate))
	fmt.Fprintf(w, "Build: %s\n\n", html.EscapeString(buildInfo))

	fmt.Fprintf(w, "<b>cache</b>\n")

	var all []string
	cache.Lock()
	for name, entry := range cache.entry {
		all = append(all, fmt.Sprintf("%s\t%x\t%s\n", name, entry.md5, entry.expire.Format(time.UnixDate)))
	}
	cache.Unlock()
	sort.Strings(all)
	tw := tabwriter.NewWriter(w, 1, 8, 1, '\t', 0)
	for _, line := range all {
		tw.Write([]byte(line))
	}
	tw.Flush()
}

type loggingResponseWriter struct {
	code int
	size int64
	http.ResponseWriter
}

func (l *loggingResponseWriter) WriteHeader(code int) {
	l.code = code
	l.ResponseWriter.WriteHeader(code)
}

func (l *loggingResponseWriter) Write(data []byte) (int, error) {
	n, err := l.ResponseWriter.Write(data)
	l.size += int64(n)
	return n, err
}

func dashOr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func logger(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := &loggingResponseWriter{
			code:           200,
			ResponseWriter: w,
		}
		startTime := time.Now().Format("02/Jan/2006:15:04:05 -0700")
		defer func() {
			err := recover()
			if err != nil {
				l.code = 999
			}
			fmt.Fprintf(os.Stderr, "%s - - [%s] %q %03d %d %q %q %q\n",
				dashOr(r.RemoteAddr),
				startTime,
				r.Method+" "+r.URL.String()+" "+r.Proto,
				l.code,
				l.size,
				r.Header.Get("Referer"),
				r.Header.Get("User-Agent"),
				r.Host)
			if err != nil {
				panic(err)
			}
		}()
		h.ServeHTTP(l, r)
	}
}
