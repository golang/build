// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux darwin

// The coordinator runs the majority of the Go build system.
//
// It is responsible for finding build work and executing it,
// reporting the results to build.golang.org for public display.
//
// For an overview of the Go build system, see the README at
// the root of the x/build repo.
package main // import "golang.org/x/build/cmd/coordinator"

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go4.org/syncutil"
	grpc "grpc.go4.org"

	"cloud.google.com/go/errorreporting"
	"cloud.google.com/go/storage"
	"golang.org/x/build"
	"golang.org/x/build/autocertcache"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/cmd/coordinator/spanlog"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/buildstats"
	"golang.org/x/build/internal/singleflight"
	"golang.org/x/build/internal/sourcecache"
	"golang.org/x/build/livelog"
	"golang.org/x/build/maintner/maintnerd/apipb"
	revdialv2 "golang.org/x/build/revdial/v2"
	"golang.org/x/build/types"
	"golang.org/x/crypto/acme/autocert"
	perfstorage "golang.org/x/perf/storage"
	"golang.org/x/time/rate"
)

const (
	// eventDone is a build event name meaning the build was
	// completed (either successfully or with remote errors).
	// Notably, it is NOT included for network/communication
	// errors.
	eventDone = "done"

	// eventSkipBuildMissingDep is a build event name meaning
	// the builder type is not applicable to the commit being
	// tested because the commit lacks a necessary dependency
	// in its git history.
	eventSkipBuildMissingDep = "skipped_build_missing_dep"
)

var (
	processStartTime = time.Now()
	processID        = "P" + randHex(9)
)

var sched = NewScheduler()

var Version string // set by linker -X

// devPause is a debug option to pause for 5 minutes after the build
// finishes before destroying buildlets.
const devPause = false

// stagingTryWork is a debug option to enable or disable running
// trybot work in staging.
//
// If enabled, only open CLs containing "DO NOT SUBMIT" and "STAGING"
// in their commit message (in addition to being marked Run-TryBot+1)
// will be run.
const stagingTryWork = true

var (
	masterKeyFile  = flag.String("masterkey", "", "Path to builder master key. Else fetched using GCE project attribute 'builder-master-key'.")
	mode           = flag.String("mode", "", "Valid modes are 'dev', 'prod', or '' for auto-detect. dev means localhost development, not be confused with staging on go-dashboard-dev, which is still the 'prod' mode.")
	buildEnvName   = flag.String("env", "", "The build environment configuration to use. Not required if running on GCE.")
	devEnableGCE   = flag.Bool("dev_gce", false, "Whether or not to enable the GCE pool when in dev mode. The pool is enabled by default in prod mode.")
	shouldRunBench = flag.Bool("run_bench", false, "Whether or not to run benchmarks on trybot commits. Override by GCE project attribute 'farmer-run-bench'.")
	perfServer     = flag.String("perf_server", "", "Upload benchmark results to `server`. Overrides buildenv default for testing.")
)

// LOCK ORDER:
//   statusMu, buildStatus.mu, trySet.mu
// (Other locks, such as the remoteBuildlet mutex should
// not be used along with other locks)

var (
	statusMu   sync.Mutex // guards the following four structures; see LOCK ORDER comment above
	status     = map[buildgo.BuilderRev]*buildStatus{}
	statusDone []*buildStatus         // finished recently, capped to maxStatusDone
	tries      = map[tryKey]*trySet{} // trybot builds
	tryList    []tryKey
)

var maintnerClient apipb.MaintnerServiceClient

const (
	maxStatusDone = 30

	// vmDeleteTimeout and podDeleteTimeout is how long before we delete a VM.
	// In practice this need only be as long as the slowest
	// builder (plan9 currently), because on startup this program
	// already deletes all buildlets it doesn't know about
	// (i.e. ones from a previous instance of the coordinator).
	vmDeleteTimeout  = 45 * time.Minute
	podDeleteTimeout = 45 * time.Minute
)

// Fake keys signed by a fake CA.
// These are used in localhost dev mode. (Not to be confused with the
// staging "dev" instance under GCE project "go-dashboard-dev")
var testFiles = map[string]string{
	"farmer-cert.pem": build.DevCoordinatorCA,
	"farmer-key.pem":  build.DevCoordinatorKey,
}

func listenAndServeTLS() {
	addr := ":443"
	if *mode == "dev" {
		addr = "localhost:8119"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("net.Listen(%s): %v", addr, err)
	}
	serveTLS(ln)
}

func serveTLS(ln net.Listener) {
	config := &tls.Config{
		NextProtos: []string{"http/1.1"},
	}

	if autocertManager != nil {
		config.GetCertificate = autocertManager.GetCertificate
	} else {
		certPEM, err := readGCSFile("farmer-cert.pem")
		if err != nil {
			log.Printf("cannot load TLS cert, skipping https: %v", err)
			return
		}
		keyPEM, err := readGCSFile("farmer-key.pem")
		if err != nil {
			log.Printf("cannot load TLS key, skipping https: %v", err)
			return
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			log.Printf("bad TLS cert: %v", err)
			return
		}
		config.Certificates = []tls.Certificate{cert}
	}

	server := &http.Server{
		Addr:    ln.Addr().String(),
		Handler: httpRouter{},
	}
	tlsLn := tls.NewListener(tcpKeepAliveListener{ln.(*net.TCPListener)}, config)
	log.Printf("Coordinator serving on: %v", tlsLn.Addr())
	if err := server.Serve(tlsLn); err != nil {
		log.Fatalf("serve https: %v", err)
	}
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

// httpRouter is the coordinator's mux, routing traffic to one of
// three locations:
//   1) a buildlet, from gomote clients (if X-Buildlet-Proxy is set)
//   2) our module proxy cache on GKE (if X-Proxy-Service == module-cache)
//   3) traffic to the coordinator itself (the default)
type httpRouter struct{}

func (httpRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Buildlet-Proxy") != "" {
		requireBuildletProxyAuth(http.HandlerFunc(proxyBuildletHTTP)).ServeHTTP(w, r)
		return
	}
	http.DefaultServeMux.ServeHTTP(w, r)
}

type loggerFunc func(event string, optText ...string)

func (fn loggerFunc) LogEventTime(event string, optText ...string) {
	fn(event, optText...)
}

func (fn loggerFunc) CreateSpan(event string, optText ...string) spanlog.Span {
	return createSpan(fn, event, optText...)
}

// autocertManager is non-nil if LetsEncrypt is in use.
var autocertManager *autocert.Manager

func main() {
	flag.Parse()

	if Version == "" && *mode == "dev" {
		Version = "dev"
	}
	log.Printf("coordinator version %q starting", Version)
	err := initGCE()
	if err != nil {
		if *mode == "" {
			*mode = "dev"
		}
		log.Printf("VM support disabled due to error initializing GCE: %v", err)
	} else {
		if *mode == "" {
			*mode = "prod"
		}
	}

	if bucket := buildEnv.AutoCertCacheBucket; bucket != "" {
		if storageClient == nil {
			log.Fatalf("expected storage client to be non-nil")
		}
		autocertManager = &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			HostPolicy: func(_ context.Context, host string) error {
				if !strings.HasSuffix(host, ".golang.org") {
					return fmt.Errorf("bogus host %q", host)
				}
				return nil
			},
			Cache: autocertcache.NewGoogleCloudStorageCache(storageClient, bucket),
		}
	}

	// TODO(evanbrown: disable kubePool if init fails)
	err = initKube()
	if err != nil {
		kubeErr = err
		log.Printf("Kube support disabled due to error initializing Kubernetes: %v", err)
	}

	go updateInstanceRecord()

	switch *mode {
	case "dev", "prod":
		log.Printf("Running in %s mode", *mode)
	default:
		log.Fatalf("Unknown mode: %q", *mode)
	}

	cc, err := grpc.NewClient(http.DefaultClient, "https://maintner.golang.org")
	if err != nil {
		log.Fatal(err)
	}
	maintnerClient = apipb.NewMaintnerServiceClient(cc)

	http.HandleFunc("/", handleStatus)
	http.HandleFunc("/debug/goroutines", handleDebugGoroutines)
	http.HandleFunc("/debug/watcher/", handleDebugWatcher)
	http.HandleFunc("/builders", handleBuilders)
	http.HandleFunc("/temporarylogs", handleLogs)
	http.HandleFunc("/reverse", handleReverse)
	http.Handle("/revdial", revdialv2.ConnHandler())
	http.HandleFunc("/style.css", handleStyleCSS)
	http.HandleFunc("/try", serveTryStatus(false))
	http.HandleFunc("/try.json", serveTryStatus(true))
	http.HandleFunc("/status/reverse.json", reversePool.ServeReverseStatusJSON)
	http.Handle("/buildlet/create", requireBuildletProxyAuth(http.HandlerFunc(handleBuildletCreate)))
	http.Handle("/buildlet/list", requireBuildletProxyAuth(http.HandlerFunc(handleBuildletList)))
	go func() {
		if *mode == "dev" {
			return
		}
		var handler http.Handler = httpToHTTPSRedirector{}
		if autocertManager != nil {
			handler = autocertManager.HTTPHandler(handler)
		}
		err := http.ListenAndServe(":80", handler)
		if err != nil {
			log.Fatalf("http.ListenAndServe:80: %v", err)
		}
	}()

	workc := make(chan buildgo.BuilderRev)

	if *mode == "dev" {
		// TODO(crawshaw): do more in dev mode
		gcePool.SetEnabled(*devEnableGCE)
		http.HandleFunc("/dosomework/", handleDoSomeWork(workc))
	} else {
		go gcePool.cleanUpOldVMs()
		if kubeErr == nil {
			go kubePool.cleanUpOldPodsLoop(context.Background())
		}

		if inStaging {
			dashboard.Builders = stagingClusterBuilders()
		}

		go listenAndServeInternalModuleProxy()
		go findWorkLoop(workc)
		go findTryWorkLoop()
		go reportMetrics(context.Background())
		// TODO(cmang): gccgo will need its own findWorkLoop
	}

	go listenAndServeTLS()
	go listenAndServeSSH() // ssh proxy to remote buildlets; remote.go

	for {
		work := <-workc
		if !mayBuildRev(work) {
			if inStaging {
				if _, ok := dashboard.Builders[work.Name]; ok && logCantBuildStaging.Allow() {
					log.Printf("may not build %v; skipping", work)
				}
			}
			continue
		}
		st, err := newBuild(work)
		if err != nil {
			log.Printf("Bad build work params %v: %v", work, err)
		} else {
			st.start()
		}
	}
}

// httpToHTTPSRedirector redirects all requests from http to https.
type httpToHTTPSRedirector struct{}

func (httpToHTTPSRedirector) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Connection", "close")
	u := *req.URL
	u.Scheme = "https"
	u.Host = req.Host
	http.Redirect(w, req, u.String(), http.StatusMovedPermanently)
}

// watcherProxy is the proxy which forwards from
// https://farmer.golang.org/ to the gitmirror kubernetes service (git
// cache+sync).
// This is used for /debug/watcher/<reponame> status pages, which are
// served at the same URL paths for both the farmer.golang.org host
// and the internal backend. (The name "watcher" is old; it's now called
// "gitmirror" but the URL path remains for now.)
var watcherProxy *httputil.ReverseProxy

func init() {
	u, err := url.Parse("http://gitmirror/") // unused hostname
	if err != nil {
		log.Fatal(err)
	}
	watcherProxy = httputil.NewSingleHostReverseProxy(u)
	watcherProxy.Transport = &http.Transport{
		IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return goKubeClient.DialServicePort(ctx, "gitmirror", "")
		},
	}
}

func handleDebugWatcher(w http.ResponseWriter, r *http.Request) {
	watcherProxy.ServeHTTP(w, r)
}

func stagingClusterBuilders() map[string]*dashboard.BuildConfig {
	m := map[string]*dashboard.BuildConfig{}
	for _, name := range []string{
		"linux-amd64",
		"linux-amd64-sid",
		"linux-amd64-clang",
		"nacl-amd64p32",
	} {
		if c, ok := dashboard.Builders[name]; ok {
			m[name] = c
		} else {
			panic(fmt.Sprintf("unknown builder %q", name))
		}
	}

	// Also permit all the reverse buildlets:
	for name, bc := range dashboard.Builders {
		if bc.IsReverse() {
			m[name] = bc
		}
	}
	return m
}

func numCurrentBuilds() int {
	statusMu.Lock()
	defer statusMu.Unlock()
	return len(status)
}

func numCurrentBuildsOfType(typ string) (n int) {
	statusMu.Lock()
	defer statusMu.Unlock()
	for rev := range status {
		if rev.Name == typ {
			n++
		}
	}
	return
}

func isBuilding(work buildgo.BuilderRev) bool {
	statusMu.Lock()
	defer statusMu.Unlock()
	_, building := status[work]
	return building
}

var (
	logUnknownBuilder   = rate.NewLimiter(rate.Every(5*time.Second), 2)
	logCantBuildStaging = rate.NewLimiter(rate.Every(1*time.Second), 2)
)

// mayBuildRev reports whether the build type & revision should be started.
// It returns true if it's not already building, and if a reverse buildlet is
// required, if an appropriate machine is registered.
func mayBuildRev(rev buildgo.BuilderRev) bool {
	if isBuilding(rev) {
		return false
	}
	buildConf, ok := dashboard.Builders[rev.Name]
	if !ok {
		if logUnknownBuilder.Allow() {
			log.Printf("unknown builder %q", rev.Name)
		}
		return false
	}
	if buildEnv.MaxBuilds > 0 && numCurrentBuilds() >= buildEnv.MaxBuilds {
		return false
	}
	if buildConf.MaxAtOnce > 0 && numCurrentBuildsOfType(rev.Name) >= buildConf.MaxAtOnce {
		return false
	}
	if buildConf.IsReverse() && !reversePool.CanBuild(buildConf.HostType) {
		return false
	}
	return true
}

