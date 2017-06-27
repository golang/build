// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Devapp is the server running dev.golang.org. It shows open bugs and code
// reviews and other useful dashboards for Go developers.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/build/autocertcache"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/godash"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/http2"
)

func init() {
	flag.Usage = func() {
		os.Stderr.WriteString("devapp generates the dashboard that powers dev.golang.org.\n")
		flag.PrintDefaults()
	}

	// TODO don't bind relative to a working directory.
	http.Handle("/", http.FileServer(http.Dir("./static/")))
	http.HandleFunc("/favicon.ico", faviconHandler)
	http.Handle("/release", hstsHandler(func(w http.ResponseWriter, r *http.Request) { servePage(w, r, "release") }))
}

func main() {
	var (
		listen         = flag.String("listen", "localhost:6343", "listen address")
		devTLSPort     = flag.Int("dev-tls-port", 0, "if non-zero, port number to run localhost self-signed TLS server")
		autocertBucket = flag.String("autocert-bucket", "", "if non-empty, listen on port 443 and serve a LetsEncrypt TLS cert using this Google Cloud Storage bucket as a cache")
		updateInterval = flag.Duration("update-interval", 5*time.Minute, "how often to update the dashboard data")
	)
	flag.Parse()

	go updateLoop(*updateInterval)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("Error listening on %s: %v\n", *listen, err)
	}
	log.Printf("Listening on %s\n", ln.Addr())

	errc := make(chan error)
	if ln != nil {
		go func() { errc <- fmt.Errorf("http.Serve = %v", http.Serve(ln, nil)) }()
	}
	if *autocertBucket != "" {
		go func() { errc <- serveAutocertTLS(*autocertBucket) }()
	}
	if *devTLSPort != 0 {
		go func() { errc <- serveDevTLS(*devTLSPort) }()
	}

	log.Fatal(<-errc)
}

func updateLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for {
		if err := update(); err != nil {
			log.Printf("update: %v", err)
		}
		<-ticker.C
	}
}

func serveDevTLS(port int) error {
	ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("Serving self-signed TLS at https://%s", ln.Addr())
	// Abuse httptest for its localhost TLS setup code:
	ts := httptest.NewUnstartedServer(http.DefaultServeMux)
	// Ditch the provided listener, replace with our own:
	ts.Listener.Close()
	ts.Listener = ln
	ts.TLS = &tls.Config{
		NextProtos:         []string{"h2", "http/1.1"},
		InsecureSkipVerify: true,
	}
	ts.StartTLS()

	select {}
}

func serveAutocertTLS(bucket string) error {
	ln, err := net.Listen("tcp", ":443")
	if err != nil {
		return err
	}
	defer ln.Close()
	sc, err := storage.NewClient(context.Background())
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}
	m := autocert.Manager{
		Prompt: autocert.AcceptTOS,
		HostPolicy: func(ctx context.Context, host string) error {
			if !strings.HasSuffix(host, ".golang.org") {
				return errors.New("refusing to serve autocert on provided domain")
			}
			return nil
		},
		Cache: autocertcache.NewGoogleCloudStorageCache(sc, bucket),
	}
	config := &tls.Config{
		GetCertificate: m.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
	tlsLn := tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, config)
	server := &http.Server{
		Addr: ln.Addr().String(),
	}
	if err := http2.ConfigureServer(server, nil); err != nil {
		log.Fatalf("http2.ConfigureServer: %v", err)
	}
	log.Printf("Serving TLS at %s", tlsLn.Addr())
	return server.Serve(tlsLn)
}

type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}

// hstsHandler wraps an http.HandlerFunc such that it sets the HSTS header.
func hstsHandler(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; preload")
		fn(w, r)
	})
}

type page struct {
	// Content is the complete HTML of the page.
	Content []byte
}

var (
	pageStore   = map[string]*page{}
	pageStoreMu sync.Mutex
)

func getPage(name string) (*page, error) {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	p, ok := pageStore[name]
	if ok {
		return p, nil
	}
	return nil, fmt.Errorf("page key %s not found", name)
}

func writePage(pageStr string, content []byte) error {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	entity := &page{
		Content: content,
	}
	pageStore[pageStr] = entity
	return nil
}

func servePage(w http.ResponseWriter, r *http.Request, pageStr string) {
	entity, err := getPage(pageStr)
	if err != nil {
		log.Printf("getPage(%s) = %v", pageStr, err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(entity.Content)
}

type countTransport struct {
	http.RoundTripper
	count int64
}

func (ct *countTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt64(&ct.count, 1)
	return ct.RoundTripper.RoundTrip(req)
}

func (ct *countTransport) Count() int64 {
	return atomic.LoadInt64(&ct.count)
}

func update() error {
	log.Printf("Updating dashboard data...")
	token, err := getToken()
	if err != nil {
		return err
	}
	gzdata, _ := getCache("gzdata")

	ctx := context.Background()
	ct := &countTransport{newTransport(ctx), 0}
	gh := godash.NewGitHubClient("golang/go", token, ct)
	defer func() {
		log.Printf("Sent %d requests to GitHub", ct.Count())
	}()

	data, err := parseData(gzdata)
	if err != nil {
		return err
	}

	if err := data.Reviewers.LoadGithub(ctx, gh); err != nil {
		return fmt.Errorf("failed to load reviewers: %v", err)
	}
	ger := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)

	if err := data.FetchData(ctx, gh, ger, log.Printf, 7, false, false); err != nil {
		return fmt.Errorf("failed to fetch data: %v", err)
	}

	var output bytes.Buffer
	const kind = "release"
	fmt.Fprintf(&output, "Go %s dashboard\n", kind)
	fmt.Fprintf(&output, "%v\n\n", time.Now().UTC().Format(time.UnixDate))
	fmt.Fprintf(&output, "HOWTO\n\n")
	data.PrintIssues(&output)
	var html bytes.Buffer
	godash.PrintHTML(&html, output.String())

	if err := writePage(kind, html.Bytes()); err != nil {
		return err
	}
	return writeCache("gzdata", &data)
}

func newTransport(ctx context.Context) http.RoundTripper {
	dline, ok := ctx.Deadline()
	t := &http.Transport{}
	if ok {
		t.ResponseHeaderTimeout = time.Until(dline)
	}
	return t
}

// GET /favicon.ico
func faviconHandler(w http.ResponseWriter, r *http.Request) {
	// Need to specify content type for consistent tests, without this it's
	// determined from mime.types on the box the test is running on
	w.Header().Set("Content-Type", "image/x-icon")
	http.ServeFile(w, r, "./static/favicon.ico")
}
