// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The coordinator runs the majority of the Go build system.
//
// It is responsible for finding build work and executing it,
// reporting the results to build.golang.org for public display.
//
// For an overview of the Go build system, see the README at
// the root of the x/build repo.
package main // import "golang.org/x/build/cmd/coordinator"

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"time"

	"go4.org/syncutil"

	"cloud.google.com/go/storage"
	"golang.org/x/build"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/lru"
	"golang.org/x/build/internal/singleflight"
	"golang.org/x/build/livelog"
	"golang.org/x/build/types"
	"golang.org/x/time/rate"
)

const subrepoPrefix = "golang.org/x/"

var (
	processStartTime = time.Now()
	processID        = "P" + randHex(9)
)

var Version string // set by linker -X

// devPause is a debug option to pause for 5 minutes after the build
// finishes before destroying buildlets.
const devPause = false

var (
	role          = flag.String("role", "coordinator", "Which role this binary should run as. Valid options: coordinator, watcher")
	masterKeyFile = flag.String("masterkey", "", "Path to builder master key. Else fetched using GCE project attribute 'builder-master-key'.")
	mode          = flag.String("mode", "", "Valid modes are 'dev', 'prod', or '' for auto-detect. dev means localhost development, not be confused with staging on go-dashboard-dev, which is still the 'prod' mode.")
	buildEnvName  = flag.String("env", "", "The build environment configuration to use. Not required if running on GCE.")
	devEnableGCE  = flag.Bool("dev_gce", false, "Whether or not to enable the GCE pool when in dev mode. The pool is enabled by default in prod mode.")
)

// LOCK ORDER:
//   statusMu, buildStatus.mu, trySet.mu
// (Other locks, such as subrepoHead.Mutex or the remoteBuildlet mutex should
// not be used along with other locks)

var (
	statusMu   sync.Mutex // guards the following four structures; see LOCK ORDER comment above
	status     = map[builderRev]*buildStatus{}
	statusDone []*buildStatus         // finished recently, capped to maxStatusDone
	tries      = map[tryKey]*trySet{} // trybot builds
	tryList    []tryKey
)

var (
	tryBuilders    []dashboard.BuildConfig // for testing the go repo
	subTryBuilders []dashboard.BuildConfig // for testing sub-repos
)

func initTryBuilders() {
	names := []string{
		"darwin-amd64-10_11",
		"linux-386",
		"linux-amd64",
		"linux-amd64-race",
		"linux-amd64-ssacheck",
		"freebsd-amd64-gce101",
		"windows-386-gce",
		"windows-amd64-gce",
		"openbsd-amd64-60",
		"nacl-386",
		"nacl-amd64p32",
		"linux-arm",
		"misc-vet-vetall",
	}
	for name := range dashboard.Builders {
		if strings.HasPrefix(name, "misc-compile") {
			names = append(names, name)
		}
	}
	for _, name := range names {
		conf, ok := dashboard.Builders[name]
		if !ok {
			log.Printf("ignoring invalid try builder config %q", name)
			continue
		}
		tryBuilders = append(tryBuilders, conf)
		if conf.BuildSubrepos() {
			subTryBuilders = append(subTryBuilders, conf)
		}
	}
}

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