func setStatus(work buildgo.BuilderRev, st *buildStatus) {
	statusMu.Lock()
	defer statusMu.Unlock()
	// TODO: panic if status[work] already exists. audit all callers.
	// For instance, what if a trybot is running, and then the CL is merged
	// and the findWork goroutine picks it up and it has the same commit,
	// because it didn't need to be rebased in Gerrit's cherrypick?
	// Could we then have two running with the same key?
	status[work] = st
}

func markDone(work buildgo.BuilderRev) {
	statusMu.Lock()
	defer statusMu.Unlock()
	st, ok := status[work]
	if !ok {
		return
	}
	delete(status, work)
	if len(statusDone) == maxStatusDone {
		copy(statusDone, statusDone[1:])
		statusDone = statusDone[:len(statusDone)-1]
	}
	statusDone = append(statusDone, st)
}

// statusPtrStr disambiguates which status to return if there are
// multiple in the history (e.g. recent failures where the build
// didn't finish for reasons outside of all.bash failing)
func getStatus(work buildgo.BuilderRev, statusPtrStr string) *buildStatus {
	statusMu.Lock()
	defer statusMu.Unlock()
	match := func(st *buildStatus) bool {
		return statusPtrStr == "" || fmt.Sprintf("%p", st) == statusPtrStr
	}
	if st, ok := status[work]; ok && match(st) {
		return st
	}
	for _, st := range statusDone {
		if st.BuilderRev == work && match(st) {
			return st
		}
	}
	for k, ts := range tries {
		if k.Commit == work.Rev {
			ts.mu.Lock()
			for _, st := range ts.builds {
				if st.BuilderRev == work && match(st) {
					ts.mu.Unlock()
					return st
				}
			}
			ts.mu.Unlock()
		}
	}
	return nil
}

type byAge []*buildStatus

func (s byAge) Len() int           { return len(s) }
func (s byAge) Less(i, j int) bool { return s[i].startTime.Before(s[j].startTime) }
func (s byAge) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func serveTryStatus(json bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ts := trySetOfCommitPrefix(r.FormValue("commit"))
		var tss trySetState
		if ts != nil {
			ts.mu.Lock()
			tss = ts.trySetState.clone()
			ts.mu.Unlock()
		}
		if json {
			serveTryStatusJSON(w, r, ts, tss)
			return
		}
		serveTryStatusHTML(w, ts, tss)
	}
}

// tss is a clone that does not require ts' lock.
func serveTryStatusJSON(w http.ResponseWriter, r *http.Request, ts *trySet, tss trySetState) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "OPTIONS" {
		// This is likely a pre-flight CORS request.
		return
	}
	var resp struct {
		Success bool        `json:"success"`
		Error   string      `json:"error,omitempty"`
		Payload interface{} `json:"payload,omitempty"`
	}
	if ts == nil {
		var buf bytes.Buffer
		resp.Error = "TryBot result not found (already done, invalid, or not yet discovered from Gerrit). Check Gerrit for results."
		if err := json.NewEncoder(&buf).Encode(resp); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write(buf.Bytes())
		return
	}
	type litebuild struct {
		Name      string    `json:"name"`
		StartTime time.Time `json:"startTime"`
		Done      bool      `json:"done"`
		Succeeded bool      `json:"succeeded"`
	}
	var result struct {
		ChangeID string      `json:"changeId"`
		Commit   string      `json:"commit"`
		Builds   []litebuild `json:"builds"`
	}
	result.Commit = ts.Commit
	result.ChangeID = ts.ChangeID

	for _, bs := range tss.builds {
		var lb litebuild
		bs.mu.Lock()
		lb.Name = bs.Name
		lb.StartTime = bs.startTime
		if !bs.done.IsZero() {
			lb.Done = true
			lb.Succeeded = bs.succeeded
		}
		bs.mu.Unlock()
		result.Builds = append(result.Builds, lb)
	}
	resp.Success = true
	resp.Payload = result
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		log.Printf("Could not encode JSON response: %v", err)
		http.Error(w, "error encoding JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(buf.Bytes())
}

// Styles unique to the trybot status page.
const tryStatusCSS = `
<style>
p {
	line-height: 1.15em;
}

table {
	font-size: 11pt;
}

.nobr {
	white-space: nowrap;
}

</style>
`

// tss is a clone that does not require ts' lock.
func serveTryStatusHTML(w http.ResponseWriter, ts *trySet, tss trySetState) {
	if ts == nil {
		http.Error(w, "TryBot result not found (already done, invalid, or not yet discovered from Gerrit). Check Gerrit for results.", http.StatusNotFound)
		return
	}
	buf := new(bytes.Buffer)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteString("<!DOCTYPE html><head><title>trybot status</title>")
	buf.WriteString(`<link rel="stylesheet" href="/style.css"/>`)
	buf.WriteString(tryStatusCSS)
	buf.WriteString("</head><body>")
	fmt.Fprintf(buf, "[<a href='/'>homepage</a>] &gt; %s\n", ts.ChangeID)
	fmt.Fprintf(buf, "<h1>Trybot Status</h1>")
	fmt.Fprintf(buf, "<p>Change-ID: <a href='https://go-review.googlesource.com/#/q/%s'>%s</a><br />\n", ts.ChangeID, ts.ChangeID)
	fmt.Fprintf(buf, "Commit: <a href='https://go-review.googlesource.com/#/q/%s'>%s</a></p>\n", ts.Commit, ts.Commit)
	fmt.Fprintf(buf, "<p>Builds remaining: %d</p>\n", tss.remain)
	fmt.Fprintf(buf, "<h4>Builds</h4>\n")
	fmt.Fprintf(buf, "<table cellpadding=5 border=0>\n")
	for _, bs := range tss.builds {
		var status string
		bs.mu.Lock()
		if !bs.done.IsZero() {
			if bs.succeeded {
				status = "pass"
			} else {
				if u := bs.failURL; u != "" {
					status = fmt.Sprintf("<a href='%s'><b>FAIL</b></a>", html.EscapeString(u))
				} else {
					status = "<b>FAIL</b>"
				}
			}
		} else {
			status = fmt.Sprintf("<i>running</i> %s", time.Since(bs.startTime).Round(time.Second))
		}
		bs.mu.Unlock()
		fmt.Fprintf(buf, "<tr><td class='nobr'>&#8226; %s</td><td>%s</td></tr>\n",
			html.EscapeString(bs.NameAndBranch()), status)
	}
	fmt.Fprintf(buf, "</table>\n")
	fmt.Fprintf(buf, "<h4>Full Detail</h4><table cellpadding=5 border=1>\n")
	for _, bs := range tss.builds {
		status := "<i>(running)</i>"
		bs.mu.Lock()
		if !bs.done.IsZero() {
			if bs.succeeded {
				status = "pass"
			} else {
				status = "<b>FAIL</b>"
			}
		}
		bs.mu.Unlock()
		fmt.Fprintf(buf, "<tr valign=top><td align=left>%s</td><td align=center>%s</td><td><pre>%s</pre></td></tr>\n",
			html.EscapeString(bs.NameAndBranch()),
			status,
			bs.HTMLStatusLine())
	}
	fmt.Fprintf(buf, "</table>")
	w.Write(buf.Bytes())
}

func trySetOfCommitPrefix(commitPrefix string) *trySet {
	if commitPrefix == "" {
		return nil
	}
	statusMu.Lock()
	defer statusMu.Unlock()
	for k, ts := range tries {
		if strings.HasPrefix(k.Commit, commitPrefix) {
			return ts
		}
	}
	return nil
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	br := buildgo.BuilderRev{
		Name:    r.FormValue("name"),
		Rev:     r.FormValue("rev"),
		SubName: r.FormValue("subName"), // may be empty
		SubRev:  r.FormValue("subRev"),  // may be empty
	}
	st := getStatus(br, r.FormValue("st"))
	if st == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeStatusHeader(w, st)

	nostream := r.FormValue("nostream") != ""
	if nostream || !st.isRunning() {
		if nostream {
			fmt.Fprintf(w, "\n\n(live streaming disabled; reload manually to see status)\n")
		}
		w.Write(st.output.Bytes())
		return
	}

	if !st.hasEvent("make_and_test") && !st.hasEvent("make_cross_compile_kube") {
		fmt.Fprintf(w, "\n\n(buildlet still starting; no live streaming. reload manually to see status)\n")
		return
	}

	w.(http.Flusher).Flush()

	output := st.output.Reader()
	go func() {
		<-w.(http.CloseNotifier).CloseNotify()
		output.Close()
	}()
	buf := make([]byte, 65536)
	for {
		n, err := output.Read(buf)
		if _, err2 := w.Write(buf[:n]); err2 != nil {
			return
		}
		w.(http.Flusher).Flush()
		if err != nil {
			break
		}
	}
}

func handleDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	w.Write(buf)
}

func writeStatusHeader(w http.ResponseWriter, st *buildStatus) {
	st.mu.Lock()
	defer st.mu.Unlock()
	fmt.Fprintf(w, "  builder: %s\n", st.Name)
	fmt.Fprintf(w, "      rev: %s\n", st.Rev)
	workaroundFlush(w)
	fmt.Fprintf(w, " buildlet: %s\n", st.bc)
	fmt.Fprintf(w, "  started: %v\n", st.startTime)
	done := !st.done.IsZero()
	if done {
		fmt.Fprintf(w, "    ended: %v\n", st.done)
		fmt.Fprintf(w, "  success: %v\n", st.succeeded)
	} else {
		fmt.Fprintf(w, "   status: still running\n")
	}
	if len(st.events) > 0 {
		io.WriteString(w, "\nEvents:\n")
		st.writeEventsLocked(w, false)
	}
	io.WriteString(w, "\nBuild log:\n")
	workaroundFlush(w)
}

// workaroundFlush is an unnecessary flush to work around a bug in Chrome.
// See https://code.google.com/p/chromium/issues/detail?id=2016 for the details.
// In summary: a couple unnecessary chunk flushes bypass the content type
// sniffing which happen (even if unused?), even if you set nosniff as we do
// in func handleLogs.
func workaroundFlush(w http.ResponseWriter) {
	w.(http.Flusher).Flush()
}

// findWorkLoop polls https://build.golang.org/?mode=json looking for new work
// for the main dashboard. It does not support gccgo.
func findWorkLoop(work chan<- buildgo.BuilderRev) {
	// Useful for debugging a single run:
	if inStaging && false {
		const debugSubrepo = false
		if debugSubrepo {
			work <- buildgo.BuilderRev{
				Name:    "linux-arm",
				Rev:     "c9778ec302b2e0e0d6027e1e0fca892e428d9657",
				SubName: "tools",
				SubRev:  "ac303766f5f240c1796eeea3dc9bf34f1261aa35",
			}
		}
		const debugArm = false
		if debugArm {
			for !reversePool.CanBuild("host-linux-arm") {
				log.Printf("waiting for ARM to register.")
				time.Sleep(time.Second)
			}
			log.Printf("ARM machine(s) registered.")
			work <- buildgo.BuilderRev{Name: "linux-arm", Rev: "3129c67db76bc8ee13a1edc38a6c25f9eddcbc6c"}
		} else {
			work <- buildgo.BuilderRev{Name: "linux-amd64", Rev: "9b16b9c7f95562bb290f5015324a345be855894d"}
			work <- buildgo.BuilderRev{Name: "linux-amd64-sid", Rev: "9b16b9c7f95562bb290f5015324a345be855894d"}
			work <- buildgo.BuilderRev{Name: "linux-amd64-clang", Rev: "9b16b9c7f95562bb290f5015324a345be855894d"}
		}

		// Still run findWork but ignore what it does.
		ignore := make(chan buildgo.BuilderRev)
		go func() {
			for range ignore {
			}
		}()
		work = ignore
	}
	ticker := time.NewTicker(15 * time.Second)
	for {
		if err := findWork(work); err != nil {
			log.Printf("failed to find new work: %v", err)
		}
		<-ticker.C
	}
}

