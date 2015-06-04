// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The coordinator runs on GCE and coordinates builds in Docker containers.
package main // import "golang.org/x/build/cmd/coordinator"

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"os"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"camlistore.org/pkg/syncutil"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/lru"
	"golang.org/x/build/internal/singleflight"
	"golang.org/x/build/types"
	"google.golang.org/cloud/storage"
)

const subrepoPrefix = "golang.org/x/"

var processStartTime = time.Now()

var Version string // set by linker -X

// devPause is a debug option to pause for 5 minutes after the build
// finishes before destroying buildlets.
const devPause = false

func init() {
	// Disabled until we have test sharding. This takes 85+ minutes.
	// Test sharding is https://github.com/golang/go/issues/10029
	delete(dashboard.Builders, "linux-arm-qemu")
}

var (
	masterKeyFile = flag.String("masterkey", "", "Path to builder master key. Else fetched using GCE project attribute 'builder-master-key'.")

	// TODO(bradfitz): remove this list and just query it from the compute API:
	// http://godoc.org/google.golang.org/api/compute/v1#RegionsService.Get
	// and Region.Zones: http://godoc.org/google.golang.org/api/compute/v1#Region
	cleanZones = flag.String("zones", "us-central1-a,us-central1-b,us-central1-f", "Comma-separated list of zones to periodically clean of stale build VMs (ones that failed to shut themselves down)")

	mode = flag.String("mode", "", "valid modes are 'dev', 'prod', or '' for auto-detect")
)

func buildLogBucket() string {
	return devPrefix() + "go-build-log"
}

func snapBucket() string {
	return devPrefix() + "go-build-snap"
}

// LOCK ORDER:
//   statusMu, buildStatus.mu, trySet.mu

var (
	startTime = time.Now()

	statusMu   sync.Mutex // guards the following four structures; see LOCK ORDER comment above
	status     = map[builderRev]*buildStatus{}
	statusDone []*buildStatus         // finished recently, capped to maxStatusDone
	tries      = map[tryKey]*trySet{} // trybot builds
	tryList    []tryKey

	// subrepoHead contains the hashes of the latest master HEAD
	// for each sub-repo. It is populated by findWork.
	subrepoHead = struct {
		sync.Mutex
		m map[string]string // [repo]hash
	}{m: map[string]string{}}
)

// tryBuilders must be VMs. The Docker container builds are going away.
var tryBuilders []dashboard.BuildConfig

func init() {
	tryList := []string{
		"misc-compile",
		"darwin-amd64-10_10",
		"linux-386",
		"linux-amd64",
		"linux-amd64-race",
		"freebsd-386-gce101",
		"freebsd-amd64-gce101",
		"windows-386-gce",
		"windows-amd64-gce",
		"openbsd-386-gce56",
		"openbsd-amd64-gce56",
		"plan9-386",
		"nacl-386",
		"nacl-amd64p32",
		"linux-arm-shard_test",
		"linux-arm-shard_std_am",
		"linux-arm-shard_std_nz",
		"linux-arm-shard_runtimecpu",
		"linux-arm-shard_cgotest",
		"linux-arm-shard_misc",
	}
	for _, bname := range tryList {
		conf, ok := dashboard.Builders[bname]
		if ok {
			tryBuilders = append(tryBuilders, conf)
		} else {
			log.Printf("ignoring invalid try builder config %q", bname)
		}
	}
}

const (
	maxStatusDone = 30

	// vmDeleteTimeout is how long before we delete a VM.
	// In practice this need only be as long as the slowest
	// builder (plan9 currently), because on startup this program
	// already deletes all buildlets it doesn't know about
	// (i.e. ones from a previous instance of the coordinator).
	vmDeleteTimeout = 45 * time.Minute
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

	r, err := storage.NewReader(serviceCtx, devPrefix()+"go-builder-data", name)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ioutil.ReadAll(r)
}

