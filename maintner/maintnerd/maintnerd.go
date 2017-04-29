// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The maintnerd command serves project maintainer data from Git,
// Github, and/or Gerrit.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/storage"
	"golang.org/x/build/autocertcache"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/crypto/acme/autocert"
)

var (
	listen         = flag.String("listen", "localhost:6343", "listen address")
	autocertDomain = flag.String("autocert", "", "if non-empty, listen on port 443 and serve a LetsEncrypt TLS cert on this domain")
	autocertBucket = flag.String("autocert-bucket", "", "if non-empty, Google Cloud Storage bucket to store LetsEncrypt cache in")
	syncQuit       = flag.Bool("sync-and-quit", false, "sync once and quit; don't run a server")
	initQuit       = flag.Bool("init-and-quit", false, "load the mutation log and quit; don't run a server")
	verbose        = flag.Bool("verbose", false, "enable verbose debug output")
	watchGithub    = flag.String("watch-github", "", "Comma-separated list of owner/repo pairs to slurp")
	// TODO: specify gerrit auth via gitcookies or similar
	watchGerrit = flag.String("watch-gerrit", "", `Comma-separated list of Gerrit projects to watch, each of form "hostname/project" (e.g. "go.googlesource.com/go")`)
	pubsub      = flag.String("pubsub", "", "If non-empty, the golang.org/x/build/cmd/pubsubhelper URL scheme and hostname, without path")
	config      = flag.String("config", "", "If non-empty, the name of a pre-defined config. Currently only 'go' is recognized.")
	dataDir     = flag.String("data-dir", "", "Local directory to write protobuf files to (default $HOME/var/maintnerd)")
	debug       = flag.Bool("debug", false, "Print debug logging information")

	bucket         = flag.String("bucket", "", "if non-empty, Google Cloud Storage bucket to use for log storage")
	migrateGCSFlag = flag.Bool("migrate-disk-to-gcs", false, "[dev] If true, migrate from disk-based logs to GCS logs on start-up, then quit.")
)

func init() {
	flag.Usage = func() {
		os.Stderr.WriteString(`Maintner mirrors, searches, syncs, and serves data from Gerrit, Github, and Git repos.

Maintner gathers data about projects that you want to watch and holds it all in
memory. This way it's easy and fast to search, and you don't have to worry about
retrieving that data from remote APIs.

Maintner is short for "maintainer."

`)
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()
	if *dataDir == "" {
		*dataDir = filepath.Join(os.Getenv("HOME"), "var", "maintnerd")
		if *bucket == "" {
			if err := os.MkdirAll(*dataDir, 0755); err != nil {
				log.Fatal(err)
			}
			log.Printf("Storing data in implicit directory %s", *dataDir)
		}
	}
	if *migrateGCSFlag && *bucket == "" {
		log.Fatalf("--bucket flag required with --migrate-disk-to-gcs")
	}

	type storage interface {
		maintner.MutationSource
		maintner.MutationLogger
	}
	var logger storage
	if *bucket != "" {
		ctx := context.Background()
		gl, err := newGCSLog(ctx, *bucket)
		if err != nil {
			log.Fatalf("newGCSLog: %v", err)
		}
		http.HandleFunc("/logs", gl.serveJSONLogsIndex)
		http.HandleFunc("/logs/", gl.serveLogFile)
		if *migrateGCSFlag {
			diskLog := maintner.NewDiskMutationLogger(*dataDir)
			if err := gl.copyFrom(diskLog); err != nil {
				log.Fatalf("migrate: %v", err)
			}
			log.Printf("Success.")
			return
		}
		logger = gl
	} else {
		logger = maintner.NewDiskMutationLogger(*dataDir)
	}

	corpus := new(maintner.Corpus)
	corpus.EnableLeaderMode(logger, *dataDir)
	if *debug {
		corpus.SetDebug()
	}
	corpus.SetVerbose(*verbose)
	switch *config {
	case "":
		// Nothing
	case "go":
		setGoConfig()
	default:
		log.Fatalf("unknown --config=%s", *config)
	}
	if *watchGithub != "" {
		for _, pair := range strings.Split(*watchGithub, ",") {
			splits := strings.SplitN(pair, "/", 2)
			if len(splits) != 2 || splits[1] == "" {
				log.Fatalf("Invalid github repo: %s. Should be 'owner/repo,owner2/repo2'", pair)
			}
			token, err := getGithubToken()
			if err != nil {
				log.Fatalf("getting github token: %v", err)
			}
			corpus.TrackGithub(splits[0], splits[1], token)
		}
	}
	if *watchGerrit != "" {
		for _, project := range strings.Split(*watchGerrit, ",") {
			// token may be empty, that's OK.
			corpus.TrackGerrit(project)
		}
	}

	var ln net.Listener
	var err error
	if !*syncQuit && !*initQuit {
		ln, err = net.Listen("tcp", *listen)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Listening on %v", ln.Addr())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Now()
	if err := corpus.Initialize(ctx, logger); err != nil {
		// TODO: if Initialize only partially syncs the data, we need to delete
		// whatever files it created, since Github returns events newest first
		// and we use the issue updated dates to check whether we need to keep
		// syncing.
		log.Fatal(err)
	}
	initDur := time.Since(t0)

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	log.Printf("Loaded data in %v. Memory: %v MB (%v bytes)", initDur, ms.HeapAlloc>>20, ms.HeapAlloc)
	if *initQuit {
		return
	}

	if *syncQuit {
		if err := corpus.Sync(ctx); err != nil {
			log.Fatalf("corpus.Sync = %v", err)
		}
		return
	}

	if *pubsub != "" {
		corpus.StartPubSubHelperSubscribe(*pubsub)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, "<html><body>This is <a href='https://godoc.org/golang.org/x/build/maintner/maintnerd'>maintnerd</a>, the <a href='https://godoc.org/golang.org/x/build/maintner'>maintner</a> server.</body>")
	})

	errc := make(chan error)
	go func() { errc <- fmt.Errorf("Corpus.SyncLoop = %v", corpus.SyncLoop(ctx)) }()
	if ln != nil {
		go func() { errc <- fmt.Errorf("http.Serve = %v", http.Serve(ln, nil)) }()
	}
	if *autocertDomain != "" {
		go func() { errc <- serveTLS() }()
	}

	log.Fatal(<-errc)
}