func findWork(work chan<- buildgo.BuilderRev) error {
	var bs types.BuildStatus
	if err := dash("GET", "", url.Values{"mode": {"json"}}, nil, &bs); err != nil {
		return err
	}
	knownToDashboard := map[string]bool{} // keys are builder
	for _, b := range bs.Builders {
		knownToDashboard[b] = true
	}

	// Before, we just sent all the possible work to workc,
	// which then kicks off lots of goroutines that fight over
	// available buildlets, with the result that we run a random
	// subset of the possible work. But really we want to run
	// the newest possible work, so that lines at the top of the
	// build dashboard are filled in before lines below.
	// It's a bit hard to push that preference all the way through
	// this code base, but we can tilt the scales a little by only
	// sending one job to workc for each different builder
	// on each findWork call. The findWork calls happen every
	// 15 seconds, so we will now only kick off one build of
	// a particular host type (for example, darwin-arm64) every
	// 15 seconds, but they should be skewed toward new work.
	// This depends on the build dashboard sending back the list
	// of empty slots newest first (matching the order on the main screen).
	sent := map[string]bool{}

	var goRevisions []string // revisions of repo "go", branch "master" revisions
	seenSubrepo := make(map[string]bool)
	for _, br := range bs.Revisions {
		if br.Repo == "grpc-review" {
			// Skip the grpc repo. It's only for reviews
			// for now (using LetsUseGerrit).
			continue
		}
		awaitSnapshot := false
		if br.Repo == "go" {
			if br.Branch == "master" {
				goRevisions = append(goRevisions, br.Revision)
			}
		} else {
			// If this is the first time we've seen this sub-repo
			// in this loop, then br.GoRevision is the go repo
			// HEAD.  To save resources, we only build subrepos
			// against HEAD once we have a snapshot.
			// The next time we see this sub-repo in this loop, the
			// GoRevision is one of the release branches, for which
			// we may not have a snapshot (if the release was made
			// a long time before this builder came up), so skip
			// the snapshot check.
			awaitSnapshot = !seenSubrepo[br.Repo]
			seenSubrepo[br.Repo] = true
		}

		if len(br.Results) != len(bs.Builders) {
			return errors.New("bogus JSON response from dashboard: results is too long.")
		}
		for i, res := range br.Results {
			if res != "" {
				// It's either "ok" or a failure URL.
				continue
			}
			builder := bs.Builders[i]
			builderInfo, ok := dashboard.Builders[builder]
			if !ok {
				// Not managed by the coordinator.
				continue
			}
			if !builderInfo.BuildsRepoPostSubmit(br.Repo, br.Branch, br.GoBranch) {
				continue
			}
			var rev buildgo.BuilderRev
			if br.Repo == "go" {
				rev = buildgo.BuilderRev{
					Name: builder,
					Rev:  br.Revision,
				}
			} else {
				rev = buildgo.BuilderRev{
					Name:    builder,
					Rev:     br.GoRevision,
					SubName: br.Repo,
					SubRev:  br.Revision,
				}
				if awaitSnapshot && !rev.SnapshotExists(context.TODO(), buildEnv) {
					continue
				}
			}

			// The !sent[builder] here is a clumsy attempt at priority scheduling
			// and probably should be replaced at some point with a better solution.
			// See golang.org/issue/19178 and the long comment above.
			if !isBuilding(rev) && !sent[builder] {
				sent[builder] = true
				work <- rev
			}
		}
	}

	// And to bootstrap new builders, see if we have any builders
	// that the dashboard doesn't know about.
	for b, builderInfo := range dashboard.Builders {
		if knownToDashboard[b] ||
			!builderInfo.BuildsRepoPostSubmit("go", "master", "master") {
			continue
		}
		for _, rev := range goRevisions {
			br := buildgo.BuilderRev{Name: b, Rev: rev}
			if !isBuilding(br) {
				work <- br
			}
		}
	}
	return nil
}

// findTryWorkLoop is a goroutine which loops periodically and queries
// Gerrit for TryBot work.
func findTryWorkLoop() {
	if errTryDeps != nil {
		return
	}
	ticker := time.NewTicker(1 * time.Second)
	for {
		if err := findTryWork(); err != nil {
			log.Printf("failed to find trybot work: %v", err)
		}
		<-ticker.C
	}
}

func findTryWork() error {
	if inStaging && !stagingTryWork {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // should be milliseconds
	defer cancel()
	tryRes, err := maintnerClient.GoFindTryWork(ctx, &apipb.GoFindTryWorkRequest{ForStaging: inStaging})
	if err != nil {
		return err
	}

	now := time.Now()

	statusMu.Lock()
	defer statusMu.Unlock()

	tryList = tryList[:0]
	for _, work := range tryRes.Waiting {
		if work.ChangeId == "" || work.Commit == "" {
			log.Printf("Warning: skipping incomplete %#v", work)
			continue
		}
		if work.Project == "grpc-review" {
			// Skip grpc-review, which is only for reviews for now.
			continue
		}
		key := tryWorkItemKey(work)
		tryList = append(tryList, key)
		if ts, ok := tries[key]; ok {
			// already in progress
			ts.wantedAsOf = now
			continue
		} else {
			ts := newTrySet(work)
			ts.wantedAsOf = now
			tries[key] = ts
		}
	}
	for k, ts := range tries {
		if ts.wantedAsOf != now {
			delete(tries, k)
			go ts.cancelBuilds()
		}
	}
	return nil
}

type tryKey struct {
	Project  string // "go", "net", etc
	Branch   string // master
	ChangeID string // I1a27695838409259d1586a0adfa9f92bccf7ceba
	Commit   string // ecf3dffc81dc21408fb02159af352651882a8383
}

// ChangeTriple returns the Gerrit (project, branch, change-ID) triple
// uniquely identifying this change. Several Gerrit APIs require this
// form of if there are multiple changes with the same Change-ID.
func (k *tryKey) ChangeTriple() string {
	return fmt.Sprintf("%s~%s~%s", k.Project, k.Branch, k.ChangeID)
}

// trySet is a the state of a set of builds of different
// configurations, all for the same (Change-ID, Commit) pair.  The
// sets which are still wanted (not already submitted or canceled) are
// stored in the global 'tries' map.
type trySet struct {
	// immutable
	tryKey
	tryID string // "T" + 9 random hex

	// wantedAsOf is guarded by statusMu and is used by
	// findTryWork. It records the last time this tryKey was still
	// wanted.
	wantedAsOf time.Time

	// mu guards state and errMsg
	// See LOCK ORDER comment above.
	mu sync.Mutex
	trySetState
	errMsg bytes.Buffer
}

type trySetState struct {
	remain       int
	failed       []string // builder names, with optional " ($branch)" suffix
	builds       []*buildStatus
	benchResults []string // builder names, with optional " ($branch)" suffix
}

func (ts trySetState) clone() trySetState {
	return trySetState{
		remain:       ts.remain,
		failed:       append([]string(nil), ts.failed...),
		builds:       append([]*buildStatus(nil), ts.builds...),
		benchResults: append([]string(nil), ts.benchResults...),
	}
}

var errHeadUnknown = errors.New("Cannot create trybot set without a known Go head (transient error)")

func tryWorkItemKey(work *apipb.GerritTryWorkItem) tryKey {
	return tryKey{
		Project:  work.Project,
		Branch:   work.Branch,
		ChangeID: work.ChangeId,
		Commit:   work.Commit,
	}
}

var testingKnobSkipBuilds bool

// newTrySet creates a new trySet group of builders for a given
// work item, the (Project, Branch, Change-ID, Commit) tuple.
// It also starts goroutines for each build.
//
// Must hold statusMu.
func newTrySet(work *apipb.GerritTryWorkItem) *trySet {
	key := tryWorkItemKey(work)
	goBranch := key.Branch // assume same as repo's branch for now
	builders := dashboard.TryBuildersForProject(key.Project, key.Branch, goBranch)
	log.Printf("Starting new trybot set for %v", key)
	ts := &trySet{
		tryKey: key,
		tryID:  "T" + randHex(9),
		trySetState: trySetState{
			builds: make([]*buildStatus, 0, len(builders)),
		},
	}

	// Defensive check that the input is well-formed.
	// Each GoCommit should have a GoBranch and a GoVersion.
	// There should always be at least one GoVersion.
	if len(work.GoBranch) < len(work.GoCommit) {
		log.Printf("WARNING: len(GoBranch) of %d != len(GoCommit) of %d", len(work.GoBranch), len(work.GoCommit))
		work.GoCommit = work.GoCommit[:len(work.GoBranch)]
	}
	if len(work.GoVersion) < len(work.GoCommit) {
		log.Printf("WARNING: len(GoVersion) of %d != len(GoCommit) of %d", len(work.GoVersion), len(work.GoCommit))
		work.GoCommit = work.GoCommit[:len(work.GoVersion)]
	}
	if len(work.GoVersion) == 0 {
		log.Print("WARNING: len(GoVersion) is zero, want at least one")
		work.GoVersion = []*apipb.MajorMinor{{}}
	}

	// GoCommit is non-empty for x/* repos (aka "subrepos"). It
	// is the Go revision to use to build & test the x/* repo
	// with. The first element is the master branch. We test the
	// master branch against all the normal builders configured to
	// do subrepos. Any GoCommit values past the first are for older
	// release branches, but we use a limited subset of builders for those.
	var goRev string
	for i, branch := range work.GoBranch {
		if branch == work.Branch {
			goRev = work.GoCommit[i]
		}
	}

	addBuilderToSet := func(bs *buildStatus, brev buildgo.BuilderRev) {
		bs.trySet = ts
		status[brev] = bs

		idx := len(ts.builds)
		ts.builds = append(ts.builds, bs)
		ts.remain++
		if testingKnobSkipBuilds {
			return
		}
		go bs.start() // acquires statusMu itself, so in a goroutine
		go ts.awaitTryBuild(idx, bs, brev)
	}

	if !testingKnobSkipBuilds {
		go ts.notifyStarting()
	}
	for _, bconf := range builders {
		goVersion := types.MajorMinor{int(work.GoVersion[0].Major), int(work.GoVersion[0].Minor)}
		if goVersion.Less(bconf.MinimumGoVersion) {
			continue
		}
		brev := tryKeyToBuilderRev(bconf.Name, key, goRev)
		bs, err := newBuild(brev)
		if err != nil {
			log.Printf("can't create build for %q: %v", brev, err)
			continue
		}
		addBuilderToSet(bs, brev)
	}

	// For subrepos on the "master" branch, test against prior releases of Go too.
	if key.Project != "go" && key.Branch == "master" {
		// linuxBuilder is the standard builder we run for when testing x/* repos against
		// the past two Go releases.
		linuxBuilder := dashboard.Builders["linux-amd64"]

		// If there's more than one GoCommit, that means this is an x/* repo
		// and we're testing against previous releases of Go.
		for i, goRev := range work.GoCommit {
			if i == 0 {
				// Skip the i==0 element, which is handled above.
				continue
			}
			branch := work.GoBranch[i]
			if !linuxBuilder.BuildsRepoTryBot(key.Project, "master", branch) {
				continue
			}
			goVersion := types.MajorMinor{int(work.GoVersion[i].Major), int(work.GoVersion[i].Minor)}
			if goVersion.Less(linuxBuilder.MinimumGoVersion) {
				continue
			}
			brev := tryKeyToBuilderRev(linuxBuilder.Name, key, goRev)
			bs, err := newBuild(brev)
			if err != nil {
				log.Printf("can't create build for %q: %v", brev, err)
				continue
			}
			bs.goBranch = branch
			addBuilderToSet(bs, brev)
		}
	}

	return ts
}

// Note: called in some paths where statusMu is held; do not make RPCs.
func tryKeyToBuilderRev(builder string, key tryKey, goRev string) buildgo.BuilderRev {
	// This function is called from within newTrySet, holding statusMu, s
	if key.Project == "go" {
		return buildgo.BuilderRev{
			Name: builder,
			Rev:  key.Commit,
		}
	}
	return buildgo.BuilderRev{
		Name:    builder,
		Rev:     goRev,
		SubName: key.Project,
		SubRev:  key.Commit,
	}
}

// state returns a copy of the trySet's state.
func (ts *trySet) state() trySetState {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.trySetState.clone()
}

// notifyStarting runs in its own goroutine and posts to Gerrit that
// the trybots have started on the user's CL with a link of where to watch.
func (ts *trySet) notifyStarting() {
	msg := "TryBots beginning. Status page: https://farmer.golang.org/try?commit=" + ts.Commit[:8]

	ctx := context.Background()
	if ci, err := gerritClient.GetChangeDetail(ctx, ts.ChangeTriple()); err == nil {
		if len(ci.Messages) == 0 {
			log.Printf("No Gerrit comments retrieved on %v", ts.ChangeTriple())
		}
		for _, cmi := range ci.Messages {
			if strings.Contains(cmi.Message, msg) {
				// Dup. Don't spam.
				return
			}
		}
	} else {
		log.Printf("Error getting Gerrit comments on %s: %v", ts.ChangeTriple(), err)
	}

	// Ignore error. This isn't critical.
	gerritClient.SetReview(ctx, ts.ChangeTriple(), ts.Commit, gerrit.ReviewInput{Message: msg})
}

// awaitTryBuild runs in its own goroutine and waits for a build in a
// trySet to complete.
//
// If the build fails without getting to the end, it sleeps and
// reschedules it, as long as it's still wanted.
func (ts *trySet) awaitTryBuild(idx int, bs *buildStatus, brev buildgo.BuilderRev) {
	for {
	WaitCh:
		for {
			timeout := time.NewTimer(10 * time.Minute)
			select {
			case <-bs.ctx.Done():
				timeout.Stop()
				break WaitCh
			case <-timeout.C:
				if !ts.wanted() {
					// Build was canceled.
					return
				}
			}
		}

		if bs.hasEvent(eventDone) || bs.hasEvent(eventSkipBuildMissingDep) {
			ts.noteBuildComplete(bs)
			return
		}

		// TODO(bradfitz): rethink this logic. we should only
		// start a new build if the old one appears dead or
		// hung.

		// Sleep a bit and retry.
		time.Sleep(30 * time.Second)
		if !ts.wanted() {
			return
		}
		bs, _ = newBuild(brev)
		bs.trySet = ts
		go bs.start()
		ts.mu.Lock()
		ts.builds[idx] = bs
		ts.mu.Unlock()
	}
}

// wanted reports whether this trySet is still active.
//
// If the commmit has been submitted, or change abandoned, or the
// checkbox unchecked, wanted returns false.
func (ts *trySet) wanted() bool {
	statusMu.Lock()
	defer statusMu.Unlock()
	_, ok := tries[ts.tryKey]
	return ok
}

// cancelBuilds run in its own goroutine and cancels this trySet's
// currently-active builds because they're no longer wanted.
func (ts *trySet) cancelBuilds() {
	// TODO(bradfitz): implement
}

func (ts *trySet) noteBuildComplete(bs *buildStatus) {
	bs.mu.Lock()
	succeeded := bs.succeeded
	var buildLog string
	if !succeeded {
		buildLog = bs.output.String()
	}
	hasBenchResults := bs.hasBenchResults
	bs.mu.Unlock()

	ts.mu.Lock()
	if hasBenchResults {
		ts.benchResults = append(ts.benchResults, bs.NameAndBranch())
	}
	ts.remain--
	remain := ts.remain
	if !succeeded {
		ts.failed = append(ts.failed, bs.NameAndBranch())
	}
	numFail := len(ts.failed)
	benchResults := append([]string(nil), ts.benchResults...)
	ts.mu.Unlock()

	const failureFooter = "Consult https://build.golang.org/ to see whether they are new failures. Keep in mind that TryBots currently test *exactly* your git commit, without rebasing. If your commit's git parent is old, the failure might've already been fixed."

	if !succeeded {
		s1 := sha1.New()
		io.WriteString(s1, buildLog)
		objName := fmt.Sprintf("%s/%s_%x.log", bs.Rev[:8], bs.Name, s1.Sum(nil)[:4])
		wr, failLogURL := newFailureLogBlob(objName)
		if _, err := io.WriteString(wr, buildLog); err != nil {
			log.Printf("Failed to write to GCS: %v", err)
			return
		}
		if err := wr.Close(); err != nil {
			log.Printf("Failed to write to GCS: %v", err)
			return
		}

		bs.mu.Lock()
		bs.failURL = failLogURL
		bs.mu.Unlock()
		ts.mu.Lock()
		fmt.Fprintf(&ts.errMsg, "Failed on %s: %s\n", bs.NameAndBranch(), failLogURL)
		ts.mu.Unlock()

		if numFail == 1 && remain > 0 {
			if err := gerritClient.SetReview(context.Background(), ts.ChangeTriple(), ts.Commit, gerrit.ReviewInput{
				Message: fmt.Sprintf(
					"Build is still in progress...\n"+
						"This change failed on %s:\n"+
						"See %s\n\n"+
						"Other builds still in progress; subsequent failure notices suppressed until final report. "+failureFooter,
					bs.NameAndBranch(), failLogURL),
			}); err != nil {
				log.Printf("Failed to call Gerrit: %v", err)
				return
			}
		}
	}

	if remain == 0 {
		score, msg := 1, "TryBots are happy."
		if numFail > 0 {
			ts.mu.Lock()
			errMsg := ts.errMsg.String()
			ts.mu.Unlock()
			score, msg = -1, fmt.Sprintf("%d of %d TryBots failed:\n%s\n"+failureFooter,
				numFail, len(ts.builds), errMsg)
		}
		if len(benchResults) > 0 {
			// TODO: restore this functionality
			// msg += fmt.Sprintf("\nBenchmark results are available at:\nhttps://perf.golang.org/search?q=cl:%d+try:%s", ts.ci.ChangeNumber, ts.tryID)
		}
		if err := gerritClient.SetReview(context.Background(), ts.ChangeTriple(), ts.Commit, gerrit.ReviewInput{
			Message: msg,
			Labels: map[string]int{
				"TryBot-Result": score,
			},
		}); err != nil {
			log.Printf("Failed to call Gerrit: %v", err)
			return
		}
	}
}

type eventTimeLogger interface {
	LogEventTime(event string, optText ...string)
}

// logger is the logging interface used within the coordinator.
// It can both log a message at a point in time, as well
// as log a span (something having a start and end time, as well as
// a final success status).
type logger interface {
	eventTimeLogger // point in time
	spanlog.Logger  // action spanning time
}

// buildletTimeoutOpt is a context.Value key for BuildletPool.GetBuildlet.
type buildletTimeoutOpt struct{} // context Value key; value is time.Duration

// highPriorityOpt is a context.Value key for BuildletPool.GetBuildlet.
// If its value is true, that means the caller should be prioritized.
type highPriorityOpt struct{} // value is bool

type BuildletPool interface {
	// GetBuildlet returns a new buildlet client.
	//
	// The hostType is the key into the dashboard.Hosts
	// map (such as "host-linux-jessie"), NOT the buidler type
	// ("linux-386").
	//
	// Users of GetBuildlet must both call Client.Close when done
	// with the client as well as cancel the provided Context.
	//
	// The ctx may have context values of type buildletTimeoutOpt
	// and highPriorityOpt.
	GetBuildlet(ctx context.Context, hostType string, lg logger) (*buildlet.Client, error)

	// HasCapacity reports whether the buildlet pool has
	// quota/capacity to create a buildlet of the provided host
	// type. This should return as fast as possible and err on
	// the side of returning false.
	HasCapacity(hostType string) bool

	String() string // TODO(bradfitz): more status stuff
}

// GetBuildlets creates up to n buildlets and sends them on the returned channel
// before closing the channel.
func GetBuildlets(ctx context.Context, pool BuildletPool, n int, hostType string, lg logger) <-chan *buildlet.Client {
	ch := make(chan *buildlet.Client) // NOT buffered
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			sp := lg.CreateSpan("get_helper", fmt.Sprintf("helper %d/%d", i+1, n))
			bc, err := pool.GetBuildlet(ctx, hostType, lg)
			sp.Done(err)
			if err != nil {
				if err != context.Canceled {
					log.Printf("failed to get a %s buildlet: %v", hostType, err)
				}
				return
			}
			lg.LogEventTime("empty_helper_ready", bc.Name())
			select {
			case ch <- bc:
			case <-ctx.Done():
				lg.LogEventTime("helper_killed_before_use", bc.Name())
				bc.Close()
				return
			}
		}(i)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}

