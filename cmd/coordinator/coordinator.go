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
	"strings"
	"sync"
	"time"

	"camlistore.org/pkg/syncutil"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/types"
	"google.golang.org/cloud/storage"
)

var processStartTime = time.Now()

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

	buildLogBucket = flag.String("logbucket", "go-build-log", "GCS bucket to put trybot build failures in")

	mode = flag.String("mode", "", "valid modes are 'dev', 'prod', or '' for auto-detect")
)

// LOCK ORDER:
//   statusMu, buildStatus.mu, trySet.mu
// TODO(bradfitz,adg): rewrite the coordinator

var (
	startTime = time.Now()

	statusMu   sync.Mutex // guards the following four structures; see LOCK ORDER comment above
	status     = map[builderRev]*buildStatus{}
	statusDone []*buildStatus         // finished recently, capped to maxStatusDone
	tries      = map[tryKey]*trySet{} // trybot builds
	tryList    []tryKey
)

// tryBuilders must be VMs. The Docker container builds are going away.
var tryBuilders []dashboard.BuildConfig

func init() {
	tryList := []string{
		"all-compile",
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
		"plan9-386-gcepartial",
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

	bucket := "go-builder-data"
	if devCluster {
		bucket = "dev-go-builder-data"
	}
	r, err := storage.NewReader(serviceCtx, bucket, name)
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
	http.HandleFunc("/logs", handleLogs)
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
			// Only run the linux-amd64 builder in the dev cluster (for now).
			conf := dashboard.Builders["linux-amd64"]
			conf.SetBuildletBinaryURL(strings.Replace(conf.BuildletBinaryURL(), "go-builder-data", "dev-go-builder-data", 1))
			dashboard.Builders = map[string]dashboard.BuildConfig{"linux-amd64": conf}
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
			log.Printf("workc received %+v; len(status) = %v, cur = %p", work, len(status), status[work])
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
	st := getStatus(builderRev{r.FormValue("name"), r.FormValue("rev")}, r.FormValue("st"))
	if st == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeStatusHeader(w, st)

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
			// TODO(bradfitz): support these: golang.org/issue/9506
			continue
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
			if builderInfo, ok := dashboard.Builders[builder]; !ok || builderInfo.TryOnly {
				// Not managed by the coordinator, or a trybot-only one.
				continue
			}
			br := builderRev{bs.Builders[i], br.Revision}
			if !isBuilding(br) {
				work <- br
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
			br := builderRev{b, rev}
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
	// Ignore error. This isn't critical.
	gerritClient.SetReview(ts.ChangeID, ts.Commit, gerrit.ReviewInput{
		// TODO: good hostname, SSL, and pretty handler for just this tryset
		Message: "TryBots beginning. Status page: http://farmer.golang.org/try?commit=" + ts.Commit[:8],
	})
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
		wr := storage.NewWriter(serviceCtx, *buildLogBucket, objName)
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
		failLogURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", *buildLogBucket, objName)
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
	rev  string // lowercase hex git hash
	// TODO: optional subrepo name/hash
}

type eventTimeLogger interface {
	logEventTime(name string)
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
	GetBuildlet(machineType, rev string, el eventTimeLogger) (*buildlet.Client, error)

	String() string // TODO(bradfitz): more status stuff
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

func (st *buildStatus) build() (retErr error) {
	buildletType := st.conf.BuildletType
	if buildletType == "" {
		buildletType = st.conf.Name
	}
	bconf, ok := dashboard.Builders[buildletType]
	if !ok {
		return fmt.Errorf("invalid BuildletType %q for %q", buildletType, st.conf.Name)
	}
	pool, err := poolForConf(bconf)
	if err != nil {
		return err
	}
	st.logEventTime("get_buildlet")
	bc, err := pool.GetBuildlet(buildletType, st.rev, st)
	if err != nil {
		return fmt.Errorf("failed to get a buildlet: %v", err)
	}
	defer bc.Close()
	st.mu.Lock()
	st.bc = bc
	st.mu.Unlock()

	st.logEventTime("got_buildlet")
	goodRes := func(res *http.Response, err error, what string) bool {
		if err != nil {
			retErr = fmt.Errorf("%s: %v", what, err)
			return false
		}
		if res.StatusCode/100 != 2 {
			slurp, _ := ioutil.ReadAll(io.LimitReader(res.Body, 4<<10))
			retErr = fmt.Errorf("%s: %v; body: %s", what, res.Status, slurp)
			res.Body.Close()
			return false

		}
		return true
	}

	// Write the VERSION file.
	st.logEventTime("start_write_version_tar")
	if err := bc.PutTar(versionTgz(st.rev), "go"); err != nil {
		return fmt.Errorf("writing VERSION tgz: %v", err)
	}

	// Feed the buildlet a tar file for it to extract.
	// TODO: cache these.
	st.logEventTime("start_fetch_gerrit_tgz")
	tarRes, err := http.Get("https://go.googlesource.com/go/+archive/" + st.rev + ".tar.gz")
	if !goodRes(tarRes, err, "fetching tarball from Gerrit") {
		return
	}

	var grp syncutil.Group
	grp.Go(func() error {
		st.logEventTime("start_write_go_tar")
		if err := bc.PutTar(tarRes.Body, "go"); err != nil {
			tarRes.Body.Close()
			return fmt.Errorf("writing tarball from Gerrit: %v", err)
		}
		st.logEventTime("end_write_go_tar")
		return nil
	})
	if st.conf.Go14URL != "" {
		grp.Go(func() error {
			st.logEventTime("start_write_go14_tar")
			if err := bc.PutTarFromURL(st.conf.Go14URL, "go1.4"); err != nil {
				return err
			}
			st.logEventTime("end_write_go14_tar")
			return nil
		})
	}
	if err := grp.Err(); err != nil {
		return err
	}

	execStartTime := time.Now()
	st.logEventTime("pre_exec")
	fmt.Fprintf(st, "%s at %v\n\n", st.name, st.rev)

	makeScript := st.conf.MakeScript()
	lastScript := makeScript
	remoteErr, err := bc.Exec(path.Join("go", makeScript), buildlet.ExecOpts{
		Output: st,
		OnStartExec: func() {
			st.logEventTime("running_exec") // TODO(adg): remove this?
			st.logEventTime("make_exec")
		},
		ExtraEnv: st.conf.Env(),
		Debug:    true,
		Args:     st.conf.MakeScriptArgs(),
	})
	if err != nil {
		return err
	}
	st.logEventTime("make_done")

	if remoteErr == nil {
		runScript := st.conf.RunScript()
		lastScript = runScript
		remoteErr, err = bc.Exec(path.Join("go", runScript), buildlet.ExecOpts{
			Output:      st,
			OnStartExec: func() { st.logEventTime("run_exec") },
			ExtraEnv:    st.conf.Env(),
			// all.X sources make.X which adds $GOROOT/bin to $PATH,
			// so run.X expects to find the go binary in $PATH.
			Path:  []string{"$WORKDIR/go/bin", "$PATH"},
			Debug: true,
			Args:  st.conf.RunScriptArgs(),
		})
		if err != nil {
			return err
		}
		st.logEventTime("run_done")
	}
	st.logEventTime("done") // TODO(adg): remove this?

	if st.trySet == nil {
		var buildLog string
		if remoteErr != nil {
			buildLog = st.logs()
		}
		if err := recordResult(st.name, remoteErr == nil, st.rev, buildLog, time.Since(execStartTime)); err != nil {
			if remoteErr != nil {
				return fmt.Errorf("Remote error was %q but failed to report it to the dashboard: %v", remoteErr, err)
			}
			return fmt.Errorf("Build succeeded but failed to report it to the dashboard: %v", err)
		}
	}
	if remoteErr != nil {
		return fmt.Errorf("%v failed: %v", lastScript, remoteErr)
	}
	return nil
}

type eventAndTime struct {
	evt string
	t   time.Time
}

// buildStatus is the status of a build.
type buildStatus struct {
	// Immutable:
	builderRev
	conf      dashboard.BuildConfig
	startTime time.Time     // actually time of newBuild (~same thing)
	trySet    *trySet       // or nil
	donec     chan struct{} // closed when done

	mu        sync.Mutex       // guards following
	bc        *buildlet.Client // nil initially, until pool returns one
	done      time.Time        // finished running
	succeeded bool             // set when done
	output    bytes.Buffer     // stdout and stderr
	events    []eventAndTime
	watcher   []*logWatcher
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

func (st *buildStatus) logEventTime(event string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.events = append(st.events, eventAndTime{event, time.Now()})
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

	fmt.Fprintf(&buf, "; <a href='%s'>build log</a>; %s", st.logsURL(), html.EscapeString(st.bc.String()))

	t := st.done
	if t.IsZero() {
		t = st.startTime
	}
	fmt.Fprintf(&buf, ", %v ago\n", time.Since(t))
	st.writeEventsLocked(&buf, true)
	return template.HTML(buf.String())
}

func (st *buildStatus) logsURL() string {
	return fmt.Sprintf("/logs?name=%s&rev=%s&st=%p", st.name, st.rev, st)
}

// st.mu must be held.
func (st *buildStatus) writeEventsLocked(w io.Writer, html bool) {
	for i, evt := range st.events {
		var elapsed string
		if i != 0 {
			elapsed = fmt.Sprintf("+%0.1fs", evt.t.Sub(st.events[i-1].t).Seconds())
		}
		msg := evt.evt
		if msg == "running_exec" && html {
			msg = fmt.Sprintf("<a href='%s'>%s</a>", st.logsURL(), msg)
		}
		fmt.Fprintf(w, " %7s %v %s\n", elapsed, evt.t.Format(time.RFC3339), msg)
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