func setGoConfig() {
	if *watchGithub != "" {
		log.Fatalf("can't set both --config and --watch-github")
	}
	if *watchGerrit != "" {
		log.Fatalf("can't set both --config and --watch-gerrit")
	}
	*pubsub = "https://pubsubhelper.golang.org"
	*watchGithub = "golang/go"

	gerrc := gerrit.NewClient("https://go-review.googlesource.com/", gerrit.NoAuth)
	projs, err := gerrc.ListProjects(context.Background())
	if err != nil {
		log.Fatalf("error listing Go's gerrit projects: %v", err)
	}
	var buf bytes.Buffer
	buf.WriteString("code.googlesource.com/gocloud,code.googlesource.com/google-api-go-client")
	for _, pi := range projs {
		buf.WriteString(",go.googlesource.com/")
		buf.WriteString(pi.ID)
	}
	*watchGerrit = buf.String()
}

func getGithubToken() (string, error) {
	if metadata.OnGCE() {
		token, err := metadata.ProjectAttributeValue("maintner-github-token")
		if err == nil {
			return token, nil
		}
		log.Printf("getting GCE metadata 'maintner-github-token': %v", err)
		log.Printf("falling back to github token from file.")
	}

	tokenFile := filepath.Join(os.Getenv("HOME"), ".github-issue-token")
	slurp, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		return "", err
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", fmt.Errorf("Expected token file %s to be of form <username>:<token>", tokenFile)
	}
	token := f[1]
	return token, nil
}

func serveTLS() error {
	if *autocertBucket == "" {
		return fmt.Errorf("using --autocert requires --autocert-bucket.")
	}
	ln, err := net.Listen("tcp", ":443")
	if err != nil {
		return err
	}
	sc, err := storage.NewClient(context.Background())
	if err != nil {
		return fmt.Errorf("storage.NewClient: %v", err)
	}
	m := autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(*autocertDomain),
		Cache:      autocertcache.NewGoogleCloudStorageCache(sc, *autocertBucket),
	}
	config := &tls.Config{
		GetCertificate: m.GetCertificate,
	}
	tlsLn := tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, config)
	server := &http.Server{
		Addr: ln.Addr().String(),
	}
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