var testPoolHook func(*dashboard.BuildConfig) BuildletPool

func poolForConf(conf *dashboard.BuildConfig) BuildletPool {
	if testPoolHook != nil {
		return testPoolHook(conf)
	}
	switch {
	case conf.IsVM():
		return gcePool
	case conf.IsContainer():
		if buildEnv.PreferContainersOnCOS || kubeErr != nil {
			return gcePool // it also knows how to do containers.
		} else {
			return kubePool
		}
	case conf.IsReverse():
		return reversePool
	default:
		panic(fmt.Sprintf("no buildlet pool for builder type %q", conf.Name))
	}
}

func newBuild(rev buildgo.BuilderRev) (*buildStatus, error) {
	// Note: can't acquire statusMu in newBuild, as this is called
	// from findTryWork -> newTrySet, which holds statusMu.

	conf, ok := dashboard.Builders[rev.Name]
	if !ok {
		return nil, fmt.Errorf("unknown builder type %q", rev.Name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &buildStatus{
		buildID:    "B" + randHex(9),
		BuilderRev: rev,
		conf:       conf,
		startTime:  time.Now(),
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// start starts the build in a new goroutine.
// The buildStatus's context is closed when the build is complete,
// successfully or not.
func (st *buildStatus) start() {
	setStatus(st.BuilderRev, st)
	go func() {
		err := st.build()
		if err == errSkipBuildDueToDeps {
			st.setDone(true)
		} else {
			if err != nil {
				fmt.Fprintf(st, "\n\nError: %v\n", err)
				log.Println(st.BuilderRev, "failed:", err)
			}
			st.setDone(err == nil)
			putBuildRecord(st.buildRecord())
		}
		markDone(st.BuilderRev)
	}()
}

func (st *buildStatus) buildletPool() BuildletPool {
	return poolForConf(st.conf)
}

// parentRev returns the parent of this build's commit (but only if this build comes from a trySet).
func (st *buildStatus) parentRev() (pbr buildgo.BuilderRev, err error) {
	err = errors.New("TODO: query maintner")
	return
}

func (st *buildStatus) expectedMakeBashDuration() time.Duration {
	// TODO: base this on historical measurements, instead of statically configured.
	// TODO: move this to dashboard/builders.go? But once we based on on historical
	// measurements, it'll need GCE services (bigtable/bigquery?), so it's probably
	// better in this file.
	goos, goarch := st.conf.GOOS(), st.conf.GOARCH()

	if goos == "linux" {
		if goarch == "arm" {
			return 4 * time.Minute
		}
		return 45 * time.Second
	}
	return 60 * time.Second
}

func (st *buildStatus) expectedBuildletStartDuration() time.Duration {
	// TODO: move this to dashboard/builders.go? But once we based on on historical
	// measurements, it'll need GCE services (bigtable/bigquery?), so it's probably
	// better in this file.
	pool := st.buildletPool()
	switch pool.(type) {
	case *gceBuildletPool:
		if strings.HasPrefix(st.Name, "android-") {
			// about a minute for buildlet + minute for Android emulator to be usable
			return 2 * time.Minute
		}
		return time.Minute
	case *reverseBuildletPool:
		goos, arch := st.conf.GOOS(), st.conf.GOARCH()
		if goos == "darwin" {
			if arch == "arm" || arch == "arm64" {
				// iOS; idle or it's not.
				return 0
			}
			if arch == "amd64" || arch == "386" {
				return 0 // TODO: remove this once we're using VMware
				// return 1 * time.Minute // VMware boot of hermetic OS X
			}
		}
		if goos == "linux" && arch == "arm" {
			// Scaleway. Ready or not.
			return 0
		}
	}
	return 0
}

// getHelpersReadySoon waits a bit (as a function of the build
// configuration) and starts getting the buildlets for test sharding
// ready, such that they're ready when make.bash is done. But we don't
// want to start too early, lest we waste idle resources during make.bash.
func (st *buildStatus) getHelpersReadySoon() {
	if st.IsSubrepo() || st.conf.NumTestHelpers(st.isTry()) == 0 || st.conf.IsReverse() {
		return
	}
	time.AfterFunc(st.expectedMakeBashDuration()-st.expectedBuildletStartDuration(),
		func() {
			st.LogEventTime("starting_helpers")
			st.getHelpers() // and ignore the result.
		})
}

// getHelpers returns a channel of buildlet test helpers, with an item
// sent as they become available. The channel is closed at the end.
func (st *buildStatus) getHelpers() <-chan *buildlet.Client {
	st.onceInitHelpers.Do(st.onceInitHelpersFunc)
	return st.helpers
}

func (st *buildStatus) onceInitHelpersFunc() {
	pool := st.buildletPool()
	st.helpers = GetBuildlets(st.ctx, pool, st.conf.NumTestHelpers(st.isTry()), st.conf.HostType, st)
}

// useSnapshot reports whether this type of build uses a snapshot of
// make.bash if it exists (anything can SplitMakeRun) and that the
// snapshot exists.
func (st *buildStatus) useSnapshot() bool {
	if st.conf.SkipSnapshot {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.useSnapshotMemo != nil {
		return *st.useSnapshotMemo
	}
	b := st.conf.SplitMakeRun() && st.BuilderRev.SnapshotExists(context.TODO(), buildEnv)
	st.useSnapshotMemo = &b
	return b
}

func (st *buildStatus) forceSnapshotUsage() {
	st.mu.Lock()
	defer st.mu.Unlock()
	truth := true
	st.useSnapshotMemo = &truth
}

func (st *buildStatus) getCrossCompileConfig() *crossCompileConfig {
	if kubeErr != nil {
		return nil
	}
	config := crossCompileConfigs[st.Name]
	if config == nil {
		return nil
	}
	if config.AlwaysCrossCompile {
		return config
	}
	if inStaging || st.isTry() {
		return config
	}
	return nil
}

func (st *buildStatus) checkDep(ctx context.Context, dep string) (have bool, err error) {
	span := st.CreateSpan("ask_maintner_has_ancestor")
	defer func() { span.Done(err) }()
	tries := 0
	for {
		tries++
		res, err := maintnerClient.HasAncestor(ctx, &apipb.HasAncestorRequest{
			Commit:   st.Rev,
			Ancestor: dep,
		})
		if err != nil {
			if tries == 3 {
				span.Done(err)
				return false, err
			}
			time.Sleep(1 * time.Second)
			continue
		}
		if res.UnknownCommit {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(1 * time.Second):
			}
			continue
		}
		return res.HasAncestor, nil
	}
}

var errSkipBuildDueToDeps = errors.New("build was skipped due to missing deps")

func (st *buildStatus) build() error {
	if deps := st.conf.GoDeps; len(deps) > 0 {
		ctx, cancel := context.WithTimeout(st.ctx, 30*time.Second)
		defer cancel()
		for _, dep := range deps {
			has, err := st.checkDep(ctx, dep)
			if err != nil {
				fmt.Fprintf(st, "Error checking whether commit %s includes ancestor %s: %v\n", st.Rev, dep, err)
				return err
			}
			if !has {
				st.LogEventTime(eventSkipBuildMissingDep)
				fmt.Fprintf(st, "skipping build; commit %s lacks ancestor %s\n", st.Rev, dep)
				return errSkipBuildDueToDeps
			}
		}
		cancel()
	}

	putBuildRecord(st.buildRecord())

	sp := st.CreateSpan("checking_for_snapshot")
	if inStaging {
		err := storageClient.Bucket(buildEnv.SnapBucket).Object(st.SnapshotObjectName()).Delete(context.Background())
		st.LogEventTime("deleted_snapshot", fmt.Sprint(err))
	}
	snapshotExists := st.useSnapshot()
	sp.Done(nil)

	if config := st.getCrossCompileConfig(); config != nil {
		if !snapshotExists {
			if err := st.crossCompileMakeAndSnapshot(config); err != nil {
				return err
			}
		}
		st.forceSnapshotUsage()
		st.getHelpers()
	}

	sp = st.CreateSpan("get_buildlet")
	pool := st.buildletPool()
	bc, err := sched.GetBuildlet(st.ctx, st, &SchedItem{
		HostType:   st.conf.HostType,
		IsTry:      st.trySet != nil,
		Pool:       pool,
		BuilderRev: st.BuilderRev,
	})
	sp.Done(err)
	if err != nil {
		err = fmt.Errorf("failed to get a buildlet: %v", err)
		go st.reportErr(err)
		return err
	}
	atomic.StoreInt32(&st.hasBuildlet, 1)
	defer bc.Close()
	st.mu.Lock()
	st.bc = bc
	st.mu.Unlock()

	st.LogEventTime("using_buildlet", bc.IPPort())

	if st.useSnapshot() {
		sp := st.CreateSpan("write_snapshot_tar")
		if err := bc.PutTarFromURL(st.SnapshotURL(buildEnv), "go"); err != nil {
			return sp.Done(fmt.Errorf("failed to put snapshot to buildlet: %v", err))
		}
		sp.Done(nil)
	} else {
		// Write the Go source and bootstrap tool chain in parallel.
		var grp syncutil.Group
		grp.Go(st.writeGoSource)
		grp.Go(st.writeBootstrapToolchain)
		if err := grp.Err(); err != nil {
			return err
		}
	}

	execStartTime := time.Now()
	fmt.Fprintf(st, "%s at %v", st.Name, st.Rev)
	if st.IsSubrepo() {
		fmt.Fprintf(st, " building %v at %v", st.SubName, st.SubRev)
	}
	fmt.Fprint(st, "\n\n")

	makeTest := st.CreateSpan("make_and_test") // warning: magic event named used by handleLogs

	var remoteErr error
	if st.conf.SplitMakeRun() {
		remoteErr, err = st.runAllSharded()
	} else {
		remoteErr, err = st.runAllLegacy()
	}
	makeTest.Done(err)

	// bc (aka st.bc) may be invalid past this point, so let's
	// close it to make sure we we don't accidentally use it.
	bc.Close()

	doneMsg := "all tests passed"
	if remoteErr != nil {
		doneMsg = "with test failures"
	} else if err != nil {
		doneMsg = "comm error: " + err.Error()
	}
	// If a build fails multiple times due to communication
	// problems with the buildlet, assume something's wrong with
	// the buildlet or machine and fail the build, rather than
	// looping forever. This promotes the err (communication
	// error) to a remoteErr (an error that occurred remotely and
	// is terminal).
	if rerr := st.repeatedCommunicationError(err); rerr != nil {
		remoteErr = rerr
		err = nil
		doneMsg = "communication error to buildlet (promoted to terminal error): " + rerr.Error()
		fmt.Fprintf(st, "\n%s\n", doneMsg)
	}
	if err != nil {
		// Return the error *before* we create the magic
		// "done" event. (which the try coordinator looks for)
		return err
	}
	st.LogEventTime(eventDone, doneMsg)

	if devPause {
		st.LogEventTime("DEV_MAIN_SLEEP")
		time.Sleep(5 * time.Minute)
	}

	if st.trySet == nil {
		var buildLog string
		if remoteErr != nil {
			buildLog = st.logs()
			// If we just have the line-or-so little
			// banner at top, that means we didn't get any
			// interesting output from the remote side, so
			// include the remoteErr text.  Otherwise
			// assume that remoteErr is redundant with the
			// buildlog text itself.
			if strings.Count(buildLog, "\n") < 10 {
				buildLog += "\n" + remoteErr.Error()
			}
		}
		if err := recordResult(st.BuilderRev, remoteErr == nil, buildLog, time.Since(execStartTime)); err != nil {
			if remoteErr != nil {
				return fmt.Errorf("Remote error was %q but failed to report it to the dashboard: %v", remoteErr, err)
			}
			return fmt.Errorf("Build succeeded but failed to report it to the dashboard: %v", err)
		}
	}
	if remoteErr != nil {
		return remoteErr
	}
	return nil
}

func (st *buildStatus) isTry() bool { return st.trySet != nil }

func (st *buildStatus) buildRecord() *types.BuildRecord {
	rec := &types.BuildRecord{
		ID:        st.buildID,
		ProcessID: processID,
		StartTime: st.startTime,
		IsTry:     st.isTry(),
		GoRev:     st.Rev,
		Rev:       st.SubRevOrGoRev(),
		Repo:      st.RepoOrGo(),
		Builder:   st.Name,
		OS:        st.conf.GOOS(),
		Arch:      st.conf.GOARCH(),
	}

	// Log whether we used COS, so we can do queries to analyze
	// Kubernetes vs COS performance for containers.
	if st.conf.IsContainer() && poolForConf(st.conf) == gcePool {
		rec.ContainerHost = "cos"
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	// TODO: buildlet instance name
	if !st.done.IsZero() {
		rec.EndTime = st.done
		rec.FailureURL = st.failURL
		rec.Seconds = rec.EndTime.Sub(rec.StartTime).Seconds()
		if st.succeeded {
			rec.Result = "ok"
		} else {
			rec.Result = "fail"
		}
	}
	return rec
}

func (st *buildStatus) spanRecord(sp *span, err error) *types.SpanRecord {
	rec := &types.SpanRecord{
		BuildID: st.buildID,
		IsTry:   st.isTry(),
		GoRev:   st.Rev,
		Rev:     st.SubRevOrGoRev(),
		Repo:    st.RepoOrGo(),
		Builder: st.Name,
		OS:      st.conf.GOOS(),
		Arch:    st.conf.GOARCH(),

		Event:     sp.event,
		Detail:    sp.optText,
		StartTime: sp.start,
		EndTime:   sp.end,
		Seconds:   sp.end.Sub(sp.start).Seconds(),
	}
	if err != nil {
		rec.Error = err.Error()
	}
	return rec
}

// shouldBench returns whether we should attempt to run benchmarks
func (st *buildStatus) shouldBench() bool {
	if !*shouldRunBench {
		return false
	}
	return st.isTry() && !st.IsSubrepo() && st.conf.RunBench
}

// goBuilder returns a GoBuilder for this buildStatus.
func (st *buildStatus) goBuilder() buildgo.GoBuilder {
	return buildgo.GoBuilder{
		Logger:     st,
		BuilderRev: st.BuilderRev,
		Conf:       st.conf,
		Goroot:     "go",
	}
}

// runAllSharded runs make.bash and then shards the test execution.
// remoteErr and err are as described at the top of this file.
//
// After runAllSharded returns, the caller must assume that st.bc
// might be invalid (It's possible that only one of the helper
// buildlets survived).
func (st *buildStatus) runAllSharded() (remoteErr, err error) {
	st.getHelpersReadySoon()

	if !st.useSnapshot() {
		remoteErr, err = st.goBuilder().RunMake(st.bc, st)
		if err != nil {
			return nil, err
		}
		if remoteErr != nil {
			return fmt.Errorf("build failed: %v", remoteErr), nil
		}
	}
	if st.conf.StopAfterMake {
		return nil, nil
	}

	if err := st.doSnapshot(st.bc); err != nil {
		return nil, err
	}

	if st.IsSubrepo() {
		remoteErr, err = st.runSubrepoTests()
	} else {
		remoteErr, err = st.runTests(st.getHelpers())
	}

	if err == errBuildletsGone {
		// Don't wrap this error. TODO: use xerrors.
		return nil, errBuildletsGone
	}
	if err != nil {
		return nil, fmt.Errorf("runTests: %v", err)
	}
	if remoteErr != nil {
		return fmt.Errorf("tests failed: %v", remoteErr), nil
	}
	return nil, nil
}

type crossCompileConfig struct {
	Buildlet           string
	CCForTarget        string
	GOARM              string
	AlwaysCrossCompile bool

	// BuildTests cross-compiles all the standard library
	// packages' tests, leaving the pkg.test binary in each std
	// package directory. This is recognized by cmd/dist when
	// GO_BUILDER_NAME is set and when only one test is being run
	// with dist at a time. (So BuildConfig.DisableTestGrouping
	// must also be set true).
	// It requires xargs (from Debian package findutils) be
	// installed on the VM.
	BuildTests bool
}

// TODO: move this into dashboard/builders.go.
var crossCompileConfigs = map[string]*crossCompileConfig{
	"linux-arm": {
		Buildlet:           "host-linux-armhf-cross",
		CCForTarget:        "arm-linux-gnueabihf-gcc",
		GOARM:              "7",
		AlwaysCrossCompile: false,
	},
	"linux-arm-arm5spacemonkey": {
		Buildlet:           "host-linux-armel-cross",
		CCForTarget:        "arm-linux-gnueabi-gcc",
		GOARM:              "5",
		AlwaysCrossCompile: true,
	},
	"linux-mips64le": {
		Buildlet:           "host-linux-mips64le-cross",
		CCForTarget:        "mips64el-linux-gnuabi64-gcc",
		AlwaysCrossCompile: true,
		BuildTests:         true,
	},
}

func (st *buildStatus) crossCompileMakeAndSnapshot(config *crossCompileConfig) (err error) {
	// TODO: currently we ditch this buildlet when we're done with
	// the make.bash & snapshot. For extra speed later, we could
	// keep it around and use it to "go test -c" each stdlib
	// package's tests, and push the binary to each ARM helper
	// machine. That might be too little gain for the complexity,
	// though, or slower once we ship everything around.
	ctx, cancel := context.WithCancel(st.ctx)
	defer cancel()
	sp := st.CreateSpan("get_buildlet_cross")
	kubeBC, err := sched.GetBuildlet(ctx, st, &SchedItem{
		HostType:   config.Buildlet,
		IsTry:      st.trySet != nil,
		Pool:       kubePool,
		BuilderRev: st.BuilderRev,
	})
	sp.Done(err)
	if err != nil {
		err = fmt.Errorf("cross-compile and snapshot: failed to get a buildlet: %v", err)
		go st.reportErr(err)
		return err
	}
	defer kubeBC.Close()

	if err := st.writeGoSourceTo(kubeBC); err != nil {
		return err
	}

	makeSpan := st.CreateSpan("make_cross_compile_kube")
	defer func() { makeSpan.Done(err) }()

	goos, goarch := st.conf.GOOS(), st.conf.GOARCH()

	var buildTests string
	if config.BuildTests {
		buildTests = `($WORKDIR/go/bin/go list -f '{{if or .TestGoFiles .XTestGoFiles }}{{.Dir}}{{end}}' std | ` +
			`xargs --max-procs=4 --max-args=1 -I % bash -c 'cd %; echo %; $WORKDIR/go/bin/go test -c -vet=off') &&`
	}

	remoteErr, err := kubeBC.Exec("/bin/bash", buildlet.ExecOpts{
		SystemLevel: true,
		Args: []string{
			"-c",
			"cd $WORKDIR/go/src && " +
				"./make.bash && " +
				buildTests +
				"cd .. && " +
				"mv bin/*_*/* bin && " +
				"rmdir bin/*_* && " +
				"rm -rf pkg/linux_amd64 pkg/tool/linux_amd64 pkg/bootstrap pkg/obj",
		},
		Output: st,
		ExtraEnv: []string{
			"GOROOT_BOOTSTRAP=/go1.4",
			"CGO_ENABLED=1",
			"CC_FOR_TARGET=" + config.CCForTarget,
			"GOOS=" + goos,
			"GOARCH=" + goarch,
			"GOARM=" + config.GOARM, // harmless if GOARCH != "arm"
		},
		Debug: true,
	})
	if err != nil {
		return err
	}
	if remoteErr != nil {
		// Add the "done" event if make.bash fails, otherwise
		// try builders will loop forever:
		st.LogEventTime(eventDone, fmt.Sprintf("make.bash failed: %v", remoteErr))
		return fmt.Errorf("remote error: %v", remoteErr)
	}

	if err := st.doSnapshot(kubeBC); err != nil {
		return err
	}

	return nil
}

// runAllLegacy executes all.bash (or .bat, or whatever) in the traditional way.
// remoteErr and err are as described at the top of this file.
//
// TODO(bradfitz,adg): delete this function when all builders
// can split make & run (and then delete the SplitMakeRun method)
func (st *buildStatus) runAllLegacy() (remoteErr, err error) {
	allScript := st.conf.AllScript()
	sp := st.CreateSpan("legacy_all_path", allScript)
	remoteErr, err = st.bc.Exec(path.Join("go", allScript), buildlet.ExecOpts{
		Output:   st,
		ExtraEnv: st.conf.Env(),
		Debug:    true,
		Args:     st.conf.AllScriptArgs(),
	})
	if err != nil {
		sp.Done(err)
		return nil, err
	}
	if remoteErr != nil {
		sp.Done(err)
		return fmt.Errorf("all script failed: %v", remoteErr), nil
	}
	sp.Done(nil)
	return nil, nil
}

func (st *buildStatus) doSnapshot(bc *buildlet.Client) error {
	// If we're using a pre-built snapshot, don't make another.
	if st.useSnapshot() {
		return nil
	}
	if st.conf.SkipSnapshot {
		return nil
	}
	if err := st.cleanForSnapshot(bc); err != nil {
		return fmt.Errorf("cleanForSnapshot: %v", err)
	}
	if err := st.writeSnapshot(bc); err != nil {
		return fmt.Errorf("writeSnapshot: %v", err)
	}
	return nil
}

func (st *buildStatus) writeGoSource() error {
	return st.writeGoSourceTo(st.bc)
}

func (st *buildStatus) writeGoSourceTo(bc *buildlet.Client) error {
	// Write the VERSION file.
	sp := st.CreateSpan("write_version_tar")
	version := "devel " + st.Rev
	if cc := st.getCrossCompileConfig(); cc != nil {
		// Something that doesn't start with "devel ".
		// Otherwise the cmd/go build caching doesn't end up
		// with the same buildids between the host & target.
		version = "go.devel." + st.Rev
	}
	if err := bc.PutTar(buildgo.VersionTgz(version), "go"); err != nil {
		return sp.Done(fmt.Errorf("writing VERSION tgz: %v", err))
	}

	srcTar, err := sourcecache.GetSourceTgz(st, "go", st.Rev)
	if err != nil {
		return err
	}
	sp = st.CreateSpan("write_go_src_tar")
	if err := bc.PutTar(srcTar, "go"); err != nil {
		return sp.Done(fmt.Errorf("writing tarball from Gerrit: %v", err))
	}
	return sp.Done(nil)
}

func (st *buildStatus) writeBootstrapToolchain() error {
	u := st.conf.GoBootstrapURL(buildEnv)
	if u == "" {
		return nil
	}
	const bootstrapDir = "go1.4" // might be newer; name is the default
	sp := st.CreateSpan("write_go_bootstrap_tar")
	return sp.Done(st.bc.PutTarFromURL(u, bootstrapDir))
}

func (st *buildStatus) cleanForSnapshot(bc *buildlet.Client) error {
	sp := st.CreateSpan("clean_for_snapshot")
	return sp.Done(bc.RemoveAll(
		"go/doc/gopher",
		"go/pkg/bootstrap",
	))
}

func (st *buildStatus) writeSnapshot(bc *buildlet.Client) (err error) {
	sp := st.CreateSpan("write_snapshot_to_gcs")
	defer func() { sp.Done(err) }()
	// This should happen in 15 seconds or so, but I saw timeouts
	// a couple times at 1 minute. Some buildlets might be far
	// away on the network, so be more lenient. The timeout mostly
	// is here to prevent infinite hangs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tsp := st.CreateSpan("fetch_snapshot_reader_from_buildlet")
	tgz, err := bc.GetTar(ctx, "go")
	tsp.Done(err)
	if err != nil {
		return err
	}
	defer tgz.Close()

	wr := storageClient.Bucket(buildEnv.SnapBucket).Object(st.SnapshotObjectName()).NewWriter(ctx)
	wr.ContentType = "application/octet-stream"
	wr.ACL = append(wr.ACL, storage.ACLRule{Entity: storage.AllUsers, Role: storage.RoleReader})
	if _, err := io.Copy(wr, tgz); err != nil {
		st.logf("failed to write snapshot to GCS: %v", err)
		wr.CloseWithError(err)
		return err
	}

	return wr.Close()
}

// reportErr reports an error to Stackdriver.
func (st *buildStatus) reportErr(err error) {
	if errorsClient == nil {
		// errorsClient is nil in dev environments.
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = fmt.Errorf("buildID: %v, name: %s, hostType: %s, error: %v", st.buildID, st.conf.Name, st.conf.HostType, err)
	errorsClient.ReportSync(ctx, errorreporting.Entry{Error: err})
}

func (st *buildStatus) distTestList() (names []string, remoteErr, err error) {
	workDir, err := st.bc.WorkDir()
	if err != nil {
		err = fmt.Errorf("distTestList, WorkDir: %v", err)
		return
	}
	goroot := st.conf.FilePathJoin(workDir, "go")

	args := []string{"tool", "dist", "test", "--no-rebuild", "--list"}
	if st.conf.IsRace() {
		args = append(args, "--race")
	}
	if st.conf.CompileOnly {
		args = append(args, "--compile-only")
	}
	var buf bytes.Buffer
	remoteErr, err = st.bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
		Output:      &buf,
		ExtraEnv:    append(st.conf.Env(), "GOROOT="+goroot),
		OnStartExec: func() { st.LogEventTime("discovering_tests") },
		Path:        []string{"$WORKDIR/go/bin", "$PATH"},
		Args:        args,
	})
	if remoteErr != nil {
		remoteErr = fmt.Errorf("Remote error: %v, %s", remoteErr, buf.Bytes())
		err = nil
		return
	}
	if err != nil {
		err = fmt.Errorf("Exec error: %v, %s", err, buf.Bytes())
		return
	}
	for _, test := range strings.Fields(buf.String()) {
		if !st.conf.ShouldRunDistTest(test, st.isTry()) {
			continue
		}
		names = append(names, test)
	}
	return names, nil, nil
}

// newTestSet returns a new testSet given the dist test names (strings from "go tool dist test -list")
// and benchmark items.
func (st *buildStatus) newTestSet(testStats *buildstats.TestStats, distTestNames []string, benchmarks []*buildgo.BenchmarkItem) (*testSet, error) {
	set := &testSet{
		st:        st,
		testStats: testStats,
	}
	for _, name := range distTestNames {
		set.items = append(set.items, &testItem{
			set:      set,
			name:     name,
			duration: testStats.Duration(st.BuilderRev.Name, name),
			take:     make(chan token, 1),
			done:     make(chan token),
		})
	}
	for _, bench := range benchmarks {
		name := "bench:" + bench.Name()
		set.items = append(set.items, &testItem{
			set:      set,
			name:     name,
			bench:    bench,
			duration: testStats.Duration(st.BuilderRev.Name, name),
			take:     make(chan token, 1),
			done:     make(chan token),
		})
	}
	return set, nil
}

func partitionGoTests(testDuration func(string, string) time.Duration, builderName string, tests []string) (sets [][]string) {
	var srcTests []string
	var cmdTests []string
	for _, name := range tests {
		if strings.HasPrefix(name, "go_test:cmd/") {
			cmdTests = append(cmdTests, name)
		} else if strings.HasPrefix(name, "go_test:") {
			srcTests = append(srcTests, name)
		}
	}
	sort.Strings(srcTests)
	sort.Strings(cmdTests)
	goTests := append(srcTests, cmdTests...)

	const sizeThres = 10 * time.Second

	var curSet []string
	var curDur time.Duration

	bc, _ := dashboard.Builders[builderName]
	disableGrouping := bc != nil && bc.DisableTestGrouping

	flush := func() {
		if len(curSet) > 0 {
			sets = append(sets, curSet)
			curSet = nil
			curDur = 0
		}
	}
	for _, testName := range goTests {
		d := testDuration(builderName, testName)
		if curDur+d > sizeThres {
			flush() // no-op if empty
		}
		curSet = append(curSet, testName)
		curDur += d
		if disableGrouping {
			flush()
		}
	}

	flush()
	return
}

var (
	testStats       atomic.Value // of *buildstats.TestStats
	testStatsLoader singleflight.Group
)

func getTestStats(sl spanlog.Logger) *buildstats.TestStats {
	sp := sl.CreateSpan("get_test_stats")
	ts, ok := testStats.Load().(*buildstats.TestStats)
	if ok && ts.AsOf.After(time.Now().Add(-1*time.Hour)) {
		sp.Done(nil)
		return ts
	}
	v, err, _ := testStatsLoader.Do("", func() (interface{}, error) {
		log.Printf("getTestStats: reloading from BigQuery...")
		sp := sl.CreateSpan("query_test_stats")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		ts, err := buildstats.QueryTestStats(ctx, buildEnv)
		sp.Done(err)
		if err != nil {
			log.Printf("getTestStats: error: %v", err)
			return nil, err
		}
		testStats.Store(ts)
		return ts, nil
	})
	if err != nil {
		sp.Done(err)
		return nil
	}
	sp.Done(nil)
	return v.(*buildstats.TestStats)
}

func (st *buildStatus) runSubrepoTests() (remoteErr, err error) {
	st.LogEventTime("fetching_subrepo", st.SubName)

	workDir, err := st.bc.WorkDir()
	if err != nil {
		err = fmt.Errorf("error discovering workdir for helper %s: %v", st.bc.IPPort(), err)
		return nil, err
	}
	goroot := st.conf.FilePathJoin(workDir, "go")
	gopath := st.conf.FilePathJoin(workDir, "gopath")

	fetched := map[string]bool{}
	toFetch := []string{st.SubName}

	// fetch checks out the provided sub-repo to the buildlet's workspace.
	fetch := func(repo, rev string) error {
		fetched[repo] = true
		return buildgo.FetchSubrepo(st, st.bc, repo, rev)
	}

	// findDeps uses 'go list' on the checked out repo to find its
	// dependencies, and adds any not-yet-fetched deps to toFetched.
	findDeps := func(repo string) (rErr, err error) {
		repoPath := importPathOfRepo(repo)
		var buf bytes.Buffer
		rErr, err = st.bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
			Output:   &buf,
			ExtraEnv: append(st.conf.Env(), "GOROOT="+goroot, "GOPATH="+gopath),
			Path:     []string{"$WORKDIR/go/bin", "$PATH"},
			Args:     []string{"list", "-f", `{{range .Deps}}{{printf "%v\n" .}}{{end}}`, repoPath + "/..."},
		})
		if err != nil {
			return nil, fmt.Errorf("exec go list on buildlet: %v", err)
		}
		if rErr != nil {
			fmt.Fprintf(st, "go list error:\n%s", &buf)
			return rErr, nil
		}
		for _, p := range strings.Fields(buf.String()) {
			if !strings.HasPrefix(p, "golang.org/x/") || strings.HasPrefix(p, repoPath) {
				continue
			}
			repo = strings.TrimPrefix(p, "golang.org/x/")
			if i := strings.Index(repo, "/"); i >= 0 {
				repo = repo[:i]
			}
			if !fetched[repo] {
				toFetch = append(toFetch, repo)
			}
		}
		return nil, nil
	}

	// Recursively fetch the repo and their golang.org/x/*
	// dependencies. Dependencies are always fetched at master,
	// which isn't great but the dashboard data model doesn't
	// track non-golang.org/x/* dependencies. For those, we
	// require on the code under test to be using Go modules.
	for i := 0; i < len(toFetch); i++ {
		repo := toFetch[i]
		if fetched[repo] {
			continue
		}
		// Fetch the HEAD revision by default.
		rev, err := getRepoHead(repo)
		if err != nil {
			return nil, err
		}
		if rev == "" {
			rev = "master" // should happen rarely; ok if it does.
		}
		// For the repo under test, choose that specific revision.
		if i == 0 {
			rev = st.SubRev
		}
		if err := fetch(repo, rev); err != nil {
			return nil, err
		}
		if rErr, err := findDeps(repo); err != nil {
			return nil, err
		} else if rErr != nil {
			// An issue with the package may cause "go list" to
			// fail and this is a legimiate build error.
			return rErr, nil
		}
	}

	sp := st.CreateSpan("running_subrepo_tests", st.SubName)
	defer func() { sp.Done(err) }()

	env := append(st.conf.Env(),
		"GOROOT="+goroot,
		"GOPATH="+gopath,
		"GOPROXY="+moduleProxy(), // GKE value but will be ignored/overwritten by reverse buildlets
	)
	env = append(env, st.conf.ModulesEnv(st.SubName)...)

	args := []string{"test"}
	if !st.conf.IsLongTest() {
		args = append(args, "-short")
	}
	if st.conf.IsRace() {
		args = append(args, "-race")
	}
	args = append(args, importPathOfRepo(st.SubName)+"/...")

	return st.bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
		Debug:    true, // make buildlet print extra debug in output for failures
		Output:   st,
		Dir:      "gopath/src/golang.org/x/" + st.SubName,
		ExtraEnv: env,
		Path:     []string{"$WORKDIR/go/bin", "$PATH"},
		Args:     args,
	})
}