func readGCSFile(name string) ([]byte, error) {
	if *mode == "dev" {
		b, ok := testFiles[name]
		if !ok {
			return nil, &os.PathError{
				Op:   "open",
				Path: name,
				Err:  os.ErrNotExist,
			}
		}
		return []byte(b), nil
	}

	r, err := storageClient.Bucket(buildEnv.BuildletBucket).Object(name).NewReader(context.Background())
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ioutil.ReadAll(r)
}

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

	server := &http.Server{
		Addr:    ln.Addr().String(),
		Handler: httpRouter{},
	}
	config := &tls.Config{
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{cert},
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

type loggerFunc func(event string, optText ...string)

func (fn loggerFunc) logEventTime(event string, optText ...string) {
	fn(event, optText...)
}

func (fn loggerFunc) createSpan(event string, optText ...string) eventSpan {
	return createSpan(fn, event, optText...)
}

func main() {
	flag.Parse()
	switch *role {
	default:
		log.Fatalf("unsupported role %q", *role)
	case "coordinator":
		// fall through
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

	// TODO(evanbrown: disable kubePool if init fails)
	err = initKube()
	if err != nil {
		kubeErr = err
		log.Printf("Kube support disabled due to eror initializing Kubernetes: %v", err)
	}

	go updateInstanceRecord()

	switch *mode {
	case "dev", "prod":
		log.Printf("Running in %s mode", *mode)
	default:
		log.Fatalf("Unknown mode: %q", *mode)
	}

	http.HandleFunc("/", handleStatus)
	http.HandleFunc("/debug/goroutines", handleDebugGoroutines)
	http.HandleFunc("/debug/watcher/", handleDebugWatcher)
	http.HandleFunc("/builders", handleBuilders)
	http.HandleFunc("/temporarylogs", handleLogs)
	http.HandleFunc("/reverse", handleReverse)
	http.HandleFunc("/style.css", handleStyleCSS)
	http.HandleFunc("/try", handleTryStatus)
	http.HandleFunc("/status/reverse.json", reversePool.ServeReverseStatusJSON)
	http.Handle("/buildlet/create", requireBuildletProxyAuth(http.HandlerFunc(handleBuildletCreate)))
	http.Handle("/buildlet/list", requireBuildletProxyAuth(http.HandlerFunc(handleBuildletList)))
	go func() {
		if *mode == "dev" {
			return
		}
		err := http.ListenAndServe(":80", httpRouter{})
		if err != nil {
			log.Fatalf("http.ListenAndServe:80: %v", err)
		}
	}()

	workc := make(chan builderRev)

	if *mode == "dev" {
		// TODO(crawshaw): do more in test mode
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
		initTryBuilders()

		go findWorkLoop(workc)
		go findTryWorkLoop()
		// TODO(cmang): gccgo will need its own findWorkLoop
	}

	go listenAndServeTLS()

	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case work := <-workc:
			if !mayBuildRev(work) {
				if inStaging {
					if _, ok := dashboard.Builders[work.name]; ok && logCantBuildStaging.Allow() {
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
		case <-ticker.C:
			if numCurrentBuilds() == 0 && time.Now().After(processStartTime.Add(10*time.Minute)) {
				// TODO: halt the whole machine to kill the VM or something
			}
		}
	}
}

// watcherProxy is the proxy which forwards from
// http://farmer.golang.org/ to the gitmirror kubeneretes service (git
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
	watcherProxy.Transport = gitMirrorClient.Transport
}

func handleDebugWatcher(w http.ResponseWriter, r *http.Request) {
	watcherProxy.ServeHTTP(w, r)
}

func stagingClusterBuilders() map[string]dashboard.BuildConfig {
	m := map[string]dashboard.BuildConfig{}
	for _, name := range []string{
		"linux-arm",
		"linux-arm-arm5",
		"linux-amd64",
		"linux-386-387",
		"windows-amd64-gce",
		"windows-386-gce",
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

func isBuilding(work builderRev) bool {
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
func mayBuildRev(rev builderRev) bool {
	if isBuilding(rev) {
		return false
	}
	if buildEnv.MaxBuilds > 0 && numCurrentBuilds() >= buildEnv.MaxBuilds {
		return false
	}
	buildConf, ok := dashboard.Builders[rev.name]
	if !ok {
		if logUnknownBuilder.Allow() {
			log.Printf("unknown builder %q", rev.name)
		}
		return false
	}
	if buildConf.IsReverse() && !reversePool.CanBuild(buildConf.HostType) {
		return false
	}
	if buildConf.IsKube() && kubeErr != nil {
		return false
	}
	return true
}

func setStatus(work builderRev, st *buildStatus) {
	statusMu.Lock()
	defer statusMu.Unlock()
	// TODO: panic if status[work] already exists. audit all callers.
	// For instance, what if a trybot is running, and then the CL is merged
	// and the findWork goroutine picks it up and it has the same commit,
	// because it didn't need to be rebased in Gerrit's cherrypick?
	// Could we then have two running with the same key?
	status[work] = st
}

func markDone(work builderRev) {
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
func getStatus(work builderRev, statusPtrStr string) *buildStatus {
	statusMu.Lock()
	defer statusMu.Unlock()
	match := func(st *buildStatus) bool {
		return statusPtrStr == "" || fmt.Sprintf("%p", st) == statusPtrStr
	}
	if st, ok := status[work]; ok && match(st) {
		return st
	}
	for _, st := range statusDone {
		if st.builderRev == work && match(st) {
			return st
		}
	}
	return nil
}

type byAge []*buildStatus

func (s byAge) Len() int           { return len(s) }
func (s byAge) Less(i, j int) bool { return s[i].startTime.Before(s[j].startTime) }
func (s byAge) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func handleTryStatus(w http.ResponseWriter, r *http.Request) {
	ts := trySetOfCommitPrefix(r.FormValue("commit"))
	if ts == nil {
		http.Error(w, "TryBot result not found (already done, invalid, or not yet discovered from Gerrit). Check Gerrit for results.", http.StatusNotFound)
		return
	}
	ts.mu.Lock()
	tss := ts.trySetState.clone()
	ts.mu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<html><head><title>trybot status</title></head><body>[<a href='/'>overall status</a>] &gt; %s\n", ts.ChangeID)

	fmt.Fprintf(w, "<h1>trybot status</h1>")
	fmt.Fprintf(w, "Change-ID: <a href='https://go-review.googlesource.com/#/q/%s'>%s</a><br>\n", ts.ChangeID, ts.ChangeID)
	fmt.Fprintf(w, "Commit: <a href='https://go-review.googlesource.com/#/q/%s'>%s</a><br>\n", ts.Commit, ts.Commit)
	fmt.Fprintf(w, "<p>Builds remain: %d</p>\n", tss.remain)
	fmt.Fprintf(w, "<p>Builds failed: %v</p>\n", tss.failed)
	fmt.Fprintf(w, "<p>Builds</p><table cellpadding=5 border=1>\n")
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
		fmt.Fprintf(w, "<tr valign=top><td align=left>%s</td><td align=center>%s</td><td><pre>%s</pre></td></tr>\n",
			bs.name,
			status,
			bs.HTMLStatusLine())
	}
	fmt.Fprintf(w, "</table></body></html>")
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
	br := builderRev{
		name:    r.FormValue("name"),
		rev:     r.FormValue("rev"),
		subName: r.FormValue("subName"), // may be empty
		subRev:  r.FormValue("subRev"),  // may be empty
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
	fmt.Fprintf(w, "  builder: %s\n", st.name)
	fmt.Fprintf(w, "      rev: %s\n", st.rev)
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
// TODO(bradfitz): it also currently does not support subrepos.
func findWorkLoop(work chan<- builderRev) {
	// Useful for debugging a single run:
	if inStaging {
		//work <- builderRev{name: "linux-arm", rev: "c9778ec302b2e0e0d6027e1e0fca892e428d9657", subName: "tools", subRev: "ac303766f5f240c1796eeea3dc9bf34f1261aa35"}
		const debugArm = false
		if debugArm {
			for !reversePool.CanBuild("host-linux-arm") {
				log.Printf("waiting for ARM to register.")
				time.Sleep(time.Second)
			}
			log.Printf("ARM machine(s) registered.")
			work <- builderRev{name: "linux-arm", rev: "3129c67db76bc8ee13a1edc38a6c25f9eddcbc6c"}
		} else {
			work <- builderRev{name: "windows-amd64-gce", rev: "3129c67db76bc8ee13a1edc38a6c25f9eddcbc6c"}
			work <- builderRev{name: "windows-386-gce", rev: "3129c67db76bc8ee13a1edc38a6c25f9eddcbc6c"}
		}

		// Still run findWork but ignore what it does.
		ignore := make(chan builderRev)
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

func findWork(work chan<- builderRev) error {
	var bs types.BuildStatus
	res, err := http.Get(buildEnv.DashBase() + "?mode=json")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&bs); err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("unexpected http status %v", res.Status)
	}

	knownToDashboard := map[string]bool{} // keys are builder
	for _, b := range bs.Builders {
		knownToDashboard[b] = true
	}

	var goRevisions []string // revisions of repo "go", branch "master" revisions
	seenSubrepo := make(map[string]bool)
	var setGoRepoHead bool
	for _, br := range bs.Revisions {
		if br.Repo == "grpc-review" {
			// Skip the grpc repo. It's only for reviews
			// for now (using LetsUseGerrit).
			continue
		}
		awaitSnapshot := false
		if br.Repo == "go" {
			if br.Branch == "master" {
				if !setGoRepoHead {
					// First Go revision on page; update repo head.
					setRepoHead(br.Repo, br.Revision)
					setGoRepoHead = true
				}
				goRevisions = append(goRevisions, br.Revision)
			}
		} else {
			// The dashboard provides only the head revision for
			// each sub-repo; store it in subrepoHead for later use.
			setRepoHead(br.Repo, br.Revision)

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
			if skipBranchForBuilder(br.Repo, br.Branch, builder) {
				continue
			}

			builderInfo, ok := dashboard.Builders[builder]
			if !ok || builderInfo.TryOnly {
				// Not managed by the coordinator, or a trybot-only one.
				continue
			}
			if br.Repo != "go" && !builderInfo.BuildSubrepos() {
				// This builder can't build subrepos; skip.
				continue
			}

			var rev builderRev
			if br.Repo == "go" {
				rev = builderRev{
					name: bs.Builders[i],
					rev:  br.Revision,
				}
			} else {
				rev = builderRev{
					name:    bs.Builders[i],
					rev:     br.GoRevision,
					subName: br.Repo,
					subRev:  br.Revision,
				}
				if awaitSnapshot && !rev.snapshotExists() {
					continue
				}
			}
			if rev.skipBuild() {
				continue
			}
			if !isBuilding(rev) {
				work <- rev
			}
		}
	}

	// And to bootstrap new builders, see if we have any builders
	// that the dashboard doesn't know about.
	for b, builderInfo := range dashboard.Builders {
		if builderInfo.TryOnly || knownToDashboard[b] {
			continue
		}
		if skipBranchForBuilder("go", "master", b) {
			continue
		}
		for _, rev := range goRevisions {
			br := builderRev{name: b, rev: rev}
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
	ticker := time.NewTicker(60 * time.Second)
	for {
		if err := findTryWork(); err != nil {
			log.Printf("failed to find trybot work: %v", err)
		}
		<-ticker.C
	}
}

func findTryWork() error {
	if inStaging && true {
		return nil
	}
	cis, err := gerritClient.QueryChanges(context.Background(), "label:Run-TryBot=1 label:TryBot-Result=0 status:open", gerrit.QueryChangesOpt{
		Fields: []string{"CURRENT_REVISION"},
	})
	if err != nil {
		return err
	}

	statusMu.Lock()
	defer statusMu.Unlock()

	tryList = make([]tryKey, 0, len(cis))
	wanted := map[tryKey]bool{}
	for _, ci := range cis {
		if ci.ChangeID == "" || ci.CurrentRevision == "" {
			log.Printf("Warning: skipping incomplete %#v", ci)
			continue
		}
		if ci.Project == "build" || ci.Project == "grpc-review" {
			// Skip trybot request in build repo.
			// Also skip grpc-review, which is only for reviews for now.
			continue
		}
		key := tryKey{
			Project:  ci.Project,
			Branch:   ci.Branch,
			ChangeID: ci.ChangeID,
			Commit:   ci.CurrentRevision,
			Repo:     ci.Project,
		}
		tryList = append(tryList, key)
		wanted[key] = true
		if _, ok := tries[key]; ok {
			// already in progress
			continue
		}
		ts, err := newTrySet(key)
		if err != nil {
			if err == errHeadUnknown {
				continue // benign transient error
			}
			log.Printf("Error creating trySet for %v: %v", key, err)
			continue
		}
		tries[key] = ts
	}
	for k, ts := range tries {
		if !wanted[k] {
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
	Repo     string // "go"
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

	// mu guards state and errMsg
	// See LOCK ORDER comment above.
	mu sync.Mutex
	trySetState
	errMsg bytes.Buffer
}

type trySetState struct {
	remain int
	failed []string // build names
	builds []*buildStatus
}

func (ts trySetState) clone() trySetState {
	return trySetState{
		remain: ts.remain,
		failed: append([]string(nil), ts.failed...),
		builds: append([]*buildStatus(nil), ts.builds...),
	}
}

var errHeadUnknown = errors.New("Cannot create trybot set without a known Go head (transient error)")

// newTrySet creates a new trySet group of builders for a given key,
// the (Change-ID, Commit, Repo) tuple.
// It also starts goroutines for each build.
// This will fail if the current Go repo HEAD is unknown.
//
// Must hold statusMu.
func newTrySet(key tryKey) (*trySet, error) {
	goHead := getRepoHead("go")
	if key.Repo != "go" && goHead == "" {
		// We don't know the go HEAD yet (but we will)
		// so don't create this trySet yet as we don't
		// know which Go revision to build against.
		return nil, errHeadUnknown
	}

	builders := tryBuilders
	if key.Repo != "go" {
		builders = subTryBuilders
	}

	log.Printf("Starting new trybot set for %v", key)
	ts := &trySet{
		tryKey: key,
		tryID:  "T" + randHex(9),
		trySetState: trySetState{
			remain: len(builders),
			builds: make([]*buildStatus, len(builders)),
		},
	}

	go ts.notifyStarting()
	for i, bconf := range builders {
		brev := tryKeyToBuilderRev(bconf.Name, key)
		bs, err := newBuild(brev)
		if err != nil {
			log.Printf("can't create build for %q: %v", brev, err)
			continue
		}
		bs.trySet = ts
		status[brev] = bs
		ts.builds[i] = bs
		go bs.start() // acquires statusMu itself, so in a goroutine
		go ts.awaitTryBuild(i, bconf, bs)
	}
	return ts, nil
}

func tryKeyToBuilderRev(builder string, key tryKey) builderRev {
	if key.Repo == "go" {
		return builderRev{
			name: builder,
			rev:  key.Commit,
		}
	}
	return builderRev{
		name:    builder,
		rev:     getRepoHead("go"),
		subName: key.Repo,
		subRev:  key.Commit,
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
	msg := "TryBots beginning. Status page: http://farmer.golang.org/try?commit=" + ts.Commit[:8]

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
func (ts *trySet) awaitTryBuild(idx int, bconf dashboard.BuildConfig, bs *buildStatus) {
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

		if bs.hasEvent("done") {
			ts.noteBuildComplete(bconf, bs)
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
		brev := tryKeyToBuilderRev(bconf.Name, ts.tryKey)
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

func (ts *trySet) noteBuildComplete(bconf dashboard.BuildConfig, bs *buildStatus) {
	bs.mu.Lock()
	succeeded := bs.succeeded
	var buildLog string
	if !succeeded {
		buildLog = bs.output.String()
	}
	bs.mu.Unlock()

	ts.mu.Lock()
	ts.remain--
	remain := ts.remain
	if !succeeded {
		ts.failed = append(ts.failed, bconf.Name)
	}
	numFail := len(ts.failed)
	ts.mu.Unlock()

	if !succeeded {
		s1 := sha1.New()
		io.WriteString(s1, buildLog)
		objName := fmt.Sprintf("%s/%s_%x.log", bs.rev[:8], bs.name, s1.Sum(nil)[:4])
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
		fmt.Fprintf(&ts.errMsg, "Failed on %s: %s\n", bs.name, failLogURL)
		ts.mu.Unlock()

		if numFail == 1 && remain > 0 {
			if err := gerritClient.SetReview(context.Background(), ts.ChangeTriple(), ts.Commit, gerrit.ReviewInput{
				Message: fmt.Sprintf(
					"Build is still in progress...\n"+
						"This change failed on %s:\n"+
						"See %s\n\n"+
						"Consult https://build.golang.org/ to see whether it's a new failure. Other builds still in progress; subsequent failure notices suppressed until final report.",
					bs.name, failLogURL),
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
			score, msg = -1, fmt.Sprintf("%d of %d TryBots failed:\n%s\nConsult https://build.golang.org/ to see whether they are new failures.",
				numFail, len(ts.builds), errMsg)
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

// builderRev is a build configuration type and a revision.
type builderRev struct {
	name string // e.g. "linux-amd64-race"
	rev  string // lowercase hex core repo git hash

	// optional sub-repository details (both must be present)
	subName string // e.g. "net"
	subRev  string // lowercase hex sub-repo git hash
}

func (br builderRev) skipBuild() bool {
	if strings.HasPrefix(br.name, "netbsd-386") {
		// Hangs during make.bash. TODO: remove once Issue 19339 is fixed.
		return true
	}
	switch br.subName {
	case "build", // has external deps
		"exp",    // always broken, depends on mobile which is broken
		"mobile", // always broken (gl, etc). doesn't compile.
		"term",   // no code yet in repo: "warning: "golang.org/x/term/..." matched no packages"
		"oauth2": // has external deps
		return true
	case "perf":
		if br.name == "linux-amd64-nocgo" {
			// The "perf" repo requires sqlite, which
			// requires cgo. Skip the no-cgo builder.
			return true
		}
	case "net":
		if br.name == "darwin-amd64-10_8" || br.name == "darwin-386-10_8" {
			// One of the tests seems to panic the kernel
			// and kill our buildlet which goes in a loop.
			return true
		}
	}
	return false
}

func (br builderRev) isSubrepo() bool {
	return br.subName != ""
}

func (br builderRev) subRevOrGoRev() string {
	if br.subRev != "" {
		return br.subRev
	}
	return br.rev
}

func (br builderRev) repoOrGo() string {
	if br.subName == "" {
		return "go"
	}
	return br.subName
}

type eventTimeLogger interface {
	logEventTime(event string, optText ...string)
}

// spanLogger is something that has the createSpan method, which
// creates a event spanning some duration which will eventually be
// logged and visualized.
type spanLogger interface {
	// optText is 0 or 1 strings.
	createSpan(event string, optText ...string) eventSpan
}

type eventSpan interface {
	// done marks a span as done.
	// The err is returned unmodified for convenience at callsites.
	done(err error) error
}

// logger is the logging interface used within the coordinator.
// It can both log a message at a point in time, as well
// as log a span (something having a start and end time, as well as
// a final success status).
type logger interface {
	eventTimeLogger // point in time
	spanLogger      // action spanning time
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
	// map (such as "host-linux-kubestd"), NOT the buidler type
	// ("linux-386").
	//
	// Users of GetBuildlet must both call Client.Close when done
	// with the client as well as cancel the provided Context.
	//
	// The ctx may have context values of type buildletTimeoutOpt
	// and highPriorityOpt.
	GetBuildlet(ctx context.Context, hostType string, lg logger) (*buildlet.Client, error)

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
			sp := lg.createSpan("get_helper", fmt.Sprintf("helper %d/%d", i+1, n))
			bc, err := pool.GetBuildlet(ctx, hostType, lg)
			sp.done(err)
			if err != nil {
				if err != context.Canceled {
					log.Printf("failed to get a %s buildlet: %v", hostType, err)
				}
				return
			}
			lg.logEventTime("empty_helper_ready", bc.Name())
			select {
			case ch <- bc:
			case <-ctx.Done():
				lg.logEventTime("helper_killed_before_use", bc.Name())
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

func poolForConf(conf dashboard.BuildConfig) BuildletPool {
	switch {
	case conf.IsGCE():
		return gcePool
	case conf.IsKube():
		return kubePool // Kubernetes
	case conf.IsReverse():
		return reversePool
	default:
		panic(fmt.Sprintf("no buildlet pool for builder type %q", conf.Name))
	}
}

func newBuild(rev builderRev) (*buildStatus, error) {
	// Note: can't acquire statusMu in newBuild, as this is called
	// from findTryWork -> newTrySet, which holds statusMu.

	conf, ok := dashboard.Builders[rev.name]
	if !ok {
		return nil, fmt.Errorf("unknown builder type %q", rev.name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &buildStatus{
		buildID:    "B" + randHex(9),
		builderRev: rev,
		conf:       conf,
		startTime:  time.Now(),
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// start starts the build in a new goroutine.
// The buildStatus's context is closed on when the build is complete,
// successfully or not.
func (st *buildStatus) start() {
	setStatus(st.builderRev, st)
	go func() {
		err := st.build()
		if err != nil {
			fmt.Fprintf(st, "\n\nError: %v\n", err)
			log.Println(st.builderRev, "failed:", err)
		}
		st.setDone(err == nil)
		putBuildRecord(st.buildRecord())
		markDone(st.builderRev)
	}()
}

func (st *buildStatus) buildletPool() BuildletPool {
	return poolForConf(st.conf)
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
		return time.Minute
	case *reverseBuildletPool:
		goos, arch := st.conf.GOOS(), st.conf.GOARCH()
		if goos == "darwin" {
			if arch == "arm" || arch == "arm64" {
				// iOS; idle or it's not.
				return 0
			}
			if arch == "amd64" || arch == "386" {
				return 0               // TODO: remove this once we're using VMware
				return 1 * time.Minute // VMware boot of hermetic OS X
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
	if st.isSubrepo() || st.conf.NumTestHelpers(st.isTry()) == 0 || st.conf.IsReverse() {
		return
	}
	time.AfterFunc(st.expectedMakeBashDuration()-st.expectedBuildletStartDuration(),
		func() {
			st.logEventTime("starting_helpers")
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

// We should try to build from a snapshot if this is a subrepo build, we can
// expect there to be a snapshot (splitmakerun), and the snapshot exists.
func (st *buildStatus) useSnapshot() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.useSnapshotMemo != nil {
		return *st.useSnapshotMemo
	}
	b := st.conf.SplitMakeRun() && st.snapshotExists()
	st.useSnapshotMemo = &b
	return b
}

func (st *buildStatus) forceSnapshotUsage() {
	st.mu.Lock()
	defer st.mu.Unlock()
	truth := true
	st.useSnapshotMemo = &truth
}

func (st *buildStatus) shouldCrossCompileMake() bool {
	if inStaging {
		return st.name == "linux-arm" && kubeErr == nil
	}
	return st.isTry() && st.name == "linux-arm" && kubeErr == nil
}

func (st *buildStatus) build() error {
	putBuildRecord(st.buildRecord())

	sp := st.createSpan("checking_for_snapshot")
	if inStaging {
		err := storageClient.Bucket(buildEnv.SnapBucket).Object(st.snapshotObjectName()).Delete(context.Background())
		st.logEventTime("deleted_snapshot", fmt.Sprint(err))
	}
	snapshotExists := st.useSnapshot()
	if inStaging {
		st.logEventTime("use_snapshot", fmt.Sprint(snapshotExists))
	}
	sp.done(nil)

	if !snapshotExists && st.shouldCrossCompileMake() {
		if err := st.crossCompileMakeAndSnapshot(); err != nil {
			return err
		}
		st.forceSnapshotUsage()
	}

	sp = st.createSpan("get_buildlet")
	pool := st.buildletPool()
	bc, err := pool.GetBuildlet(st.ctx, st.conf.HostType, st)
	sp.done(err)
	if err != nil {
		return fmt.Errorf("failed to get a buildlet: %v", err)
	}
	defer bc.Close()
	st.mu.Lock()
	st.bc = bc
	st.mu.Unlock()

	st.logEventTime("using_buildlet", bc.IPPort())

	if st.useSnapshot() {
		sp := st.createSpan("write_snapshot_tar")
		if err := bc.PutTarFromURL(st.snapshotURL(), "go"); err != nil {
			return sp.done(fmt.Errorf("failed to put snapshot to buildlet: %v", err))
		}
		sp.done(nil)
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
	fmt.Fprintf(st, "%s at %v", st.name, st.rev)
	if st.isSubrepo() {
		fmt.Fprintf(st, " building %v at %v", st.subName, st.subRev)
	}
	fmt.Fprint(st, "\n\n")

	makeTest := st.createSpan("make_and_test") // warning: magic event named used by handleLogs

	var remoteErr error
	if st.conf.SplitMakeRun() {
		remoteErr, err = st.runAllSharded()
	} else {
		remoteErr, err = st.runAllLegacy()
	}
	makeTest.done(err)

	// bc (aka st.bc) may be invalid past this point, so let's
	// close it to make sure we we don't accidentally use it.
	bc.Close()

	doneMsg := "all tests passed"
	if remoteErr != nil {
		doneMsg = "with test failures"
	} else if err != nil {
		doneMsg = "comm error: " + err.Error()
	}
	if err != nil {
		// Return the error *before* we create the magic
		// "done" event. (which the try coordinator looks for)
		return err
	}
	st.logEventTime("done", doneMsg) // "done" is a magic value

	if devPause {
		st.logEventTime("DEV_MAIN_SLEEP")
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
		if err := recordResult(st.builderRev, remoteErr == nil, buildLog, time.Since(execStartTime)); err != nil {
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
		GoRev:     st.rev,
		Rev:       st.subRevOrGoRev(),
		Repo:      st.repoOrGo(),
		Builder:   st.name,
		OS:        st.conf.GOOS(),
		Arch:      st.conf.GOARCH(),
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
		GoRev:   st.rev,
		Rev:     st.subRevOrGoRev(),
		Repo:    st.repoOrGo(),
		Builder: st.name,
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

// runAllSharded runs make.bash and then shards the test execution.
// remoteErr and err are as described at the top of this file.
//
// After runAllSharded returns, the caller must assume that st.bc
// might be invalid (It's possible that only one of the helper
// buildlets survived).
func (st *buildStatus) runAllSharded() (remoteErr, err error) {
	st.getHelpersReadySoon()

	remoteErr, err = st.runMake()
	if err != nil {
		return nil, err
	}
	if remoteErr != nil {
		return fmt.Errorf("build failed: %v", remoteErr), nil
	}
	if st.conf.StopAfterMake {
		return nil, nil
	}

	if err := st.doSnapshot(st.bc); err != nil {
		return nil, err
	}

	if st.isSubrepo() {
		remoteErr, err = st.runSubrepoTests()
	} else {
		remoteErr, err = st.runTests(st.getHelpers())
	}
	if err != nil {
		return nil, fmt.Errorf("runTests: %v", err)
	}
	if remoteErr != nil {
		return fmt.Errorf("tests failed: %v", remoteErr), nil
	}
	return nil, nil
}

func (st *buildStatus) crossCompileMakeAndSnapshot() (err error) {
	// TODO: currently we ditch this buildlet when we're done with
	// the make.bash & snapshot. For extra speed later, we could
	// keep it around and use it to "go test -c" each stdlib
	// package's tests, and push the binary to each ARM helper
	// machine. That might be too little gain for the complexity,
	// though, or slower once we ship everything around.
	ctx, cancel := context.WithCancel(st.ctx)
	defer cancel()
	sp := st.createSpan("get_buildlet_cross")
	kubeBC, err := kubePool.GetBuildlet(ctx, "host-linux-armhf-cross", st)
	sp.done(err)
	if err != nil {
		return err
	}
	defer kubeBC.Close()

	if err := st.writeGoSourceTo(kubeBC); err != nil {
		return err
	}

	makeSpan := st.createSpan("make_cross_compile_kube")
	defer func() { makeSpan.done(err) }()

	goos, goarch := st.conf.GOOS(), st.conf.GOARCH()

	remoteErr, err := kubeBC.Exec("/bin/bash", buildlet.ExecOpts{
		SystemLevel: true,
		Args: []string{
			"-c",
			"cd $WORKDIR/go/src && " +
				"./make.bash && " +
				"cd .. && " +
				"mv bin/*_*/* bin && " +
				"rmdir bin/*_* && " +
				"rm -rf pkg/linux_amd64 pkg/tool/linux_amd64 pkg/bootstrap pkg/obj",
		},
		Output: st,
		ExtraEnv: []string{
			"GOROOT_BOOTSTRAP=/go1.4",
			"CGO_ENABLED=1",
			"CC_FOR_TARGET=arm-linux-gnueabihf-gcc",
			"GOOS=" + goos,
			"GOARCH=" + goarch,
			"GOARM=7", // harmless if GOARCH != "arm"
		},
		Debug: true,
	})
	if err != nil {
		return err
	}
	if remoteErr != nil {
		return fmt.Errorf("remote error: %v", remoteErr)
	}

	if err := st.doSnapshot(kubeBC); err != nil {
		return err
	}

	return nil
}

// runMake builds the tool chain.
// remoteErr and err are as described at the top of this file.
func (st *buildStatus) runMake() (remoteErr, err error) {
	// Don't do this if we're using a pre-built snapshot.
	if st.useSnapshot() {
		return nil, nil
	}

	// Build the source code.
	makeSpan := st.createSpan("make", st.conf.MakeScript())
	remoteErr, err = st.bc.Exec(path.Join("go", st.conf.MakeScript()), buildlet.ExecOpts{
		Output:   st,
		ExtraEnv: append(st.conf.Env(), "GOBIN="),
		Debug:    true,
		Args:     st.conf.MakeScriptArgs(),
	})
	if err != nil {
		makeSpan.done(err)
		return nil, err
	}
	if remoteErr != nil {
		makeSpan.done(remoteErr)
		return fmt.Errorf("make script failed: %v", remoteErr), nil
	}
	makeSpan.done(nil)

	// Need to run "go install -race std" before the snapshot + tests.
	if st.conf.IsRace() {
		sp := st.createSpan("install_race_std")
		remoteErr, err = st.bc.Exec("go/bin/go", buildlet.ExecOpts{
			Output:   st,
			ExtraEnv: append(st.conf.Env(), "GOBIN="),
			Debug:    true,
			Args:     []string{"install", "-race", "std"},
		})
		if err != nil {
			sp.done(err)
			return nil, err
		}
		if remoteErr != nil {
			sp.done(err)
			return fmt.Errorf("go install -race std failed: %v", remoteErr), nil
		}
		sp.done(nil)
	}

	return nil, nil
}

// runAllLegacy executes all.bash (or .bat, or whatever) in the traditional way.
// remoteErr and err are as described at the top of this file.
//
// TODO(bradfitz,adg): delete this function when all builders
// can split make & run (and then delete the SplitMakeRun method)
func (st *buildStatus) runAllLegacy() (remoteErr, err error) {
	allScript := st.conf.AllScript()
	sp := st.createSpan("legacy_all_path", allScript)
	remoteErr, err = st.bc.Exec(path.Join("go", allScript), buildlet.ExecOpts{
		Output:   st,
		ExtraEnv: st.conf.Env(),
		Debug:    true,
		Args:     st.conf.AllScriptArgs(),
	})
	if err != nil {
		sp.done(err)
		return nil, err
	}
	if remoteErr != nil {
		sp.done(err)
		return fmt.Errorf("all script failed: %v", remoteErr), nil
	}
	sp.done(nil)
	return nil, nil
}

func (st *buildStatus) doSnapshot(bc *buildlet.Client) error {
	// If we're using a pre-built snapshot, don't make another.
	if st.useSnapshot() {
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

var timeSnapshotCorruptionFixed = time.Date(2015, time.November, 1, 0, 0, 0, 0, time.UTC)

// TODO(adg): prune this map over time (might never be necessary, though)
var snapshotExistsCache = struct {
	sync.Mutex
	m map[builderRev]bool // set; only true values
}{m: map[builderRev]bool{}}

// snapshotExists reports whether the snapshot exists and isn't corrupt.
// Unfortunately we put some corrupt ones in for awhile, so this check is
// now more paranoid than it used to be.
func (br *builderRev) snapshotExists() (ok bool) {
	// If we already know this snapshot exists, don't check again.
	snapshotExistsCache.Lock()
	exists := snapshotExistsCache.m[*br]
	snapshotExistsCache.Unlock()
	if exists {
		return true
	}

	// When the function exits, cache an affirmative result.
	defer func() {
		if ok {
			snapshotExistsCache.Lock()
			snapshotExistsCache.m[*br] = true
			snapshotExistsCache.Unlock()
		}
	}()

	resp, err := http.Head(br.snapshotURL())
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}

	// If the snapshot is newer than the point at which we fixed writing
	// potentially-truncated snapshots to GCS, then stop right here.
	// See history in golang.org/issue/12671
	lastMod, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if err == nil && lastMod.After(timeSnapshotCorruptionFixed) {
		log.Printf("Snapshot exists for %v (without truncate checks)", br)
		return true
	}

	// Otherwise, if the snapshot is too old, verify it.
	// This path is slow.
	// TODO(bradfitz): delete this in 6 months or so? (around 2016-06-01)
	resp, err = http.Get(br.snapshotURL())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	// Verify the .tar.gz is valid.
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		log.Printf("corrupt snapshot? %s gzip.NewReader: %v", br.snapshotURL(), err)
		return false
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("corrupt snapshot? %s tar.Next: %v", br.snapshotURL(), err)
			return false
		}
		if _, err := io.Copy(ioutil.Discard, tr); err != nil {
			log.Printf("corrupt snapshot? %s reading contents of %s: %v", br.snapshotURL(), hdr.Name, err)
			return false
		}
	}
	return gz.Close() == nil
}

func (st *buildStatus) writeGoSource() error {
	return st.writeGoSourceTo(st.bc)
}

func (st *buildStatus) writeGoSourceTo(bc *buildlet.Client) error {
	// Write the VERSION file.
	sp := st.createSpan("write_version_tar")
	if err := bc.PutTar(versionTgz(st.rev), "go"); err != nil {
		return sp.done(fmt.Errorf("writing VERSION tgz: %v", err))
	}

	srcTar, err := getSourceTgz(st, "go", st.rev, st.isTry())
	if err != nil {
		return err
	}
	sp = st.createSpan("write_go_src_tar")
	if err := bc.PutTar(srcTar, "go"); err != nil {
		return sp.done(fmt.Errorf("writing tarball from Gerrit: %v", err))
	}
	return sp.done(nil)
}

func (st *buildStatus) writeBootstrapToolchain() error {
	u := st.conf.GoBootstrapURL(buildEnv)
	if u == "" {
		return nil
	}
	const bootstrapDir = "go1.4" // might be newer; name is the default
	sp := st.createSpan("write_go_bootstrap_tar")
	return sp.done(st.bc.PutTarFromURL(u, bootstrapDir))
}

func (st *buildStatus) cleanForSnapshot(bc *buildlet.Client) error {
	sp := st.createSpan("clean_for_snapshot")
	return sp.done(bc.RemoveAll(
		"go/doc/gopher",
		"go/pkg/bootstrap",
	))
}

// snapshotObjectName is the cloud storage object name of the
// built Go tree for this builder and Go rev (not the sub-repo).
// The entries inside this tarball do not begin with "go/".
func (br *builderRev) snapshotObjectName() string {
	return fmt.Sprintf("%v/%v/%v.tar.gz", "go", br.name, br.rev)
}

// snapshotURL is the absolute URL of the snapshot object (see above).
func (br *builderRev) snapshotURL() string {
	return buildEnv.SnapshotURL(br.name, br.rev)
}

func (st *buildStatus) writeSnapshot(bc *buildlet.Client) (err error) {
	sp := st.createSpan("write_snapshot_to_gcs")
	defer func() { sp.done(err) }()
	// This should happen in 15 seconds or so, but I saw timeouts
	// a couple times at 1 minute. Some buildlets might be far
	// away on the network, so be more lenient. The timeout mostly
	// is here to prevent infinite hangs.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tsp := st.createSpan("fetch_snapshot_reader_from_buildlet")
	tgz, err := bc.GetTar(ctx, "go")
	tsp.done(err)
	if err != nil {
		return err
	}
	defer tgz.Close()

	wr := storageClient.Bucket(buildEnv.SnapBucket).Object(st.snapshotObjectName()).NewWriter(ctx)
	wr.ContentType = "application/octet-stream"
	wr.ACL = append(wr.ACL, storage.ACLRule{Entity: storage.AllUsers, Role: storage.RoleReader})
	if _, err := io.Copy(wr, tgz); err != nil {
		st.logf("failed to write snapshot to GCS: %v", err)
		wr.CloseWithError(err)
		return err
	}

	return wr.Close()
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
		OnStartExec: func() { st.logEventTime("discovering_tests") },
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
		if st.shouldSkipTest(test) {
			continue
		}
		names = append(names, test)
	}
	return names, nil, nil
}

// shouldSkipTest reports whether this test should be skipped.  We
// only do this for slow builders running redundant tests. (That is,
// tests which have identical behavior across different ports)
func (st *buildStatus) shouldSkipTest(testName string) bool {
	if inStaging && st.name == "linux-arm" && false {
		if strings.HasPrefix(testName, "go_test:") && testName < "go_test:runtime" {
			return true
		}
	}
	switch testName {
	case "vet/all":
		// Old vetall test name, before the sharding in CL 37572.
		return true
	case "api":
		return st.isTry() && st.name != "linux-amd64"
	}
	return false
}

func (st *buildStatus) newTestSet(names []string) *testSet {
	set := &testSet{
		st: st,
	}
	for _, name := range names {
		set.items = append(set.items, &testItem{
			set:      set,
			name:     name,
			duration: testDuration(name),
			take:     make(chan token, 1),
			done:     make(chan token),
		})
	}
	return set
}

func partitionGoTests(tests []string) (sets [][]string) {
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

	flush := func() {
		if len(curSet) > 0 {
			sets = append(sets, curSet)
			curSet = nil
			curDur = 0
		}
	}
	for _, name := range goTests {
		d := testDuration(name) - minGoTestSpeed // subtract 'go' tool overhead
		if curDur+d > sizeThres {
			flush() // no-op if empty
		}
		curSet = append(curSet, name)
		curDur += d
	}

	flush()
	return
}

var minGoTestSpeed = (func() time.Duration {
	var min Seconds
	for name, secs := range fixedTestDuration {
		if !strings.HasPrefix(name, "go_test:") {
			continue
		}
		if min == 0 || secs < min {
			min = secs
		}
	}
	return min.Duration()
})()

type Seconds float64

func (s Seconds) Duration() time.Duration {
	return time.Duration(float64(s) * float64(time.Second))
}

// in seconds on Linux/amd64 (once on 2015-05-28), each
// by themselves. There seems to be a 0.6s+ overhead
// from the go tool which goes away if they're combined.
var fixedTestDuration = map[string]Seconds{
	"go_test:archive/tar":                    1.30,
	"go_test:archive/zip":                    1.68,
	"go_test:bufio":                          1.61,
	"go_test:bytes":                          1.50,
	"go_test:compress/bzip2":                 0.82,
	"go_test:compress/flate":                 1.73,
	"go_test:compress/gzip":                  0.82,
	"go_test:compress/lzw":                   0.86,
	"go_test:compress/zlib":                  1.78,
	"go_test:container/heap":                 0.69,
	"go_test:container/list":                 0.72,
	"go_test:container/ring":                 0.64,
	"go_test:crypto/aes":                     0.79,
	"go_test:crypto/cipher":                  0.96,
	"go_test:crypto/des":                     0.96,
	"go_test:crypto/dsa":                     0.75,
	"go_test:crypto/ecdsa":                   0.86,
	"go_test:crypto/elliptic":                1.06,
	"go_test:crypto/hmac":                    0.67,
	"go_test:crypto/md5":                     0.77,
	"go_test:crypto/rand":                    0.89,
	"go_test:crypto/rc4":                     0.71,
	"go_test:crypto/rsa":                     1.17,
	"go_test:crypto/sha1":                    0.75,
	"go_test:crypto/sha256":                  0.68,
	"go_test:crypto/sha512":                  0.67,
	"go_test:crypto/subtle":                  0.56,
	"go_test:crypto/tls":                     3.29,
	"go_test:crypto/x509":                    2.81,
	"go_test:database/sql":                   1.75,
	"go_test:database/sql/driver":            0.64,
	"go_test:debug/dwarf":                    0.77,
	"go_test:debug/elf":                      1.41,
	"go_test:debug/gosym":                    1.45,
	"go_test:debug/macho":                    0.97,
	"go_test:debug/pe":                       0.79,
	"go_test:debug/plan9obj":                 0.73,
	"go_test:encoding/ascii85":               0.64,
	"go_test:encoding/asn1":                  1.16,
	"go_test:encoding/base32":                0.79,
	"go_test:encoding/base64":                0.82,
	"go_test:encoding/binary":                0.96,
	"go_test:encoding/csv":                   0.67,
	"go_test:encoding/gob":                   2.70,
	"go_test:encoding/hex":                   0.66,
	"go_test:encoding/json":                  2.20,
	"test:errors":                            0.54,
	"go_test:expvar":                         1.36,
	"go_test:flag":                           0.92,
	"go_test:fmt":                            2.02,
	"go_test:go/ast":                         1.44,
	"go_test:go/build":                       1.42,
	"go_test:go/constant":                    0.92,
	"go_test:go/doc":                         1.51,
	"go_test:go/format":                      0.73,
	"go_test:go/internal/gcimporter":         1.30,
	"go_test:go/parser":                      1.30,
	"go_test:go/printer":                     1.61,
	"go_test:go/scanner":                     0.89,
	"go_test:go/token":                       0.92,
	"go_test:go/types":                       5.24,
	"go_test:hash/adler32":                   0.62,
	"go_test:hash/crc32":                     0.68,
	"go_test:hash/crc64":                     0.55,
	"go_test:hash/fnv":                       0.66,
	"go_test:html":                           0.74,
	"go_test:html/template":                  1.93,
	"go_test:image":                          1.42,
	"go_test:image/color":                    0.77,
	"go_test:image/draw":                     1.32,
	"go_test:image/gif":                      1.15,
	"go_test:image/jpeg":                     1.32,
	"go_test:image/png":                      1.23,
	"go_test:index/suffixarray":              0.79,
	"go_test:internal/singleflight":          0.66,
	"go_test:io":                             0.97,
	"go_test:io/ioutil":                      0.73,
	"go_test:log":                            0.72,
	"go_test:log/syslog":                     2.93,
	"go_test:math":                           1.59,
	"go_test:math/big":                       3.75,
	"go_test:math/cmplx":                     0.81,
	"go_test:math/rand":                      1.15,
	"go_test:mime":                           1.01,
	"go_test:mime/multipart":                 1.51,
	"go_test:mime/quotedprintable":           0.95,
	"go_test:net":                            6.71,
	"go_test:net/http":                       9.41,
	"go_test:net/http/cgi":                   2.00,
	"go_test:net/http/cookiejar":             1.51,
	"go_test:net/http/fcgi":                  1.43,
	"go_test:net/http/httptest":              1.36,
	"go_test:net/http/httputil":              1.54,
	"go_test:net/http/internal":              0.68,
	"go_test:net/internal/socktest":          0.58,
	"go_test:net/mail":                       0.92,
	"go_test:net/rpc":                        1.95,
	"go_test:net/rpc/jsonrpc":                1.50,
	"go_test:net/smtp":                       1.43,
	"go_test:net/textproto":                  1.01,
	"go_test:net/url":                        1.45,
	"go_test:os":                             1.88,
	"go_test:os/exec":                        2.13,
	"go_test:os/signal":                      4.22,
	"go_test:os/user":                        0.93,
	"go_test:path":                           0.68,
	"go_test:path/filepath":                  1.14,
	"go_test:reflect":                        3.42,
	"go_test:regexp":                         1.65,
	"go_test:regexp/syntax":                  1.40,
	"go_test:runtime":                        21.02,
	"go_test:runtime/debug":                  0.79,
	"go_test:runtime/pprof":                  8.01,
	"go_test:sort":                           0.96,
	"go_test:strconv":                        1.60,
	"go_test:strings":                        1.51,
	"go_test:sync":                           1.05,
	"go_test:sync/atomic":                    1.13,
	"go_test:syscall":                        1.69,
	"go_test:testing":                        3.70,
	"go_test:testing/quick":                  0.74,
	"go_test:text/scanner":                   0.79,
	"go_test:text/tabwriter":                 0.71,
	"go_test:text/template":                  1.65,
	"go_test:text/template/parse":            1.25,
	"go_test:time":                           4.20,
	"go_test:unicode":                        0.68,
	"go_test:unicode/utf16":                  0.77,
	"go_test:unicode/utf8":                   0.71,
	"go_test:cmd/addr2line":                  1.73,
	"go_test:cmd/api":                        1.33,
	"go_test:cmd/asm/internal/asm":           1.24,
	"go_test:cmd/asm/internal/lex":           0.91,
	"go_test:cmd/compile/internal/big":       5.26,
	"go_test:cmd/cover":                      3.32,
	"go_test:cmd/fix":                        1.26,
	"go_test:cmd/go":                         36,
	"go_test:cmd/gofmt":                      1.06,
	"go_test:cmd/internal/goobj":             0.65,
	"go_test:cmd/internal/obj":               1.16,
	"go_test:cmd/internal/obj/x86":           1.04,
	"go_test:cmd/internal/rsc.io/arm/armasm": 1.92,
	"go_test:cmd/internal/rsc.io/x86/x86asm": 2.22,
	"go_test:cmd/newlink":                    1.48,
	"go_test:cmd/nm":                         1.84,
	"go_test:cmd/objdump":                    3.60,
	"go_test:cmd/pack":                       2.64,
	"go_test:cmd/pprof/internal/profile":     1.29,
	"go_test:cmd/compile/internal/gc":        18,
	"gp_test:cmd/compile/internal/ssa":       8,
	"runtime:cpu124":                         44.78,
	"sync_cpu":                               1.01,
	"cgo_stdio":                              1.53,
	"cgo_life":                               1.56,
	"cgo_test":                               45.60,
	"race":                                   42.55,
	"testgodefs":                             2.37,
	"testso":                                 2.72,
	"testcarchive":                           11.11,
	"testcshared":                            15.80,
	"testshared":                             7.13,
	"testasan":                               2.56,
	"cgo_errors":                             7.03,
	"testsigfwd":                             2.74,
	"doc_progs":                              5.38,
	"wiki":                                   3.56,
	"shootout":                               11.34,
	"bench_go1":                              3.72,
	"test:0_5":                               10,
	"test:1_5":                               10,
	"test:2_5":                               10,
	"test:3_5":                               10,
	"test:4_5":                               10,
	"codewalk":                               2.42,
	"api":                                    7.38,
	"go_test_bench:compress/bzip2":    3.059513602,
	"go_test_bench:image/jpeg":        3.143345345,
	"go_test_bench:encoding/hex":      3.182452293,
	"go_test_bench:expvar":            3.490162906,
	"go_test_bench:crypto/cipher":     3.609317114,
	"go_test_bench:compress/lzw":      3.628982201,
	"go_test_bench:database/sql":      3.693163398,
	"go_test_bench:math/rand":         3.807438591,
	"go_test_bench:bufio":             3.882166683,
	"go_test_bench:context":           4.038173785,
	"go_test_bench:hash/crc32":        4.107135055,
	"go_test_bench:unicode/utf8":      4.205641826,
	"go_test_bench:regexp/syntax":     4.587359311,
	"go_test_bench:sort":              4.660599666,
	"go_test_bench:math/cmplx":        5.311264213,
	"go_test_bench:encoding/gob":      5.326788419,
	"go_test_bench:reflect":           5.777081055,
	"go_test_bench:image/png":         6.12439885,
	"go_test_bench:html/template":     6.765132418,
	"go_test_bench:fmt":               7.476528843,
	"go_test_bench:sync":              7.526458261,
	"go_test_bench:archive/zip":       7.782424696,
	"go_test_bench:regexp":            8.428459563,
	"go_test_bench:image/draw":        8.666510786,
	"go_test_bench:strings":           10.836201759,
	"go_test_bench:time":              10.952476479,
	"go_test_bench:image/gif":         11.373276098,
	"go_test_bench:encoding/json":     11.547950173,
	"go_test_bench:crypto/tls":        11.548834754,
	"go_test_bench:strconv":           12.819669296,
	"go_test_bench:math":              13.7889302,
	"go_test_bench:net":               14.845086695,
	"go_test_bench:net/http":          15.288519219,
	"go_test_bench:bytes":             15.809308703,
	"go_test_bench:index/suffixarray": 23.69239388,
	"go_test_bench:compress/flate":    26.906228664,
	"go_test_bench:math/big":          28.82127674,
}

// testDuration predicts how long the dist test 'name' will take 'name' will take.
// It's only a scheduling guess.
func testDuration(name string) time.Duration {
	if secs, ok := fixedTestDuration[name]; ok {
		return secs.Duration()
	}
	return minGoTestSpeed * 2
}

func (st *buildStatus) runSubrepoTests() (remoteErr, err error) {
	st.logEventTime("fetching_subrepo", st.subName)

	workDir, err := st.bc.WorkDir()
	if err != nil {
		err = fmt.Errorf("error discovering workdir for helper %s: %v", st.bc.IPPort(), err)
		return nil, err
	}
	goroot := st.conf.FilePathJoin(workDir, "go")
	gopath := st.conf.FilePathJoin(workDir, "gopath")

	fetched := map[string]bool{}
	toFetch := []string{st.subName}

	// fetch checks out the provided sub-repo to the buildlet's workspace.
	fetch := func(repo, rev string) error {
		fetched[repo] = true
		isTry := st.trySet != nil && repo == st.subName // i.e. hit Gerrit directly
		tgz, err := getSourceTgz(st, repo, rev, isTry)
		if err != nil {
			return err
		}
		return st.bc.PutTar(tgz, "gopath/src/"+subrepoPrefix+repo)
	}

	// findDeps uses 'go list' on the checked out repo to find its
	// dependencies, and adds any not-yet-fetched deps to toFetched.
	findDeps := func(repo string) (rErr, err error) {
		repoPath := subrepoPrefix + repo
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
			if !strings.HasPrefix(p, subrepoPrefix) || strings.HasPrefix(p, repoPath) {
				continue
			}
			repo = strings.TrimPrefix(p, subrepoPrefix)
			if i := strings.Index(repo, "/"); i >= 0 {
				repo = repo[:i]
			}
			if !fetched[repo] {
				toFetch = append(toFetch, repo)
			}
		}
		return nil, nil
	}

	// Recursively fetch the repo and its dependencies.
	// Dependencies are always fetched at master, which isn't
	// great but the dashboard data model doesn't track
	// sub-repo dependencies. TODO(adg): fix this somehow??
	for i := 0; i < len(toFetch); i++ {
		repo := toFetch[i]
		if fetched[repo] {
			continue
		}
		// Fetch the HEAD revision by default.
		rev := getRepoHead(repo)
		if rev == "" {
			rev = "master" // should happen rarely; ok if it does.
		}
		// For the repo under test, choose that specific revision.
		if i == 0 {
			rev = st.subRev
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

	sp := st.createSpan("running_subrepo_tests", st.subName)
	defer func() { sp.done(err) }()
	return st.bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
		Output: st,
		// TODO(adg): remove vendor experiment variable after Go 1.6
		ExtraEnv: append(st.conf.Env(),
			"GOROOT="+goroot,
			"GOPATH="+gopath,
			"GO15VENDOREXPERIMENT=1"),
		Path: []string{"$WORKDIR/go/bin", "$PATH"},
		Args: []string{"test", "-short", subrepoPrefix + st.subName + "/..."},
	})
}

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
	set := st.newTestSet(testNames)
	st.logEventTime("starting_tests", fmt.Sprintf("%d tests", len(set.items)))
	startTime := time.Now()

	workDir, err := st.bc.WorkDir()
	if err != nil {
		return nil, fmt.Errorf("error discovering workdir for main buildlet, %s: %v", st.bc.Name(), err)
	}
	mainBuildletGoroot := st.conf.FilePathJoin(workDir, "go")

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
			st.runTestsOnBuildlet(st.bc, tis, mainBuildletGoroot)
		}
		st.logEventTime("main_buildlet_broken", st.bc.Name())
	}()
	go func() {
		defer buildletActivity.Done() // for the per-goroutine Add(2) above
		for helper := range helpers {
			buildletActivity.Add(1)
			go func(bc *buildlet.Client) {
				defer buildletActivity.Done() // for the per-helper Add(1) above
				defer st.logEventTime("closed_helper", bc.Name())
				defer bc.Close()
				if devPause {
					defer time.Sleep(5 * time.Minute)
					defer st.logEventTime("DEV_HELPER_SLEEP", bc.Name())
				}
				st.logEventTime("got_empty_test_helper", bc.String())
				if err := bc.PutTarFromURL(st.snapshotURL(), "go"); err != nil {
					log.Printf("failed to extract snapshot for helper %s: %v", bc.Name(), err)
					return
				}
				workDir, err := bc.WorkDir()
				if err != nil {
					log.Printf("error discovering workdir for helper %s: %v", bc.Name(), err)
					return
				}
				st.logEventTime("test_helper_set_up", bc.Name())
				goroot := st.conf.FilePathJoin(workDir, "go")
				for !bc.IsBroken() {
					tis, ok := set.testsToRunBiggestFirst()
					if !ok {
						st.logEventTime("no_new_tests_remain", bc.Name())
						return
					}
					st.runTestsOnBuildlet(bc, tis, goroot)
				}
				st.logEventTime("test_helper_is_broken", bc.Name())
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
				st.logEventTime("still_waiting_on_test", ti.name)
			case <-buildletsGone:
				set.cancelAll()
				return nil, fmt.Errorf("dist test failed: all buildlets had network errors or timeouts, yet tests remain")
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
	st.logEventTime("tests_complete", msg)
	fmt.Fprintf(st, "\nAll tests passed.\n")
	return nil, nil
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

func execTimeout(testNames []string) time.Duration {
	// TODO(bradfitz): something smarter probably.
	return 20 * time.Minute
}

// runTestsOnBuildlet runs tis on bc, using the optional goroot environment variable.
func (st *buildStatus) runTestsOnBuildlet(bc *buildlet.Client, tis []*testItem, goroot string) {
	names := make([]string, len(tis))
	for i, ti := range tis {
		names[i] = ti.name
		if i > 0 && !strings.HasPrefix(ti.name, "go_test:") {
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
	sp := st.createSpan(spanName, detail)

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
	timeout := execTimeout(names)
	remoteErr, err := bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
		// We set Dir to "." instead of the default ("go/bin") so when the dist tests
		// try to run os/exec.Command("go", "test", ...), the LookPath of "go" doesn't
		// return "./go.exe" (which exists in the current directory: "go/bin") and then
		// fail when dist tries to run the binary in dir "$GOROOT/src", since
		// "$GOROOT/src" + "./go.exe" doesn't exist. Perhaps LookPath should return
		// an absolute path.
		Dir:      ".",
		Output:   &buf, // see "maybe stream lines" TODO below
		ExtraEnv: append(st.conf.Env(), "GOROOT="+goroot),
		Timeout:  timeout,
		Path:     []string{"$WORKDIR/go/bin", "$PATH"},
		Args:     args,
	})
	execDuration := time.Since(t0)
	sp.done(err)
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
	st    *buildStatus
	items []*testItem

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
	stdSets := partitionGoTests(names)
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
	builderRev
	buildID   string // "B" + 9 random hex
	conf      dashboard.BuildConfig
	startTime time.Time // actually time of newBuild (~same thing); TODO(bradfitz): rename this createTime
	trySet    *trySet   // or nil

	onceInitHelpers sync.Once // guards call of onceInitHelpersFunc
	helpers         <-chan *buildlet.Client
	ctx             context.Context    // used to start the build
	cancel          context.CancelFunc // used to cancel context; for use by setDone only

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
	log.Printf("[build %s %s]: %s", st.name, st.rev, fmt.Sprintf(format, args...))
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
	el.logEventTime(event, opt)
	return &span{
		el:      el,
		event:   event,
		start:   start,
		optText: opt,
	}
}

// done ends a span.
// It is legal to call done multiple times. Only the first call
// logs.
// done always returns its input argument.
func (s *span) done(err error) error {
	if !s.end.IsZero() {
		return err
	}
	t1 := time.Now()
	s.end = t1
	td := t1.Sub(s.start)
	var text bytes.Buffer
	fmt.Fprintf(&text, "after %v", td)
	if err != nil {
		fmt.Fprintf(&text, "; err=%v", err)
	}
	if s.optText != "" {
		fmt.Fprintf(&text, "; %v", s.optText)
	}
	if st, ok := s.el.(*buildStatus); ok {
		putSpanRecord(st.spanRecord(s, err))
	}
	s.el.logEventTime("finish_"+s.event, text.String())
	return err
}

func (st *buildStatus) createSpan(event string, optText ...string) eventSpan {
	return createSpan(st, event, optText...)
}

func (st *buildStatus) logEventTime(event string, optText ...string) {
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
	st.mu.Lock()
	defer st.mu.Unlock()

	urlPrefix := "https://go-review.googlesource.com/#/q/"
	if strings.Contains(st.name, "gccgo") {
		urlPrefix = "https://code.google.com/p/gofrontend/source/detail?r="
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<a href='https://github.com/golang/go/wiki/DashboardBuilders'>%s</a> rev <a href='%s%s'>%s</a>",
		st.name, urlPrefix, st.rev, st.rev[:8])
	if st.isSubrepo() {
		fmt.Fprintf(&buf, " (sub-repo %s rev <a href='%s%s'>%s</a>)",
			st.subName, urlPrefix, st.subRev, st.subRev[:8])
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
		fmt.Fprintf(&buf, "; <a href='%s'>%s</a>; %s", st.logsURLLocked(), state, html.EscapeString(st.bc.String()))
	} else {
		fmt.Fprintf(&buf, "; <a href='%s'>%s</a>", st.logsURLLocked(), state)
	}

	t := st.done
	if t.IsZero() {
		t = st.startTime
	}
	fmt.Fprintf(&buf, ", %v ago", time.Since(t))
	if full {
		buf.WriteByte('\n')
		st.writeEventsLocked(&buf, true)
	}
	return template.HTML(buf.String())
}

func (st *buildStatus) logsURLLocked() string {
	var urlPrefix string
	if buildEnv == buildenv.Production {
		urlPrefix = "http://farmer.golang.org"
	} else {
		urlPrefix = "http://" + buildEnv.StaticIP
	}
	if *mode == "dev" {
		urlPrefix = "https://localhost:8119"
	}
	u := fmt.Sprintf("%v/temporarylogs?name=%s&rev=%s&st=%p", urlPrefix, st.name, st.rev, st)
	if st.isSubrepo() {
		u += fmt.Sprintf("&subName=%v&subRev=%v", st.subName, st.subRev)
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
				e = fmt.Sprintf("<a href='%s'>%s</a>", st.logsURLLocked(), e)
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

func versionTgz(rev string) io.Reader {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	// Writing to a bytes.Buffer should never fail, so check
	// errors with an explosion:
	check := func(err error) {
		if err != nil {
			panic("previously assumed to never fail: " + err.Error())
		}
	}

	contents := fmt.Sprintf("devel " + rev)
	check(tw.WriteHeader(&tar.Header{
		Name: "VERSION",
		Mode: 0644,
		Size: int64(len(contents)),
	}))
	_, err := io.WriteString(tw, contents)
	check(err)
	check(tw.Close())
	check(zw.Close())
	return bytes.NewReader(buf.Bytes())
}

var sourceGroup singleflight.Group

var sourceCache = lru.New(40) // git rev -> []byte

func useWatcher() bool {
	if inStaging {
		// Adjust as needed, depending on what you're testing.
		return false
	}
	return *mode != "dev"
}

// repo is go.googlesource.com repo ("go", "net", etc)
// rev is git revision.
func getSourceTgz(sl spanLogger, repo, rev string, isTry bool) (tgz io.Reader, err error) {
	sp := sl.createSpan("get_source")
	defer func() { sp.done(err) }()

	key := fmt.Sprintf("%v-%v", repo, rev)
	vi, err, _ := sourceGroup.Do(key, func() (interface{}, error) {
		if tgzBytes, ok := sourceCache.Get(key); ok {
			return tgzBytes, nil
		}

		if useWatcher() {
			sp := sl.createSpan("get_source_from_gitmirror")
			tgzBytes, err := getSourceTgzFromGitMirror(repo, rev)
			if err == nil {
				sourceCache.Add(key, tgzBytes)
				sp.done(nil)
				return tgzBytes, nil
			}
			log.Printf("Error fetching source %s/%s from watcher (after %v uptime): %v",
				repo, rev, time.Since(processStartTime), err)
			sp.done(errors.New("timeout"))
		}

		sp := sl.createSpan("get_source_from_gerrit", fmt.Sprintf("%v from gerrit", key))
		tgzBytes, err := getSourceTgzFromGerrit(repo, rev)
		sp.done(err)
		if err == nil {
			sourceCache.Add(key, tgzBytes)
		}
		return tgzBytes, err
	})
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(vi.([]byte)), nil
}

var gitMirrorClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return goKubeClient.DialServicePort(ctx, "gitmirror", "")
		},
	},
}

var gerritHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

func getSourceTgzFromGerrit(repo, rev string) (tgz []byte, err error) {
	return getSourceTgzFromURL(gerritHTTPClient, "gerrit", repo, rev, "https://go.googlesource.com/"+repo+"/+archive/"+rev+".tar.gz")
}

func getSourceTgzFromGitMirror(repo, rev string) (tgz []byte, err error) {
	for i := 0; i < 2; i++ { // two tries; different pods maybe?
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		// The "gitmirror" hostname is unused:
		tgz, err = getSourceTgzFromURL(gitMirrorClient, "gitmirror", repo, rev, "http://gitmirror/"+repo+".tar.gz?rev="+rev)
		if err == nil {
			return tgz, nil
		}
		if tr, ok := http.DefaultTransport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	}
	return nil, err
}

func getSourceTgzFromURL(hc *http.Client, source, repo, rev, urlStr string) (tgz []byte, err error) {
	res, err := hc.Get(urlStr)
	if err != nil {
		return nil, fmt.Errorf("fetching %s/%s from %s: %v", repo, rev, source, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		slurp, _ := ioutil.ReadAll(io.LimitReader(res.Body, 4<<10))
		return nil, fmt.Errorf("fetching %s/%s from %s: %v; body: %s", repo, rev, source, res.Status, slurp)
	}
	// TODO(bradfitz): finish golang.org/issue/11224
	const maxSize = 50 << 20 // talks repo is over 25MB; go source is 7.8MB on 2015-06-15
	slurp, err := ioutil.ReadAll(io.LimitReader(res.Body, maxSize+1))
	if len(slurp) > maxSize && err == nil {
		err = fmt.Errorf("body over %d bytes", maxSize)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s/%s from %s: %v", repo, rev, source, err)
	}
	return slurp, nil
}

var nl = []byte("\n")

// repoHead contains the hashes of the latest master HEAD
// for each sub-repo. It is populated by findWork.
var repoHead = struct {
	sync.Mutex
	m map[string]string // [repo]hash (["go"]"d3adb33f")
}{m: map[string]string{}}

// getRepoHead returns the commit hash of the latest master HEAD
// for the given repo ("go", "tools", "sys", etc).
func getRepoHead(repo string) string {
	repoHead.Lock()
	defer repoHead.Unlock()
	return repoHead.m[repo]
}

// getRepoHead sets the commit hash of the latest master HEAD
// for the given repo ("go", "tools", "sys", etc).
func setRepoHead(repo, head string) {
	repoHead.Lock()
	defer repoHead.Unlock()
	repoHead.m[repo] = head
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

func skipBranchForBuilder(repo, branch, builder string) bool {
	if strings.HasPrefix(builder, "darwin-") {
		switch builder {
		case "darwin-amd64-10_8", "darwin-amd64-10_10", "darwin-amd64-10_11",
			"darwin-386-10_8", "darwin-386-10_10", "darwin-386-10_11":
			// OS X before Sierra can build any branch.
			// (We've never had a 10.9 builder.)
		default:
			// Sierra or after, however, requires the 1.7 branch:
			switch branch {
			case "release-branch.go1.6",
				"release-branch.go1.5",
				"release-branch.go1.4",
				"release-branch.go1.3",
				"release-branch.go1.2",
				"release-branch.go1.1",
				"release-branch.go1":
				return true
			}
		}
	}
	return false
}