// Fake keys signed by a fake CA.
var testFiles = map[string]string{
	"farmer-cert.pem": `-----BEGIN CERTIFICATE-----
MIICljCCAX4CCQCoS+/smvkG2TANBgkqhkiG9w0BAQUFADANMQswCQYDVQQDEwJn
bzAeFw0xNTA0MDYwMzE3NDJaFw0xNzA0MDUwMzE3NDJaMA0xCzAJBgNVBAMTAmdv
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA1NMaVxX8RfCMtQB18azV
hL6/U7C8W2G+8WXYeFuOpgP2SHnMbsUeTiUYWS1xqAxUh3Vl/TT1HIASRDL7kBis
yj+drspafnCr4Yp9oJx1xlIhVXGD/SyHk5oewkjkNEmrFtUT07mT2lmZqD3XJ+6V
aQslRxhPEkLGsXIA/hCucPIplI9jgLY8TmOBhQ7RzXAnk/ayAzDkCgkWB4k/zaFy
LiHjEkE7O7PIjjY51btCLep9QSts98zojY5oYNj2RdQOZa56MHAlh9hbdpm+P1vp
2QBpsDbVpHYv2VPCPvkdOGU1/nzumsxHy17DcirKP8Tuf6zMf9obeuSlMvUUPptl
hwIDAQABMA0GCSqGSIb3DQEBBQUAA4IBAQBxvUMKsX+DEhZSmc164IuSVJ9ucZ97
+KWn4nCwnVkI/RrsJpiTj3pZNRkAxq2vmZTpUdU0CgGHdZNXp/6s/GX4cSzFphSf
WZQN0CG/O50SQ39m7fz/dZ2Xse6EH2grr6KN0QsDhK/RVxecQv57rY9nLFHnC60t
vJBDC739lWlnsGDxylJNxEk2l5c2rJdn82yGw2G9pQ/LDVAtO1G2rxGkpi4FcpGk
rNAa6MiwcyFHcAr3OsigLm4Q9bCS6YXfQDvCZGAR91ADXVWDFC1sgBgM3U3+1bGp
tgXUVKymUvoVq0BiY4BCCYDluoErgZDytLmnUOxrykYi532VpRbbK2ja
-----END CERTIFICATE-----`,
	"farmer-key.pem": `-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEA1NMaVxX8RfCMtQB18azVhL6/U7C8W2G+8WXYeFuOpgP2SHnM
bsUeTiUYWS1xqAxUh3Vl/TT1HIASRDL7kBisyj+drspafnCr4Yp9oJx1xlIhVXGD
/SyHk5oewkjkNEmrFtUT07mT2lmZqD3XJ+6VaQslRxhPEkLGsXIA/hCucPIplI9j
gLY8TmOBhQ7RzXAnk/ayAzDkCgkWB4k/zaFyLiHjEkE7O7PIjjY51btCLep9QSts
98zojY5oYNj2RdQOZa56MHAlh9hbdpm+P1vp2QBpsDbVpHYv2VPCPvkdOGU1/nzu
msxHy17DcirKP8Tuf6zMf9obeuSlMvUUPptlhwIDAQABAoIBAAJOPyzOWitPzdZw
KNbzbmS/xEbd1UyQJIds+QlkxIjb5iEm4KYakJd8I2Vj7qVJbOkCxpYVqsoiQRBo
FP2cptKSGd045/4SrmoFHBNPXp9FaIMKdcmaX+Wjd83XCFHgsm/O4yYaDpYA/n8q
HFicZxX6Pu8kPkcOXiSx/XzDJYCnuec0GIfiJfbrQEwNLA+Ck2HnFfLy6LyrgCqi
eqaxyBoLolzjW7guWV6e/ECsnLXx2n/Pj4l1aqIFKlYxOjBIKRqeUsqzMFpOCbrx
z/scaBuH88hO96jbGZWUAm3R6ZslocQ6TaENYWNVKN1SeGISiE3hRoMAUIu1eHVu
mEzOjvECgYEA9Ypu04NzVjAHdZRwrP7IiX3+CmbyNatdZXIoagp8boPBYWw7QeL8
TPwvc3PCSIjxcT+Jv2hHTZ9Ofz9vAm/XJx6Ios9o/uAbytA+RAolQJWtLGuFLKv1
wxq78iDFcIWq3iPwpl8FJaXeCb/bsNP9jruPhwWWbJVvD1eTif09ZzsCgYEA3ePo
aQ5S0YrPtaf5r70eSBloe5vveG/kW3EW0QMrN6YlOhGSX+mjdAJk7XI/JW6vVPYS
aK+g+ZnzV7HL421McuVH8mmwPHi48l5o2FewF54qYfOoTAJS1cjV08j8WtQsrEax
HHom4m4joQEm0o4QEnTxJDS8/u7T/hhMALxeziUCgYANwevjvgHAWoCQffiyOLRT
v9N0EcCQcUGSZYsOJfhC2O8E3mOTlXw9dAPUnC/OkJ22krDNILKeDsb/Kja2FD4h
2vwc4zIm1be47WIPveHIdJp3Wq7jid8DR4QwVNW7MEIaoDjjmX9YVKrUMQPGLJqQ
XMH19sIu41CNs4J4wM+n8QKBgBiIcFPdP47neBuvnM2vbT+vf3vbO9jnFip+EHW/
kfGvLwKCmtp77JSRBzOxpAWxfTU5l8N3V6cBPIR/pflZRlCVxSSqRtAI0PoLMjBp
UZDq7eiylfMBdsMoV2v5Ft28A8xwbHinkNEMOGg+xloVVvWTdG36XsMZCNtZOF4E
db75AoGBAIk6IW5O2lk9Vc537TCyLpl2HYCP0jI3v6xIkFFolnfHPEgsXLJo9YU8
crVtB0zy4jzjN/SClc/iaeOzk5Ot+iwSRFBZu2jdt0TRxbG+cd+6vKLs0Baw6kB1
gpRUwP6i5yhi838rMgurGVFr3O/0Sv7wMx5UNEJ/RopbQ2K/bnwn
-----END RSA PRIVATE KEY-----`,
}

func listenAndServeTLS() {
	addr := ":443"
	if *mode == "dev" {
		addr = ":8119"
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

	server := &http.Server{Addr: ln.Addr().String()}
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

func main() {
	flag.Parse()
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
	switch *mode {
	case "dev", "prod":
		log.Printf("Running in %s mode", *mode)
	default:
		log.Fatalf("Unknown mode: %q", *mode)
	}

	http.HandleFunc("/", handleStatus)
	http.HandleFunc("/debug/goroutines", handleDebugGoroutines)
	http.HandleFunc("/builders", handleBuilders)
	http.HandleFunc("/temporarylogs", handleLogs)
	http.HandleFunc("/reverse", handleReverse)
	http.HandleFunc("/style.css", handleStyleCSS)
	http.HandleFunc("/try", handleTryStatus)
	go func() {
		if *mode == "dev" {
			return
		}
		err := http.ListenAndServe(":80", nil)
		if err != nil {
			log.Fatalf("http.ListenAndServe:80: %v", err)
		}
	}()

	workc := make(chan builderRev)

	if *mode == "dev" {
		// TODO(crawshaw): do more in test mode
		gcePool.SetEnabled(false)
		http.HandleFunc("/dosomework/", handleDoSomeWork(workc))
	} else {
		go gcePool.cleanUpOldVMs()

		if devCluster {
			dashboard.BuildletBucket = "dev-go-builder-data"
			dashboard.Builders = devClusterBuilders()
		}

		// Start the Docker processes on this host polling Gerrit and
		// pinging build.golang.org when new commits are available.
		startWatchers() // in watcher.go

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
				continue
			}
			st, err := newBuild(work)
			if err != nil {
				log.Printf("Bad build work params %v: %v", work, err)
			} else {
				st.start()
			}
		case <-ticker.C:
			if numCurrentBuilds() == 0 && time.Now().After(startTime.Add(10*time.Minute)) {
				// TODO: halt the whole machine to kill the VM or something
			}
		}
	}
}