// moduleProxy returns the GOPROXY environment value to use for module-enabled
// tests.
//
// We go through an internal (10.0.0.0/8) proxy that then hits
// https://proxy.golang.org/ so we're still able to firewall
// non-internal outbound connections on builder nodes.
//
// This moduleProxy func in prod mode (when running on GKE) returns an http
// URL to the current GKE pod's IP with a Kubernetes NodePort service
// port that forwards back to the coordinator's 8123. See comment below.
//
// In localhost dev mode it just returns the value of GOPROXY.
func moduleProxy() string {
	// If we're running on localhost, just use the current environment's value.
	if buildEnv == nil || !buildEnv.IsProd {
		// If empty, use installed VCS tools as usual to fetch modules.
		return os.Getenv("GOPROXY")
	}
	// We run a NodePort service on each GKE node
	// (cmd/coordinator/module-proxy-service.yaml) on port 30157
	// that maps back the coordinator's port 8123. (We could round
	// robin over all the GKE nodes' IPs if we wanted, but the
	// coordinator is running on GKE so our node by definition is
	// up, so just use it. It won't be much traffic.)
	// TODO: migrate to a GKE internal load balancer with an internal static IP
	// once we migrate symbolic-datum-552 off a Legacy VPC network to the modern
	// scheme that supports internal static IPs.
	return "http://" + gkeNodeIP + ":30157"
}

