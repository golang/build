// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"cloud.google.com/go/datastore"
	"github.com/NYTimes/gziphandler"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/repos"
	"golang.org/x/net/http2"
	"grpc.go4.org" // simpler, uses x/net/http2.Transport; we use this elsewhere in x/build
)

var (
	maintnerClient  = createMaintnerClient()
	datastoreClient *datastore.Client // not done at init as createDatastoreClient fails under test environments
)

var (
	dev         = flag.Bool("dev", false, "whether to run in local development mode")
	fakeResults = flag.Bool("fake-results", false, "dev mode option: whether to make up fake random results. If true, datastore is not used.")
)

func main() {
	flag.Parse()
	if *fakeResults && !*dev {
		log.Fatalf("--fake-results requires --dev mode")
	}
	if *dev {
		randBytes := make([]byte, 20)
		if _, err := crand.Read(randBytes[:]); err != nil {
			panic(err)
		}
		devModeMasterKey = fmt.Sprintf("%x", randBytes)
		if !*fakeResults {
			log.Printf("Running in dev mode. Temporary master key is %v", devModeMasterKey)
			if os.Getenv("DATASTORE_PROJECT_ID") == "" {
				log.Printf("DATASTORE_PROJECT_ID not set; defaulting to production golang-org")
				os.Setenv("DATASTORE_PROJECT_ID", "golang-org")
			}
		}
	}

	datastoreClient = createDatastoreClient()

	if *dev && !*fakeResults {
		// Test early whether user has datastore access.
		key := dsKey("Log", "bogus-want-no-such-entity", nil)
		if err := datastoreClient.Get(context.Background(), key, new(Log)); err != datastore.ErrNoSuchEntity {
			log.Printf("Failed to access datastore: %v", err)
			log.Printf("Run with --fake-results to avoid hitting a real datastore.")
			os.Exit(1)
		}
	}

	// authenticated handlers
	handleFunc("/clear-results", AuthHandler(clearResultsHandler)) // called by x/build/cmd/retrybuilds
	handleFunc("/result", AuthHandler(resultHandler))              // called by coordinator after build

	// public handlers
	handleFunc("/", uiHandler)
	handleFunc("/log/", logHandler)

	// We used to use App Engine's static file handling support, declared in app.yaml,
	// but it's currently broken with dev_appserver.py with the go111 runtime we use.
	// So just do it ourselves. It doesn't buy us enough to be worth it.
	fs := http.StripPrefix("/static", http.FileServer(http.Dir(staticDir())))
	handleFunc("/static/", fs.ServeHTTP)
	handleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://golang.org/favicon.ico", http.StatusFound)
	})

	listen := os.Getenv("PORT")
	if listen == "" {
		listen = "8080"
	}
	if !strings.Contains(listen, ":") {
		listen = ":" + listen
	}

	log.Printf("Serving dashboard on %s", listen)
	if err := http.ListenAndServe(listen, nil); err != nil {
		log.Fatal(err)
	}
}

func staticDir() string {
	if pwd, _ := os.Getwd(); strings.HasSuffix(pwd, "app/appengine") {
		return "static"
	}
	return "app/appengine/static"
}

func createDatastoreClient() *datastore.Client {
	if *fakeResults {
		return nil
	}
	// First try with an empty project ID, so $DATASTORE_PROJECT_ID will be respected
	// if set.
	c, err := datastore.NewClient(context.Background(), "")
	if err == nil {
		return c
	}
	// Otherwise auto-detect it from the environment (that is,
	// work automatically in prod).
	c, err = datastore.NewClient(context.Background(), datastore.DetectProjectID)
	if err != nil {
		log.Fatalf("datastore.NewClient: %v", err)
	}
	return c
}

func createMaintnerClient() apipb.MaintnerServiceClient {
	addr := os.Getenv("MAINTNER_ADDR") // host[:port]
	if addr == "" {
		addr = "maintner.golang.org"
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			NextProtos:         []string{"h2"},
			InsecureSkipVerify: strings.HasPrefix(addr, "localhost:"),
		},
	}
	hc := &http.Client{Transport: tr}
	http2.ConfigureTransport(tr)

	cc, err := grpc.NewClient(hc, "https://"+addr)
	if err != nil {
		log.Fatal(err)
	}
	return apipb.NewMaintnerServiceClient(cc)
}

func handleFunc(path string, h http.HandlerFunc) {
	http.Handle(path, hstsHandler(gziphandler.GzipHandler(h)))
}

// hstsHandler returns a Handler that sets the HSTS header but
// otherwise just wraps h.
func hstsHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; preload")
		h.ServeHTTP(w, r)
	})
}

// Dashboard describes a unique build dashboard.
//
// (There used to be more than one dashboard, so this is now somewhat
// less important than it once was.)
type Dashboard struct {
	Name     string     // This dashboard's name (always "Go" nowadays)
	Packages []*Package // The project's packages to build
}

// packageWithPath returns the Package in d with the provided importPath,
// or nil if none is found.
func (d *Dashboard) packageWithPath(importPath string) *Package {
	for _, p := range d.Packages {
		if p.Path == importPath {
			return p
		}
	}
	return nil
}

// goDash is the dashboard for the main go repository.
var goDash = &Dashboard{
	Name: "Go",
	Packages: []*Package{
		{Name: "Go"},
	},
}

func init() {
	var add []*Package
	for _, r := range repos.ByGerritProject {
		if !r.ShowOnDashboard() {
			continue
		}
		add = append(add, &Package{
			Name: r.GoGerritProject,
			Path: r.ImportPath,
		})
	}
	sort.Slice(add, func(i, j int) bool {
		return add[i].Name < add[j].Name
	})
	goDash.Packages = append(goDash.Packages, add...)
}