func devClusterBuilders() map[string]dashboard.BuildConfig {
	m := map[string]dashboard.BuildConfig{}
	for _, name := range []string{
		"linux-amd64",
		"linux-amd64-race",
		"windows-amd64-gce",
		"plan9-386",
	} {
		m[name] = dashboard.Builders[name]
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

// mayBuildRev reports whether the build type & revision should be started.
// It returns true if it's not already building, and if a reverse buildlet is
// required, if an appropriate machine is registered.
func mayBuildRev(rev builderRev) bool {
	if isBuilding(rev) {
		return false
	}
	if devCluster && numCurrentBuilds() != 0 {
		return false
	}
	if dashboard.Builders[rev.name].IsReverse {
		return reversePool.CanBuild(rev.name)
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

	if r.FormValue("nostream") != "" {
		fmt.Fprintf(w, "\n\n(no live streaming. reload manually to see status)\n")
		st.mu.Lock()
		defer st.mu.Unlock()
		w.Write(st.output.Bytes())
		return
	}

	if !st.hasEvent("pre_exec") {
		fmt.Fprintf(w, "\n\n(buildlet still starting; no live streaming. reload manually to see status)\n")
		return
	}

	w.(http.Flusher).Flush()

	logs := st.watchLogs()
	defer st.unregisterWatcher(logs)
	closed := w.(http.CloseNotifier).CloseNotify()
	for {
		select {
		case b, ok := <-logs:
			if !ok {
				return
			}
			w.Write(b)
			w.(http.Flusher).Flush()
		case <-closed:
			return
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
		fmt.Fprintf(w, "  started: %v\n", st.done)
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

// findWorkLoop polls http://build.golang.org/?mode=json looking for new work
// for the main dashboard. It does not support gccgo.
// TODO(bradfitz): it also currently does not support subrepos.
func findWorkLoop(work chan<- builderRev) {
	// Useful for debugging a single run:
	if devCluster && false {
		work <- builderRev{name: "linux-amd64", rev: "c9778ec302b2e0e0d6027e1e0fca892e428d9657", subName: "tools", subRev: "ac303766f5f240c1796eeea3dc9bf34f1261aa35"}
		//work <- builderRev{name: "linux-amd64", rev: "54789eff385780c54254f822e09505b6222918e2"}
		//work <- builderRev{name: "windows-amd64-gce", rev: "54789eff385780c54254f822e09505b6222918e2"}

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
	res, err := http.Get(dashBase() + "?mode=json")
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

	var goRevisions []string
	for _, br := range bs.Revisions {
		if br.Repo == "go" {
			goRevisions = append(goRevisions, br.Revision)
		} else {
			// The dashboard provides only the head revision for
			// each sub-repo; store it in subrepoHead for later use.
			subrepoHead.Lock()
			subrepoHead.m[br.Repo] = br.Revision
			subrepoHead.Unlock()
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
			if !ok || builderInfo.TryOnly {
				// Not managed by the coordinator, or a trybot-only one.
				continue
			}
			if br.Repo != "go" && !builderInfo.SplitMakeRun() {
				// If we don't split make and run then we can't
				// have a snapshot from which to build sub-repos.
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
				if !builderInfo.BuildSubrepos() || !rev.snapshotExists() {
					// Don't try to build this sub-repo until we have a snapshot.
					continue
				}
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
	cis, err := gerritClient.QueryChanges("label:Run-TryBot=1 label:TryBot-Result=0 project:go status:open", gerrit.QueryChangesOpt{
		Fields: []string{"CURRENT_REVISION"},
	})
	if err != nil {
		return err
	}
	if len(cis) == 0 {
		return nil
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
		key := tryKey{
			ChangeID: ci.ChangeID,
			Commit:   ci.CurrentRevision,
		}
		tryList = append(tryList, key)
		wanted[key] = true
		if _, ok := tries[key]; ok {
			// already in progress
			continue
		}
		tries[key] = newTrySet(key)
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
	ChangeID string // I1a27695838409259d1586a0adfa9f92bccf7ceba
	Commit   string // ecf3dffc81dc21408fb02159af352651882a8383
}

// trySet is a the state of a set of builds of different
// configurations, all for the same (Change-ID, Commit) pair.  The
// sets which are still wanted (not already submitted or canceled) are
// stored in the global 'tries' map.
type trySet struct {
	// immutable
	tryKey

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

// newTrySet creates a new trySet group of builders for a given key,
// the (Change-ID, Commit) pair.  It also starts goroutines for each
// build.
//
// Must hold statusMu.
func newTrySet(key tryKey) *trySet {
	log.Printf("Starting new trybot set for %v", key)
	ts := &trySet{
		tryKey: key,
		trySetState: trySetState{
			remain: len(tryBuilders),
			builds: make([]*buildStatus, len(tryBuilders)),
		},
	}
	go ts.notifyStarting()
	for i, bconf := range tryBuilders {
		brev := builderRev{name: bconf.Name, rev: key.Commit}

		bs, _ := newBuild(brev)
		bs.trySet = ts
		status[brev] = bs
		ts.builds[i] = bs
		go bs.start() // acquires statusMu itself, so in a goroutine
		go ts.awaitTryBuild(i, bconf, bs)
	}
	return ts
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

	if ci, err := gerritClient.GetChangeDetail(ts.ChangeID); err == nil {
		if len(ci.Messages) == 0 {
			log.Printf("No Gerrit comments retrieved on %v", ts.ChangeID)
		}
		for _, cmi := range ci.Messages {
			if strings.Contains(cmi.Message, msg) {
				// Dup. Don't spam.
				return
			}
		}
	} else {
		log.Printf("Error getting Gerrit comments on %s: %v", ts.ChangeID, err)
	}

	// Ignore error. This isn't critical.
	gerritClient.SetReview(ts.ChangeID, ts.Commit, gerrit.ReviewInput{Message: msg})
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
			case <-bs.donec:
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
		brev := builderRev{name: bconf.Name, rev: ts.Commit}
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
		wr := storage.NewWriter(serviceCtx, buildLogBucket(), objName)
		wr.ContentType = "text/plain; charset=utf-8"
		wr.ACL = append(wr.ACL, storage.ACLRule{Entity: storage.AllUsers, Role: storage.RoleReader})
		if _, err := io.WriteString(wr, buildLog); err != nil {
			log.Printf("Failed to write to GCS: %v", err)
			return
		}
		if err := wr.Close(); err != nil {
			log.Printf("Failed to write to GCS: %v", err)
			return
		}
		failLogURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", buildLogBucket(), objName)

		bs.mu.Lock()
		bs.failURL = failLogURL
		bs.mu.Unlock()
		ts.mu.Lock()
		fmt.Fprintf(&ts.errMsg, "Failed on %s: %s\n", bs.name, failLogURL)
		ts.mu.Unlock()

		if numFail == 1 && remain > 0 {
			if err := gerritClient.SetReview(ts.ChangeID, ts.Commit, gerrit.ReviewInput{
				Message: fmt.Sprintf(
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
		if err := gerritClient.SetReview(ts.ChangeID, ts.Commit, gerrit.ReviewInput{
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

func (br builderRev) isSubrepo() bool {
	return br.subName != ""
}

type eventTimeLogger interface {
	logEventTime(event string, optText ...string)
}

var ErrCanceled = errors.New("canceled")

// Cancel is a channel that's closed by the caller when the request is no longer
// desired. The function receiving a cancel should return ErrCanceled whenever
// Cancel becomes readable.
type Cancel <-chan struct{}

func (c Cancel) IsCanceled() bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

type BuildletPool interface {
	// GetBuildlet returns a new buildlet client.
	//
	// The machineType is the machine type (e.g. "linux-amd64-race").
	//
	// The rev is git hash. Implementations should not use it for
	// anything except for log messages or VM naming.
	//
	// Clients must Close when done with the client.
	GetBuildlet(cancel Cancel, machineType, rev string, el eventTimeLogger) (*buildlet.Client, error)

	String() string // TODO(bradfitz): more status stuff
}

// GetBuildlets creates up to n buildlets and sends them on the returned channel
// before closing the channel.
func GetBuildlets(cancel Cancel, pool BuildletPool, n int, machineType, rev string, el eventTimeLogger) <-chan *buildlet.Client {
	ch := make(chan *buildlet.Client) // NOT buffered
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			bc, err := pool.GetBuildlet(cancel, machineType, rev, el)
			if err != nil {
				if err != ErrCanceled {
					log.Printf("failed to get a %s buildlet for rev %s: %v", machineType, rev, err)
				}
				return
			}
			el.logEventTime("helper_ready")
			select {
			case ch <- bc:
			case <-cancel:
				el.logEventTime("helper_killed_before_use")
				bc.Close()
				return
			}
		}()
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}

func poolForConf(conf dashboard.BuildConfig) (BuildletPool, error) {
	if conf.VMImage != "" {
		return gcePool, nil
	}
	return reversePool, nil
}

func newBuild(rev builderRev) (*buildStatus, error) {
	// Note: can't acquire statusMu in newBuild, as this is called
	// from findTryWork -> newTrySet, which holds statusMu.

	conf, ok := dashboard.Builders[rev.name]
	if !ok {
		return nil, fmt.Errorf("unknown builder type %q", rev.name)
	}
	return &buildStatus{
		builderRev: rev,
		conf:       conf,
		donec:      make(chan struct{}),
		startTime:  time.Now(),
	}, nil
}

// start sets the st.startTime and starts the build in a new goroutine.
// If it returns an error, st is not modified and a new goroutine has not
// been started.
// The build status's donec channel is closed on when the build is complete
// in either direction.
func (st *buildStatus) start() {
	setStatus(st.builderRev, st)
	go st.pingDashboard()
	go func() {
		err := st.build()
		if err != nil {
			fmt.Fprintf(st, "\n\nError: %v\n", err)
		}
		st.setDone(err == nil)
		markDone(st.builderRev)
	}()
}

func (st *buildStatus) buildletType() string {
	if v := st.conf.BuildletType; v != "" {
		return v
	}
	return st.conf.Name
}

func (st *buildStatus) buildletPool() (BuildletPool, error) {
	buildletType := st.buildletType()
	bconf, ok := dashboard.Builders[buildletType]
	if !ok {
		return nil, fmt.Errorf("invalid BuildletType %q for %q", buildletType, st.conf.Name)
	}
	return poolForConf(bconf)
}

func (st *buildStatus) expectedMakeBashDuration() time.Duration {
	// TODO: base this on historical measurements, instead of statically configured.
	// TODO: move this to dashboard/builders.go? But once we based on on historical
	// measurements, it'll need GCE services (bigtable/bigquery?), so it's probably
	// better in this file.
	goos, goarch := st.conf.GOOS(), st.conf.GOARCH()

	if goos == "plan9" {
		return 2500 * time.Millisecond
	}
	if goos == "linux" {
		if goarch == "arm" {
			return 4 * time.Minute
		}
		return 1000 * time.Millisecond
	}
	if goos == "windows" {
		return 1000 * time.Millisecond
	}

	return 1500 * time.Millisecond
}

func (st *buildStatus) expectedBuildletStartDuration() time.Duration {
	// TODO: move this to dashboard/builders.go? But once we based on on historical
	// measurements, it'll need GCE services (bigtable/bigquery?), so it's probably
	// better in this file.
	pool, _ := st.buildletPool()
	switch pool.(type) {
	case *gceBuildletPool:
		return time.Minute
	case *reverseBuildletPool:
		goos, arch := st.conf.GOOS(), st.conf.GOARCH()
		if goos == "darwin" {
			if arch == "arm" && arch == "arm64" {
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
	if st.isSubrepo() || st.conf.NumTestHelpers == 0 {
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
	pool, _ := st.buildletPool() // won't return an error since we called it already
	st.helpers = GetBuildlets(st.donec, pool, st.conf.NumTestHelpers, st.buildletType(), st.rev, st)
}

// We should try to build from a snapshot if this is a subrepo build, we can
// expect there to be a snapshot (splitmakerun), and the snapshot exists.
func (st *buildStatus) useSnapshot() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.useSnapshotMemo != nil {
		return *st.useSnapshotMemo
	}
	b := st.isSubrepo() && st.conf.SplitMakeRun() && st.snapshotExists()
	st.useSnapshotMemo = &b
	return b
}

func (st *buildStatus) build() error {
	pool, err := st.buildletPool()
	if err != nil {
		return err
	}
	st.logEventTime("get_buildlet")
	bc, err := pool.GetBuildlet(nil, st.buildletType(), st.rev, st)
	if err != nil {
		return fmt.Errorf("failed to get a buildlet: %v", err)
	}
	defer bc.Close()
	st.mu.Lock()
	st.bc = bc
	st.mu.Unlock()

	st.logEventTime("got_buildlet", bc.IPPort())

	if st.useSnapshot() {
		st.logEventTime("start_write_snapshot_tar")
		if err := bc.PutTarFromURL(st.snapshotURL(), "go"); err != nil {
			return fmt.Errorf("failed to put snapshot to buildlet: %v", err)
		}
		st.logEventTime("end_write_snapshot_tar")
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
	st.logEventTime("pre_exec")
	fmt.Fprintf(st, "%s at %v\n\n", st.name, st.rev)

	var remoteErr error
	if st.conf.SplitMakeRun() {
		remoteErr, err = st.runAllSharded()
	} else {
		remoteErr, err = st.runAllLegacy()
	}
	doneMsg := "all tests passed"
	if remoteErr != nil {
		doneMsg = "with test failures"
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

// runAllSharded runs make.bash and then shards the test execution.
// remoteErr and err are as described at the top of this file.
func (st *buildStatus) runAllSharded() (remoteErr, err error) {
	st.getHelpersReadySoon()

	remoteErr, err = st.runMake()
	if err != nil {
		return nil, err
	}
	if remoteErr != nil {
		return fmt.Errorf("build failed: %v", remoteErr), nil
	}

	if err := st.doSnapshot(); err != nil {
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

// runMake builds the tool chain.
// remoteErr and err are as described at the top of this file.
func (st *buildStatus) runMake() (remoteErr, err error) {
	// Don't do this if we're using a pre-built snapshot.
	if st.useSnapshot() {
		return nil, nil
	}

	// Build the source code.
	makeScript := st.conf.MakeScript()
	t0 := time.Now()
	remoteErr, err = st.bc.Exec(path.Join("go", makeScript), buildlet.ExecOpts{
		Output: st,
		OnStartExec: func() {
			st.logEventTime("running_exec", makeScript)
		},
		ExtraEnv: st.conf.Env(),
		Debug:    true,
		Args:     st.conf.MakeScriptArgs(),
	})
	if err != nil {
		return nil, err
	}
	st.logEventTime("exec_done", fmt.Sprintf("%s in %v", makeScript, time.Since(t0)))
	if remoteErr != nil {
		return fmt.Errorf("make script failed: %v", remoteErr), nil
	}
	return nil, nil
}

// runAllLegacy executes all.bash (or .bat, or whatever) in the traditional way.
// remoteErr and err are as described at the top of this file.
//
// TODO(bradfitz,adg): delete this function when all builders
// can split make & run (and then delete the SplitMakeRun method)
func (st *buildStatus) runAllLegacy() (remoteErr, err error) {
	st.logEventTime("legacy_all_path")
	allScript := st.conf.AllScript()
	t0 := time.Now()
	remoteErr, err = st.bc.Exec(path.Join("go", allScript), buildlet.ExecOpts{
		Output: st,
		OnStartExec: func() {
			st.logEventTime("running_exec", allScript)
		},
		ExtraEnv: st.conf.Env(),
		Debug:    true,
		Args:     st.conf.AllScriptArgs(),
	})
	if err != nil {
		return err, nil
	}
	st.logEventTime("exec_done", fmt.Sprintf("%s in %v", allScript, time.Since(t0)))
	if remoteErr != nil {
		return fmt.Errorf("all script failed: %v", remoteErr), nil
	}
	return nil, nil
}

func (st *buildStatus) doSnapshot() error {
	// If we're using a pre-built snapshot, don't make another.
	if st.useSnapshot() {
		return nil
	}

	if err := st.cleanForSnapshot(); err != nil {
		return fmt.Errorf("cleanForSnapshot: %v", err)
	}
	if err := st.writeSnapshot(); err != nil {
		return fmt.Errorf("writeSnapshot: %v", err)
	}
	return nil
}

func (br *builderRev) snapshotExists() bool {
	resp, err := http.Head(br.snapshotURL())
	return err == nil && resp.StatusCode == http.StatusOK
}

func (st *buildStatus) writeGoSource() error {
	// Write the VERSION file.
	st.logEventTime("start_write_version_tar")
	if err := st.bc.PutTar(versionTgz(st.rev), "go"); err != nil {
		return fmt.Errorf("writing VERSION tgz: %v", err)
	}

	st.logEventTime("fetch_go_tar")
	tarReader, err := getSourceTgz(st, "go", st.rev)
	if err != nil {
		return err
	}
	st.logEventTime("start_write_go_tar")
	if err := st.bc.PutTar(tarReader, "go"); err != nil {
		return fmt.Errorf("writing tarball from Gerrit: %v", err)
	}
	st.logEventTime("end_write_go_tar")
	return nil
}

func (st *buildStatus) writeBootstrapToolchain() error {
	if st.conf.Go14URL == "" {
		return nil
	}
	st.logEventTime("start_write_go14_tar")
	if err := st.bc.PutTarFromURL(st.conf.Go14URL, "go1.4"); err != nil {
		return err
	}
	st.logEventTime("end_write_go14_tar")
	return nil
}

var cleanForSnapshotFiles = []string{
	"go/doc/gopher",
	"go/pkg/bootstrap",
}

func (st *buildStatus) cleanForSnapshot() error {
	st.logEventTime("clean_for_snapshot")
	defer st.logEventTime("clean_for_snapshot_done")

	return st.bc.RemoveAll(cleanForSnapshotFiles...)
}

// snapshotObjectName is the cloud storage object name of the
// built Go tree for this builder and Go rev (not the sub-repo).
// The entries inside this tarball do not begin with "go/".
func (br *builderRev) snapshotObjectName() string {
	return fmt.Sprintf("%v/%v/%v.tar.gz", "go", br.name, br.rev)
}

// snapshotURL is the absolute URL of the snapshot object (see above).
func (br *builderRev) snapshotURL() string {
	return fmt.Sprintf("https://storage.googleapis.com/%s/%s", snapBucket(), br.snapshotObjectName())
}

func (st *buildStatus) writeSnapshot() error {
	st.logEventTime("write_snapshot")
	defer st.logEventTime("write_snapshot_done")

	tgz, err := st.bc.GetTar("go")
	if err != nil {
		return err
	}
	defer tgz.Close()

	wr := storage.NewWriter(serviceCtx, snapBucket(), st.snapshotObjectName())
	wr.ContentType = "application/octet-stream"
	wr.ACL = append(wr.ACL, storage.ACLRule{Entity: storage.AllUsers, Role: storage.RoleReader})
	if _, err := io.Copy(wr, tgz); err != nil {
		wr.Close()
		return err
	}

	return wr.Close()
}

func (st *buildStatus) distTestList() (names []string, err error) {
	var buf bytes.Buffer
	remoteErr, err := st.bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
		Output:      &buf,
		ExtraEnv:    st.conf.Env(),
		OnStartExec: func() { st.logEventTime("discovering_tests") },
		Path:        []string{"$WORKDIR/go/bin", "$PATH"},
		Args:        []string{"tool", "dist", "test", "--no-rebuild", "--list"},
	})
	if err != nil {
		return nil, fmt.Errorf("Exec error: %v, %s", remoteErr, buf.Bytes())
	}
	if remoteErr != nil {
		return nil, fmt.Errorf("Remote error: %v, %s", remoteErr, buf.Bytes())
	}
	return strings.Fields(buf.String()), nil
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
	"go_test:cmd/go":                         3.63,
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
	"test":                                   45, // old, but valid for a couple weeks from 2015-06-04
	"test:0_5":                               10,
	"test:1_5":                               10,
	"test:2_5":                               10,
	"test:3_5":                               10,
	"test:4_5":                               10,
	"codewalk":                               2.42,
	"api":                                    7.38,
}

// testDuration predicts how long the dist test 'name' will take.
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
		log.Printf("error discovering workdir for helper %s: %v", st.bc.IPPort(), err)
		return
	}
	goroot := st.conf.FilePathJoin(workDir, "go")
	gopath := st.conf.FilePathJoin(workDir, "gopath")

	fetched := map[string]bool{}
	toFetch := []string{st.subName}

	// fetch checks out the provided sub-repo to the buildlet's workspace.
	fetch := func(repo, rev string) error {
		fetched[repo] = true
		tgz, err := getSourceTgz(st, repo, rev)
		if err != nil {
			return err
		}
		return st.bc.PutTar(tgz, "gopath/src/"+subrepoPrefix+repo)
	}

	// findDeps uses 'go list' on the checked out repo to find its
	// dependencies, and adds any not-yet-fetched deps to toFetched.
	findDeps := func(repo string) error {
		repoPath := subrepoPrefix + repo
		var buf bytes.Buffer
		rErr, err := st.bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
			Output:   &buf,
			ExtraEnv: append(st.conf.Env(), "GOROOT="+goroot, "GOPATH="+gopath),
			Path:     []string{"$WORKDIR/go/bin", "$PATH"},
			Args:     []string{"list", "-f", `{{range .Deps}}{{printf "%v\n" .}}{{end}}`, repoPath + "/..."},
		})
		if err != nil {
			return fmt.Errorf("exec go list on buildlet: %v", err)
		}
		if rErr != nil {
			return fmt.Errorf("go list error on buildlet: %v\n%s", rErr, buf.Bytes())
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
		return nil
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
		subrepoHead.Lock()
		rev := subrepoHead.m[repo]
		subrepoHead.Unlock()
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
		if err := findDeps(repo); err != nil {
			return nil, err
		}
	}

	st.logEventTime("starting_tests", st.subName)
	defer st.logEventTime("tests_complete")
	return st.bc.Exec(path.Join("go", "bin", "go"), buildlet.ExecOpts{
		Output:   st,
		ExtraEnv: append(st.conf.Env(), "GOROOT="+goroot, "GOPATH="+gopath),
		Path:     []string{"$WORKDIR/go/bin", "$PATH"},
		Args:     []string{"test", "-short", subrepoPrefix + st.subName + "/..."},
	})
}

// runTests is only called for builders which support a split make/run
// (should be everything, at least soon). Currently (2015-05-27) iOS
// and Android and Nacl may not. Untested.
func (st *buildStatus) runTests(helpers <-chan *buildlet.Client) (remoteErr, err error) {
	testNames, err := st.distTestList()
	if err != nil {
		return nil, fmt.Errorf("distTestList: %v", err)
	}
	set := st.newTestSet(testNames)
	st.logEventTime("starting_tests", fmt.Sprintf("%d tests", len(set.items)))
	startTime := time.Now()

	// We use our original buildlet to run the tests in order, to
	// make the streaming somewhat smooth and not incredibly
	// lumpy.  The rest of the buildlets run the largest tests
	// first (critical path scheduling).
	go func() {
		for {
			tis, ok := set.testsToRunInOrder()
			if !ok {
				st.logEventTime("in_order_tests_complete")
				return
			}
			goroot := "" // no need to override; main buildlet's GOROOT is baked into binaries
			st.runTestsOnBuildlet(st.bc, tis, goroot)
		}
	}()
	go func() {
		for helper := range helpers {
			go func(bc *buildlet.Client) {
				defer st.logEventTime("closed_helper", bc.IPPort())
				defer bc.Close()
				if devPause {
					defer time.Sleep(5 * time.Minute)
					defer st.logEventTime("DEV_HELPER_SLEEP", bc.IPPort())
				}
				st.logEventTime("got_helper", bc.IPPort())
				if err := bc.PutTarFromURL(st.snapshotURL(), "go"); err != nil {
					log.Printf("failed to extract snapshot for helper %s: %v", bc.IPPort(), err)
					return
				}
				workDir, err := bc.WorkDir()
				if err != nil {
					log.Printf("error discovering workdir for helper %s: %v", bc.IPPort(), err)
					return
				}
				st.logEventTime("setup_helper", bc.IPPort())
				goroot := st.conf.FilePathJoin(workDir, "go")
				for {
					tis, ok := set.testsToRunBiggestFirst()
					if !ok {
						st.logEventTime("biggest_tests_complete", bc.IPPort())
						return
					}
					st.runTestsOnBuildlet(bc, tis, goroot)
				}
			}(helper)
		}
	}()

	var lastBanner string
	var serialDuration time.Duration
	for _, ti := range set.items {
	AwaitDone:
		for {
			select {
			case <-ti.done: // wait for success
				break AwaitDone
			case <-time.After(30 * time.Second):
				st.logEventTime("still_waiting_on_test", ti.name)
			}
		}

		serialDuration += ti.execDuration
		if len(ti.output) > 0 {
			banner, out := parseOutputAndBanner(ti.output)
			if banner != lastBanner {
				lastBanner = banner
				fmt.Fprintf(st, "\n##### %s\n", banner)
			}
			if devCluster {
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
	shardedDuration := time.Since(startTime)
	st.logEventTime("tests_complete", fmt.Sprintf("took %v; aggregate %v; saved %v", shardedDuration, serialDuration, serialDuration-shardedDuration))
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

// runTestsOnBuildlet runs tis on bc, using the optional goroot environment variable.
func (st *buildStatus) runTestsOnBuildlet(bc *buildlet.Client, tis []*testItem, goroot string) {
	names := make([]string, len(tis))
	for i, ti := range tis {
		names[i] = ti.name
		if i > 0 && !strings.HasPrefix(ti.name, "go_test:") {
			panic("only go_test:* tests may be merged")
		}
	}
	which := fmt.Sprintf("%s: %v", bc.IPPort(), names)
	st.logEventTime("start_tests", which)

	// TODO(bradfitz,adg): a few weeks after
	// https://go-review.googlesource.com/10688 is submitted,
	// around Jun 18th 2015, remove this innerRx stuff and just
	// pass a list of test names to dist instead. We don't want to
	// do it right away, so people don't have to rebase their CLs
	// to avoid trybot failures.
	var innerRx string
	if len(tis) > 1 {
		innerRx = "(" + strings.Join(names, "|") + ")"
	} else {
		innerRx = names[0]
	}

	var buf bytes.Buffer
	t0 := time.Now()
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
		Path:     []string{"$WORKDIR/go/bin", "$PATH"},
		Args:     []string{"tool", "dist", "test", "--no-rebuild", "--banner=" + banner, "--run=^" + innerRx + "$"},
	})
	summary := "ok"
	if err != nil {
		summary = "commErr=" + err.Error()
	} else if remoteErr != nil {
		summary = "test failed remotely"
	}
	execDuration := time.Since(t0)
	st.logEventTime("end_tests", fmt.Sprintf("%s; %s after %v", which, summary, execDuration))
	if err != nil {
		for _, ti := range tis {
			ti.numFail++
			st.logf("Execution error running %s on %s: %v (numFails = %d)", ti.name, bc, err, ti.numFail)
			if ti.numFail >= maxTestExecErrors {
				msg := fmt.Sprintf("Failed to schedule %q test after %d tries.\n", ti.name, maxTestExecErrors)
				ti.output = []byte(msg)
				ti.remoteErr = errors.New(msg)
				close(ti.done)
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
	conf      dashboard.BuildConfig
	startTime time.Time     // actually time of newBuild (~same thing)
	trySet    *trySet       // or nil
	donec     chan struct{} // closed when done

	onceInitHelpers sync.Once // guards call of onceInitHelpersFunc, to init::
	helpers         <-chan *buildlet.Client

	mu              sync.Mutex       // guards following
	failURL         string           // if non-empty, permanent URL of failure
	bc              *buildlet.Client // nil initially, until pool returns one
	done            time.Time        // finished running
	succeeded       bool             // set when done
	output          bytes.Buffer     // stdout and stderr
	events          []eventAndTime
	watcher         []*logWatcher
	useSnapshotMemo *bool
}

func (st *buildStatus) setDone(succeeded bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.succeeded = succeeded
	st.done = time.Now()
	st.notifyWatchersLocked(true)
	close(st.donec)
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

func (st *buildStatus) logEventTime(event string, optText ...string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	var text string
	if len(optText) > 0 {
		if len(optText) > 1 {
			panic("usage")
		}
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
func (st *buildStatus) HTMLStatusLine() template.HTML {
	st.mu.Lock()
	defer st.mu.Unlock()

	urlPrefix := "https://go-review.googlesource.com/#/q/"
	if strings.Contains(st.name, "gccgo") {
		urlPrefix = "https://code.google.com/p/gofrontend/source/detail?r="
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<a href='https://github.com/golang/go/wiki/DashboardBuilders'>%s</a> rev <a href='%s%s'>%s</a>",
		st.name, urlPrefix, st.rev, st.rev)
	if st.isSubrepo() {
		fmt.Fprintf(&buf, " (sub-repo %s rev <a href='%s%s'>%s</a>)",
			st.subName, urlPrefix, st.subRev, st.subRev)
	}
	if ts := st.trySet; ts != nil {
		fmt.Fprintf(&buf, " (trying <a href='https://go-review.googlesource.com/#/q/%s'>%s</a>)",
			ts.ChangeID, ts.ChangeID[:8])
	}

	if st.done.IsZero() {
		buf.WriteString(", running")
	} else if st.succeeded {
		buf.WriteString(", succeeded")
	} else {
		buf.WriteString(", failed")
	}

	fmt.Fprintf(&buf, "; <a href='%s'>build log</a>; %s", st.logsURLLocked(), html.EscapeString(st.bc.String()))

	t := st.done
	if t.IsZero() {
		t = st.startTime
	}
	fmt.Fprintf(&buf, ", %v ago\n", time.Since(t))
	st.writeEventsLocked(&buf, true)
	return template.HTML(buf.String())
}

func (st *buildStatus) logsURLLocked() string {
	host := "farmer.golang.org"
	if devCluster {
		host = externalIP
	}
	u := fmt.Sprintf("http://%v/temporarylogs?name=%s&rev=%s&st=%p", host, st.name, st.rev, st)
	if st.isSubrepo() {
		u += fmt.Sprintf("&subName=%v&subRev=%v", st.subName, st.subRev)
	}
	return u
}

// st.mu must be held.
func (st *buildStatus) writeEventsLocked(w io.Writer, htmlMode bool) {
	var lastT time.Time
	for i, evt := range st.events {
		lastT = evt.t
		var elapsed string
		if i != 0 {
			elapsed = fmt.Sprintf("+%0.1fs", evt.t.Sub(st.events[i-1].t).Seconds())
		}
		e := evt.evt
		text := evt.text
		if htmlMode {
			if e == "running_exec" {
				e = fmt.Sprintf("<a href='%s'>%s</a>", st.logsURLLocked(), e)
			}
			e = "<b>" + e + "</b>"
			text = "<i>" + html.EscapeString(text) + "</i>"
		}
		fmt.Fprintf(w, " %7s %v %s %s\n", elapsed, evt.t.Format(time.RFC3339), e, text)
	}
	if st.isRunningLocked() {
		fmt.Fprintf(w, " %7s (now)\n", fmt.Sprintf("+%0.1fs", time.Since(lastT).Seconds()))
	}

}

func (st *buildStatus) logs() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.output.String()
}

func (st *buildStatus) Write(p []byte) (n int, err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	const maxBufferSize = 2 << 20 // 2MB of output is way more than we expect.
	plen := len(p)
	if st.output.Len()+len(p) > maxBufferSize {
		p = p[:maxBufferSize-st.output.Len()]
	}
	st.output.Write(p) // bytes.Buffer can't fail
	st.notifyWatchersLocked(false)
	return plen, nil
}

// logWatcher holds the state of a client watching the logs of a running build.
type logWatcher struct {
	ch     chan []byte
	offset int // Offset of seen logs (offset == len(buf) means "up to date")
}

// watchLogs returns a channel on which the build's logs is sent.
// When the build is complete the channel is closed.
func (st *buildStatus) watchLogs() <-chan []byte {
	st.mu.Lock()
	defer st.mu.Unlock()

	ch := make(chan []byte, 10) // room for a few log writes
	ch <- st.output.Bytes()
	if !st.isRunningLocked() {
		close(ch)
		return ch
	}

	st.watcher = append(st.watcher, &logWatcher{
		ch:     ch,
		offset: st.output.Len(),
	})
	return ch
}

// unregisterWatcher removes the provided channel from the list of watchers,
// so that it receives no further log data.
func (st *buildStatus) unregisterWatcher(ch <-chan []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()

	for i, w := range st.watcher {
		if w.ch == ch {
			st.watcher = append(st.watcher[:i], st.watcher[i+1:]...)
			break
		}
	}
}

// notifyWatchersLocked pushes any new log data to watching clients.
// If done is true it closes any watcher channels.
//
// NOTE: st.mu must be held.
func (st *buildStatus) notifyWatchersLocked(done bool) {
	l := st.output.Len()
	for _, w := range st.watcher {
		if w.offset < l {
			select {
			case w.ch <- st.output.Bytes()[w.offset:]:
				w.offset = l
			default:
				// If the receiver isn't ready, drop the write.
			}
		}
		if done {
			close(w.ch)
		}
	}
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

// repo is go.googlesource.com repo ("go", "net", etc)
// rev is git revision.
func getSourceTgz(el eventTimeLogger, repo, rev string) (tgz io.Reader, err error) {
	fromCache := false
	key := fmt.Sprintf("%v-%v", repo, rev)
	vi, err, shared := sourceGroup.Do(key, func() (interface{}, error) {
		if tgzBytes, ok := sourceCache.Get(key); ok {
			fromCache = true
			return tgzBytes, nil
		}

		for i := 0; i < 10; i++ {
			el.logEventTime("fetching_source", fmt.Sprintf("%v from watcher", key))
			tgzBytes, err := getSourceTgzFromWatcher(repo, rev)
			if err == nil {
				sourceCache.Add(key, tgzBytes)
				return tgzBytes, nil
			}
			log.Printf("Error fetching source %s/%s from watcher (after %v uptime): %v",
				repo, rev, time.Since(processStartTime), err)
			// Wait for watcher to start up. Give it a minute until
			// we try Gerrit.
			time.Sleep(6 * time.Second)
		}

		el.logEventTime("fetching_source", fmt.Sprintf("%v from gerrit", key))
		tgzBytes, err := getSourceTgzFromGerrit(repo, rev)
		if err == nil {
			sourceCache.Add(key, tgzBytes)
		}
		return tgzBytes, err
	})
	if err != nil {
		return nil, err
	}
	el.logEventTime("got_source", fmt.Sprintf("%v cache=%v shared=%v", key, fromCache, shared))
	return bytes.NewReader(vi.([]byte)), nil
}

func getSourceTgzFromGerrit(repo, rev string) (tgz []byte, err error) {
	return getSourceTgzFromURL("gerrit", repo, rev, "https://go.googlesource.com/"+repo+"/+archive/"+rev+".tar.gz")
}

func getSourceTgzFromWatcher(repo, rev string) (tgz []byte, err error) {
	return getSourceTgzFromURL("watcher", repo, rev, "http://"+gitArchiveAddr+"/"+repo+".tar.gz?rev="+rev)
}

func getSourceTgzFromURL(source, repo, rev, urlStr string) (tgz []byte, err error) {
	res, err := http.Get(urlStr)
	if err != nil {
		return nil, fmt.Errorf("fetching %s/%s from %s: %v", repo, rev, source, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		slurp, _ := ioutil.ReadAll(io.LimitReader(res.Body, 4<<10))
		return nil, fmt.Errorf("fetching %s/%s from %s: %v; body: %s", repo, rev, source, res.Status, slurp)
	}
	const maxSize = 25 << 20 // seems unlikely; go source is 7.8MB on 2015-06-15
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