// affectedPkgs returns the name of every package affected by this commit.
// The returned list may contain duplicates and is unsorted.
// It is safe to call this on a nil trySet.
func (ts *trySet) affectedPkgs() (pkgs []string) {
	// TODO(quentin): Support non-try commits by asking maintnerd for the affected files.
	if ts == nil {
		return
	}
	// TODO(bradfitz): query maintner for this. Old logic with a *gerrit.ChangeInfo was:
	/*
		rev := ts.ci.Revisions[ts.ci.CurrentRevision]
			for p := range rev.Files {
				if strings.HasPrefix(p, "src/") {
					pkg := path.Dir(p[len("src/"):])
					if pkg != "" {
						pkgs = append(pkgs, pkg)
					}
				}
			}
	*/
	return
}

var errBuildletsGone = errors.New("runTests: dist test failed: all buildlets had network errors or timeouts, yet tests remain")

// runTests is only called for builders which support a split make/run
// (should be everything, at least soon). Currently (2015-05-27) iOS
// and Android and Nacl do not.
//
// After runTests completes, the caller must assume that st.bc might be invalid
// (It's possible that only one of the helper buildlets survived).
func (st *buildStatus) runTests(helpers <-chan *buildlet.Client) (remoteErr, err error) {
	testNames, remoteErr, err := st.distTestList()
	if remoteErr != nil {
		return fmt.Errorf("distTestList remote: %v", remoteErr), nil
	}
	if err != nil {
		return nil, fmt.Errorf("distTestList exec: %v", err)
	}
	var benches []*buildgo.BenchmarkItem
	if st.shouldBench() {
		sp := st.CreateSpan("enumerate_benchmarks")
		rev, err := getRepoHead("benchmarks")
		if err != nil {
			return nil, err
		}
		if rev == "" {
			rev = "master" // should happen rarely; ok if it does.
		}
		b, err := st.goBuilder().EnumerateBenchmarks(st.bc, rev, st.trySet.affectedPkgs())
		sp.Done(err)
		if err == nil {
			benches = b
		}
	}

	testStats := getTestStats(st)

	set, err := st.newTestSet(testStats, testNames, benches)
	if err != nil {
		return nil, err
	}
	st.LogEventTime("starting_tests", fmt.Sprintf("%d tests", len(set.items)))
	startTime := time.Now()

	workDir, err := st.bc.WorkDir()
	if err != nil {
		return nil, fmt.Errorf("error discovering workdir for main buildlet, %s: %v", st.bc.Name(), err)
	}

	mainBuildletGoroot := st.conf.FilePathJoin(workDir, "go")
	mainBuildletGopath := st.conf.FilePathJoin(workDir, "gopath")

	// We use our original buildlet to run the tests in order, to
	// make the streaming somewhat smooth and not incredibly
	// lumpy.  The rest of the buildlets run the largest tests
	// first (critical path scheduling).
	// The buildletActivity WaitGroup is used to track when all
	// the buildlets are dead or done.
	var buildletActivity sync.WaitGroup
	buildletActivity.Add(2) // one per goroutine below (main + helper launcher goroutine)
	go func() {
		defer buildletActivity.Done() // for the per-goroutine Add(2) above
		for !st.bc.IsBroken() {
			tis, ok := set.testsToRunInOrder()
			if !ok {
				select {
				case <-st.ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}
			st.runTestsOnBuildlet(st.bc, tis, mainBuildletGoroot, mainBuildletGopath)
		}
		st.LogEventTime("main_buildlet_broken", st.bc.Name())
	}()
	go func() {
		defer buildletActivity.Done() // for the per-goroutine Add(2) above
		for helper := range helpers {
			buildletActivity.Add(1)
			go func(bc *buildlet.Client) {
				defer buildletActivity.Done() // for the per-helper Add(1) above
				defer st.LogEventTime("closed_helper", bc.Name())
				defer bc.Close()
				if devPause {
					defer time.Sleep(5 * time.Minute)
					defer st.LogEventTime("DEV_HELPER_SLEEP", bc.Name())
				}
				st.LogEventTime("got_empty_test_helper", bc.String())
				if err := bc.PutTarFromURL(st.SnapshotURL(buildEnv), "go"); err != nil {
					log.Printf("failed to extract snapshot for helper %s: %v", bc.Name(), err)
					return
				}
				workDir, err := bc.WorkDir()
				if err != nil {
					log.Printf("error discovering workdir for helper %s: %v", bc.Name(), err)
					return
				}
				st.LogEventTime("test_helper_set_up", bc.Name())
				goroot := st.conf.FilePathJoin(workDir, "go")
				gopath := st.conf.FilePathJoin(workDir, "gopath")
				for !bc.IsBroken() {
					tis, ok := set.testsToRunBiggestFirst()
					if !ok {
						st.LogEventTime("no_new_tests_remain", bc.Name())
						return
					}
					st.runTestsOnBuildlet(bc, tis, goroot, gopath)
				}
				st.LogEventTime("test_helper_is_broken", bc.Name())
			}(helper)
		}
	}()

	// Convert a sync.WaitGroup into a channel.
	// Aside: https://groups.google.com/forum/#!topic/golang-dev/7fjGWuImu5k
	buildletsGone := make(chan struct{})
	go func() {
		buildletActivity.Wait()
		close(buildletsGone)
	}()

	benchFiles := st.benchFiles()

	var lastBanner string
	var serialDuration time.Duration
	for _, ti := range set.items {
	AwaitDone:
		for {
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ti.done: // wait for success
				timer.Stop()
				break AwaitDone
			case <-timer.C:
				st.LogEventTime("still_waiting_on_test", ti.name)
			case <-buildletsGone:
				set.cancelAll()
				return nil, errBuildletsGone
			}
		}

		if ti.bench != nil {
			for i, s := range ti.bench.Output {
				if i < len(benchFiles) {
					benchFiles[i].out.WriteString(s)
				}
			}
		}

		serialDuration += ti.execDuration
		if len(ti.output) > 0 {
			banner, out := parseOutputAndBanner(ti.output)
			if banner != lastBanner {
				lastBanner = banner
				fmt.Fprintf(st, "\n##### %s\n", banner)
			}
			if inStaging {
				out = bytes.TrimSuffix(out, nl)
				st.Write(out)
				fmt.Fprintf(st, " (shard %s; par=%d)\n", ti.shardIPPort, ti.groupSize)
			} else {
				st.Write(out)
			}
		}

		if ti.remoteErr != nil {
			set.cancelAll()
			return fmt.Errorf("dist test failed: %s: %v", ti.name, ti.remoteErr), nil
		}
	}
	elapsed := time.Since(startTime)
	var msg string
	if st.conf.NumTestHelpers(st.isTry()) > 0 {
		msg = fmt.Sprintf("took %v; aggregate %v; saved %v", elapsed, serialDuration, serialDuration-elapsed)
	} else {
		msg = fmt.Sprintf("took %v", elapsed)
	}
	st.LogEventTime("tests_complete", msg)
	fmt.Fprintf(st, "\nAll tests passed.\n")
	for _, f := range benchFiles {
		if f.out.Len() > 0 {
			st.hasBenchResults = true
		}
	}
	if st.hasBenchResults {
		sp := st.CreateSpan("upload_bench_results")
		sp.Done(st.uploadBenchResults(st.ctx, benchFiles))
	}
	return nil, nil
}

func (st *buildStatus) uploadBenchResults(ctx context.Context, files []*benchFile) error {
	s := *perfServer
	if s == "" {
		s = buildEnv.PerfDataURL
	}
	client := &perfstorage.Client{BaseURL: s, HTTPClient: oAuthHTTPClient}
	u := client.NewUpload(ctx)
	for _, b := range files {
		w, err := u.CreateFile(b.name)
		if err != nil {
			u.Abort()
			return err
		}
		if _, err := b.out.WriteTo(w); err != nil {
			u.Abort()
			return err
		}
	}
	status, err := u.Commit()
	if err != nil {
		return err
	}
	st.LogEventTime("bench_upload", status.UploadID)
	return nil
}

// TODO: what is a bench file?
type benchFile struct {
	name string
	out  bytes.Buffer
}

func (st *buildStatus) benchFiles() []*benchFile {
	if !st.shouldBench() {
		return nil
	}
	// TODO: renable benchmarking. Or do it outside of the coordinator, if we end up
	// making the coordinator into just a gomote proxy + scheduler.
	// Old logic was:
	/*
		// We know rev and rev.Commit.Parents[0] exist because BenchmarkItem.buildParent has checked.
		rev := st.trySet.ci.Revisions[st.trySet.ci.CurrentRevision]
		ps := rev.PatchSetNumber
		benchFiles := []*benchFile{
			{name: "orig.txt"},
			{name: fmt.Sprintf("ps%d.txt", ps)},
		}
		fmt.Fprintf(&benchFiles[0].out, "cl: %d\nps: %d\ntry: %s\nbuildlet: %s\nbranch: %s\nrepo: https://go.googlesource.com/%s\n",
			st.trySet.ci.ChangeNumber, ps, st.trySet.tryID,
			st.Name, st.trySet.ci.Branch, st.trySet.ci.Project,
		)
		if inStaging {
			benchFiles[0].out.WriteString("staging: true\n")
		}
		benchFiles[1].out.Write(benchFiles[0].out.Bytes())
		fmt.Fprintf(&benchFiles[0].out, "commit: %s\n", rev.Commit.Parents[0].CommitID)
		fmt.Fprintf(&benchFiles[1].out, "commit: %s\n", st.BuilderRev.Rev)
		return benchFiles
	*/
	return nil
}

const (
	banner       = "XXXBANNERXXX:" // flag passed to dist
	bannerPrefix = "\n" + banner   // with the newline added by dist
)

var bannerPrefixBytes = []byte(bannerPrefix)

func parseOutputAndBanner(b []byte) (banner string, out []byte) {
	if bytes.HasPrefix(b, bannerPrefixBytes) {
		b = b[len(bannerPrefixBytes):]
		nl := bytes.IndexByte(b, '\n')
		if nl != -1 {
			banner = string(b[:nl])
			b = b[nl+1:]
		}
	}
	return banner, b
}

// maxTestExecError is the number of test execution failures at which
// we give up and stop trying and instead permanently fail the test.
// Note that this is not related to whether the test failed remotely,
// but whether we were unable to start or complete watching it run.
// (A communication error)
const maxTestExecErrors = 3

// runTestsOnBuildlet runs tis on bc, using the optional goroot & gopath environment variables.
func (st *buildStatus) runTestsOnBuildlet(bc *buildlet.Client, tis []*testItem, goroot, gopath string) {
	names := make([]string, len(tis))
	for i, ti := range tis {
		names[i] = ti.name
		if i > 0 && (!strings.HasPrefix(ti.name, "go_test:") || !strings.HasPrefix(names[0], "go_test:")) {
			panic("only go_test:* tests may be merged")
		}
	}
	var spanName string
	var detail string
	if len(names) == 1 {
		spanName = "run_test:" + names[0]
		detail = bc.Name()
	} else {
		spanName = "run_tests_multi"
		detail = fmt.Sprintf("%s: %v", bc.Name(), names)
	}
	sp := st.CreateSpan(spanName, detail)

	args := []string{"tool", "dist", "test", "--no-rebuild", "--banner=" + banner}
	if st.conf.IsRace() {
		args = append(args, "--race")
	}
	if st.conf.CompileOnly {
		args = append(args, "--compile-only")
	}
	args = append(args, names...)
	var buf bytes.Buffer
	t0 := time.Now()
	timeout := st.conf.DistTestsExecTimeout(names)
	var remoteErr, err error
	if ti := tis[0]; ti.bench != nil {
		pbr, perr := st.parentRev()
		// TODO(quentin): Error if parent commit could not be determined?
		if perr == nil {
			remoteErr, err = ti.bench.Run(buildEnv, st, st.conf, bc, &buf, []buildgo.BuilderRev{st.BuilderRev, pbr})
		}
	} else {
		env := append(st.conf.Env(),
			"GOROOT="+goroot,
			"GOPATH="+gopath,
			"GOPROXY="+moduleProxy(),
		)
		env = append(env, st.conf.ModulesEnv("go")...)

		remoteErr, err = bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
			// We set Dir to "." instead of the default ("go/bin") so when the dist tests
			// try to run os/exec.Command("go", "test", ...), the LookPath of "go" doesn't
			// return "./go.exe" (which exists in the current directory: "go/bin") and then
			// fail when dist tries to run the binary in dir "$GOROOT/src", since
			// "$GOROOT/src" + "./go.exe" doesn't exist. Perhaps LookPath should return
			// an absolute path.
			Dir:      ".",
			Output:   &buf, // see "maybe stream lines" TODO below
			ExtraEnv: env,
			Timeout:  timeout,
			Path:     []string{"$WORKDIR/go/bin", "$PATH"},
			Args:     args,
		})
	}
	execDuration := time.Since(t0)
	sp.Done(err)
	if err != nil {
		bc.MarkBroken() // prevents reuse
		for _, ti := range tis {
			ti.numFail++
			st.logf("Execution error running %s on %s: %v (numFails = %d)", ti.name, bc, err, ti.numFail)
			if err == buildlet.ErrTimeout {
				ti.failf("Test %q ran over %v limit (%v)", ti.name, timeout, execDuration)
			} else if ti.numFail >= maxTestExecErrors {
				ti.failf("Failed to schedule %q test after %d tries.\n", ti.name, maxTestExecErrors)
			} else {
				ti.retry()
			}
		}
		return
	}

	out := buf.Bytes()
	out = bytes.Replace(out, []byte("\nALL TESTS PASSED (some were excluded)\n"), nil, 1)
	out = bytes.Replace(out, []byte("\nALL TESTS PASSED\n"), nil, 1)

	for _, ti := range tis {
		ti.output = out
		ti.remoteErr = remoteErr
		ti.execDuration = execDuration
		ti.groupSize = len(tis)
		ti.shardIPPort = bc.IPPort()
		close(ti.done)

		// After the first one, make the rest succeed with no output.
		// TODO: maybe stream lines (set Output to a line-reading
		// Writer instead of &buf). for now we just wait for them in
		// ~10 second batches.  Doesn't look as smooth on the output,
		// though.
		out = nil
		remoteErr = nil
		execDuration = 0
	}
}

type testSet struct {
	st        *buildStatus
	items     []*testItem
	testStats *buildstats.TestStats

	mu           sync.Mutex
	inOrder      [][]*testItem
	biggestFirst [][]*testItem
}

// cancelAll cancels all pending tests.
func (s *testSet) cancelAll() {
	for _, ti := range s.items {
		ti.tryTake() // ignore return value
	}
}

func (s *testSet) testsToRunInOrder() (chunk []*testItem, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inOrder == nil {
		s.initInOrder()
	}
	return s.testsFromSlice(s.inOrder)
}

func (s *testSet) testsToRunBiggestFirst() (chunk []*testItem, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.biggestFirst == nil {
		s.initBiggestFirst()
	}
	return s.testsFromSlice(s.biggestFirst)
}

func (s *testSet) testsFromSlice(chunkList [][]*testItem) (chunk []*testItem, ok bool) {
	for _, candChunk := range chunkList {
		for _, ti := range candChunk {
			if ti.tryTake() {
				chunk = append(chunk, ti)
			}
		}
		if len(chunk) > 0 {
			return chunk, true
		}
	}
	return nil, false
}

func (s *testSet) initInOrder() {
	names := make([]string, len(s.items))
	namedItem := map[string]*testItem{}
	for i, ti := range s.items {
		names[i] = ti.name
		namedItem[ti.name] = ti
	}

	// First do the go_test:* ones. partitionGoTests
	// only returns those, which are the ones we merge together.
	stdSets := partitionGoTests(s.testStats.Duration, s.st.BuilderRev.Name, names)
	for _, set := range stdSets {
		tis := make([]*testItem, len(set))
		for i, name := range set {
			tis[i] = namedItem[name]
		}
		s.inOrder = append(s.inOrder, tis)
	}

	// Then do the misc tests, which are always by themselves.
	// (No benefit to merging them)
	for _, ti := range s.items {
		if !strings.HasPrefix(ti.name, "go_test:") {
			s.inOrder = append(s.inOrder, []*testItem{ti})
		}
	}
}

func (s *testSet) initBiggestFirst() {
	items := append([]*testItem(nil), s.items...)
	sort.Sort(sort.Reverse(byTestDuration(items)))
	for _, item := range items {
		s.biggestFirst = append(s.biggestFirst, []*testItem{item})
	}
}

type testItem struct {
	set      *testSet
	name     string        // "go_test:sort"
	duration time.Duration // optional approximate size

	bench *buildgo.BenchmarkItem // If populated, this is a benchmark instead of a regular test.

	take chan token // buffered size 1: sending takes ownership of rest of fields:

	done    chan token // closed when done; guards output & failed
	numFail int        // how many times it's failed to execute

	// groupSize is the number of tests which were run together
	// along with this one with "go dist test".
	// This is 1 for non-std/cmd tests, and usually >1 for std/cmd tests.
	groupSize   int
	shardIPPort string // buildlet's IPPort, for debugging

	// the following are only set for the first item in a group:
	output       []byte
	remoteErr    error         // real test failure (not a communications failure)
	execDuration time.Duration // actual time
}

func (ti *testItem) tryTake() bool {
	select {
	case ti.take <- token{}:
		return true
	default:
		return false
	}
}

func (ti *testItem) isDone() bool {
	select {
	case <-ti.done:
		return true
	default:
		return false
	}
}

// retry reschedules the test to run again, if a machine died before
// or during execution, so its results aren't yet known.
// The caller must own the 'take' semaphore.
func (ti *testItem) retry() {
	// release it to make it available for somebody else to try later:
	<-ti.take
}

func (ti *testItem) failf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	ti.output = []byte(msg)
	ti.remoteErr = errors.New(msg)
	close(ti.done)
}

type byTestDuration []*testItem

func (s byTestDuration) Len() int           { return len(s) }
func (s byTestDuration) Less(i, j int) bool { return s[i].duration < s[j].duration }
func (s byTestDuration) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type eventAndTime struct {
	t    time.Time
	evt  string
	text string
}

// buildStatus is the status of a build.
type buildStatus struct {
	// Immutable:
	buildgo.BuilderRev
	buildID   string // "B" + 9 random hex
	goBranch  string // non-empty for subrepo trybots if not go master branch
	conf      *dashboard.BuildConfig
	startTime time.Time // actually time of newBuild (~same thing); TODO(bradfitz): rename this createTime
	trySet    *trySet   // or nil

	onceInitHelpers sync.Once // guards call of onceInitHelpersFunc
	helpers         <-chan *buildlet.Client
	ctx             context.Context    // used to start the build
	cancel          context.CancelFunc // used to cancel context; for use by setDone only

	hasBuildlet int32 // atomic: non-zero if this build has a buildlet; for status.go.

	hasBenchResults bool // set by runTests, may only be used when build() returns.

	mu              sync.Mutex       // guards following
	failURL         string           // if non-empty, permanent URL of failure
	bc              *buildlet.Client // nil initially, until pool returns one
	done            time.Time        // finished running
	succeeded       bool             // set when done
	output          livelog.Buffer   // stdout and stderr
	startedPinging  bool             // started pinging the go dashboard
	events          []eventAndTime
	useSnapshotMemo *bool // if non-nil, memoized result of useSnapshot
}

func (st *buildStatus) NameAndBranch() string {
	if st.goBranch != "" {
		// For the common and currently-only case of
		// "release-branch.go1.15" say "linux-amd64 (Go 1.15.x)"
		const releasePrefix = "release-branch.go"
		if strings.HasPrefix(st.goBranch, releasePrefix) {
			return fmt.Sprintf("%s (Go %s.x)", st.Name, strings.TrimPrefix(st.goBranch, releasePrefix))
		}
		// But if we ever support building other branches,
		// fall back to something verbose until we add a
		// special case:
		return fmt.Sprintf("%s (go branch %s)", st.Name, st.goBranch)
	}
	return st.Name
}

func (st *buildStatus) setDone(succeeded bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.succeeded = succeeded
	st.done = time.Now()
	st.output.Close()
	st.cancel()
}

func (st *buildStatus) isRunning() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.isRunningLocked()
}

func (st *buildStatus) isRunningLocked() bool { return st.done.IsZero() }

func (st *buildStatus) logf(format string, args ...interface{}) {
	log.Printf("[build %s %s]: %s", st.Name, st.Rev, fmt.Sprintf(format, args...))
}

// span is an event covering a region of time.
// A span ultimately ends in an error or success, and will eventually
// be visualized and logged.
type span struct {
	event   string // event name like "get_foo" or "write_bar"
	optText string // optional details for event
	start   time.Time
	end     time.Time
	el      eventTimeLogger // where we log to at the end; TODO: this will change
}

func createSpan(el eventTimeLogger, event string, optText ...string) *span {
	if len(optText) > 1 {
		panic("usage")
	}
	start := time.Now()
	var opt string
	if len(optText) > 0 {
		opt = optText[0]
	}
	el.LogEventTime(event, opt)
	return &span{
		el:      el,
		event:   event,
		start:   start,
		optText: opt,
	}
}

// Done ends a span.
// It is legal to call Done multiple times. Only the first call
// logs.
// Done always returns its input argument.
func (s *span) Done(err error) error {
	if !s.end.IsZero() {
		return err
	}
	t1 := time.Now()
	s.end = t1
	td := t1.Sub(s.start)
	var text bytes.Buffer
	fmt.Fprintf(&text, "after %s", friendlyDuration(td))
	if err != nil {
		fmt.Fprintf(&text, "; err=%v", err)
	}
	if s.optText != "" {
		fmt.Fprintf(&text, "; %v", s.optText)
	}
	if st, ok := s.el.(*buildStatus); ok {
		putSpanRecord(st.spanRecord(s, err))
	}
	s.el.LogEventTime("finish_"+s.event, text.String())
	return err
}

func (st *buildStatus) CreateSpan(event string, optText ...string) spanlog.Span {
	return createSpan(st, event, optText...)
}

func (st *buildStatus) LogEventTime(event string, optText ...string) {
	if len(optText) > 1 {
		panic("usage")
	}
	if inStaging {
		st.logf("%s %v", event, optText)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	switch event {
	case "finish_get_buildlet", "create_gce_buildlet":
		if !st.startedPinging {
			st.startedPinging = true
			go st.pingDashboard()
		}
	}
	var text string
	if len(optText) > 0 {
		text = optText[0]
	}
	st.events = append(st.events, eventAndTime{
		t:    time.Now(),
		evt:  event,
		text: text,
	})
}

func (st *buildStatus) hasEvent(event string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, e := range st.events {
		if e.evt == event {
			return true
		}
	}
	return false
}

// HTMLStatusLine returns the HTML to show within the <pre> block on
// the main page's list of active builds.
func (st *buildStatus) HTMLStatusLine() template.HTML      { return st.htmlStatusLine(true) }
func (st *buildStatus) HTMLStatusLine_done() template.HTML { return st.htmlStatusLine(false) }

func (st *buildStatus) htmlStatusLine(full bool) template.HTML {
	if st == nil {
		return "[nil]"
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	urlPrefix := "https://go-review.googlesource.com/#/q/"

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<a href='https://github.com/golang/go/wiki/DashboardBuilders'>%s</a> rev <a href='%s%s'>%s</a>",
		st.Name, urlPrefix, st.Rev, st.Rev[:8])
	if st.IsSubrepo() {
		fmt.Fprintf(&buf, " (sub-repo %s rev <a href='%s%s'>%s</a>)",
			st.SubName, urlPrefix, st.SubRev, st.SubRev[:8])
	}
	if ts := st.trySet; ts != nil {
		fmt.Fprintf(&buf, " (<a href='/try?commit=%v'>trybot set</a> for <a href='https://go-review.googlesource.com/#/q/%s'>%s</a>)",
			ts.Commit[:8],
			ts.ChangeTriple(), ts.ChangeID[:8])
	}

	var state string
	if st.done.IsZero() {
		state = "running"
	} else if st.succeeded {
		state = "succeeded"
	} else {
		state = "<font color='#700000'>failed</font>"
	}
	if full {
		fmt.Fprintf(&buf, "; <a href='%s'>%s</a>; %s", html.EscapeString(st.logsURLLocked()), state, html.EscapeString(st.bc.String()))
	} else {
		fmt.Fprintf(&buf, "; <a href='%s'>%s</a>", html.EscapeString(st.logsURLLocked()), state)
	}

	t := st.done
	if t.IsZero() {
		t = st.startTime
	}
	fmt.Fprintf(&buf, ", %v ago", time.Since(t).Round(time.Second))
	if full {
		buf.WriteByte('\n')
		st.writeEventsLocked(&buf, true)
	}
	return template.HTML(buf.String())
}

func (st *buildStatus) logsURLLocked() string {
	if st.failURL != "" {
		return st.failURL
	}
	var urlPrefix string
	if buildEnv == buildenv.Production {
		urlPrefix = "https://farmer.golang.org"
	} else {
		urlPrefix = "http://" + buildEnv.StaticIP
	}
	if *mode == "dev" {
		urlPrefix = "https://localhost:8119"
	}
	u := fmt.Sprintf("%v/temporarylogs?name=%s&rev=%s&st=%p", urlPrefix, st.Name, st.Rev, st)
	if st.IsSubrepo() {
		u += fmt.Sprintf("&subName=%v&subRev=%v", st.SubName, st.SubRev)
	}
	return u
}

// st.mu must be held.
func (st *buildStatus) writeEventsLocked(w io.Writer, htmlMode bool) {
	var lastT time.Time
	for _, evt := range st.events {
		lastT = evt.t
		e := evt.evt
		text := evt.text
		if htmlMode {
			if e == "running_exec" {
				e = fmt.Sprintf("<a href='%s'>%s</a>", html.EscapeString(st.logsURLLocked()), e)
			}
			e = "<b>" + e + "</b>"
			text = "<i>" + html.EscapeString(text) + "</i>"
		}
		fmt.Fprintf(w, "  %v %s %s\n", evt.t.Format(time.RFC3339), e, text)
	}
	if st.isRunningLocked() {
		fmt.Fprintf(w, " %7s (now)\n", fmt.Sprintf("+%0.1fs", time.Since(lastT).Seconds()))
	}

}

func (st *buildStatus) logs() string {
	return st.output.String()
}

func (st *buildStatus) Write(p []byte) (n int, err error) {
	return st.output.Write(p)
}

// repeatedCommunicationError takes a buildlet execution error (a
// network/communication error, as opposed to a remote execution that
// ran and had a non-zero exit status and we heard about) and
// conditionally promotes it to a terminal error. If this returns a
// non-nil value, the execErr should be considered terminal with the
// returned error.
func (st *buildStatus) repeatedCommunicationError(execErr error) error {
	if execErr == nil {
		return nil
	}
	// For now, only do this for plan9, which is flaky (Issue 31261)
	if strings.HasPrefix(st.Name, "plan9-") && execErr == errBuildletsGone {
		// TODO: give it two tries at least later (store state
		// somewhere; global map?). But for now we're going to
		// only give it one try.
		return fmt.Errorf("network error promoted to terminal error: %v", execErr)
	}
	return nil
}

func useGitMirror() bool {
	return *mode != "dev"
}

var nl = []byte("\n")

// getRepoHead returns the commit hash of the latest master HEAD
// for the given repo ("go", "tools", "sys", etc).
func getRepoHead(repo string) (string, error) {
	// This gRPC call should only take a couple milliseconds, but set some timeout
	// to catch network problems. 5 seconds is overkill.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := maintnerClient.GetRef(ctx, &apipb.GetRefRequest{
		GerritServer:  "go.googlesource.com",
		GerritProject: repo,
		Ref:           "refs/heads/master",
	})
	if err != nil {
		return "", fmt.Errorf("looking up ref for %q: %v", repo, err)
	}
	if res.Value == "" {
		return "", fmt.Errorf("no master ref found for %q", repo)
	}
	return res.Value, nil
}

// newFailureLogBlob creates a new object to record a public failure log.
// The objName should be a Google Cloud Storage object name.
// When developing on localhost, the WriteCloser may be of a different type.
func newFailureLogBlob(objName string) (obj io.WriteCloser, url_ string) {
	if *mode == "dev" {
		// TODO(bradfitz): write to disk or something, or
		// something testable. Maybe memory.
		return struct {
			io.Writer
			io.Closer
		}{
			os.Stderr,
			ioutil.NopCloser(nil),
		}, "devmode://fail-log/" + objName
	}
	if storageClient == nil {
		panic("nil storageClient in newFailureBlob")
	}
	bucket := buildEnv.LogBucket

	wr := storageClient.Bucket(bucket).Object(objName).NewWriter(context.Background())
	wr.ContentType = "text/plain; charset=utf-8"
	wr.ACL = append(wr.ACL, storage.ACLRule{
		Entity: storage.AllUsers,
		Role:   storage.RoleReader,
	})

	return wr, fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucket, objName)
}

func randHex(n int) string {
	buf := make([]byte, n/2+1)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("randHex: %v", err)
	}
	return fmt.Sprintf("%x", buf)[:n]
}

// importPathOfRepo returns the Go import path corresponding to the
// root of the given repo (Gerrit project). Because it's a Go import
// path, it always has forward slashes and no trailing slash.
//
// For example:
//   "net"    -> "golang.org/x/net"
//   "crypto" -> "golang.org/x/crypto"
//   "dl"     -> "golang.org/dl"
func importPathOfRepo(repo string) string {
	if repo == "dl" {
		return "golang.org/dl"
	}
	return "golang.org/x/" + repo
}
