// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The coordinator runs on GCE and coordinates builds in Docker containers.
package main // import "golang.org/x/build/cmd/coordinator"

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
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
	"golang.org/x/build/types"
	"google.golang.org/cloud/storage"
)

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
)

// LOCK ORDER:
//   statusMu, buildStatus.mu, trySet.mu
// TODO(bradfitz,adg): rewrite the coordinator

var (
	startTime = time.Now()
	donec     = make(chan builderRev) // reports of finished builders (but not try builds)

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

func main() {
	flag.Parse()

	if err := initGCE(); err != nil {
		log.Printf("VM support disabled due to error initializing GCE: %v", err)
	}

	http.HandleFunc("/", handleStatus)
	http.HandleFunc("/try", handleTryStatus)
	http.HandleFunc("/logs", handleLogs)
	http.HandleFunc("/debug/goroutines", handleDebugGoroutines)
	go http.ListenAndServe(":80", nil)

	go cleanUpOldVMs()

	// Start the Docker processes on this host polling Gerrit and
	// pinging build.golang.org when new commits are available.
	startWatchers() // in watcher.go

	workc := make(chan builderRev)
	go findWorkLoop(workc)
	go findTryWorkLoop()
	// TODO(cmang): gccgo will need its own findWorkLoop

	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case work := <-workc:
			log.Printf("workc received %+v; len(status) = %v, cur = %p", work, len(status), status[work])
			if mayBuildRev(work) {
				conf, _ := dashboard.Builders[work.name]
				if st, err := startBuilding(conf, work.rev); err == nil {
					setStatus(work, st)
					go pingDashboard(st)
				} else {
					log.Printf("Error starting to build %v: %v", work, err)
				}
			}
		case done := <-donec:
			log.Printf("%v done", done)
			markDone(done)
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
// It returns true if it's not already building, and there is capacity.
func mayBuildRev(work builderRev) bool {
	statusMu.Lock()
	_, building := status[work]
	statusMu.Unlock()
	return !building
}

func setStatus(work builderRev, st *buildStatus) {
	statusMu.Lock()
	defer statusMu.Unlock()
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

func vmIsBuilding(instName string) bool {
	if instName == "" {
		log.Printf("bogus empty instance name passed to vmIsBuilding")
		return false
	}
	statusMu.Lock()
	defer statusMu.Unlock()
	for _, st := range status {
		if st.instName == instName {
			return true
		}
	}
	return false
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
func (s byAge) Less(i, j int) bool { return s[i].start.Before(s[j].start) }
func (s byAge) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func handleStatus(w http.ResponseWriter, r *http.Request) {
	var active []*buildStatus
	var recent []*buildStatus
	statusMu.Lock()
	for _, st := range status {
		active = append(active, st)
	}
	recent = append(recent, statusDone...)
	numTotal := len(status)

	// TODO: make this prettier.
	var tryBuf bytes.Buffer
	for _, key := range tryList {
		if ts := tries[key]; ts != nil {
			state := ts.state()
			fmt.Fprintf(&tryBuf, "Change-ID: %v Commit: %v\n", key.ChangeID, key.Commit)
			fmt.Fprintf(&tryBuf, "   Remain: %d, fails: %v\n", state.remain, state.failed)
			for _, bs := range ts.builds {
				fmt.Fprintf(&tryBuf, "  %s: running=%v\n", bs.name, bs.isRunning())
			}
		}
	}
	statusMu.Unlock()

	sort.Sort(byAge(active))
	sort.Sort(sort.Reverse(byAge(recent)))

	io.WriteString(w, "<html><body><h1>Go build coordinator</h1>")

	fmt.Fprintf(w, "<h2>running</h2><p>%d total builds active; VM capacity: %d/%d", numTotal, len(vmCap), cap(vmCap))

	io.WriteString(w, "<pre>")
	for _, st := range active {
		io.WriteString(w, st.htmlStatusLine())
	}
	io.WriteString(w, "</pre>")

	io.WriteString(w, "<h2>trybot state</h2><pre>")
	if errTryDeps != nil {
		fmt.Fprintf(w, "<b>trybots disabled: </b>: %v\n", html.EscapeString(errTryDeps.Error()))
	} else {
		w.Write(tryBuf.Bytes())
	}
	io.WriteString(w, "</pre>")

	io.WriteString(w, "<h2>recently completed</h2><pre>")
	for _, st := range recent {
		io.WriteString(w, st.htmlStatusLine())
	}
	io.WriteString(w, "</pre>")

	fmt.Fprintf(w, "<h2>disk space</h2><pre>%s</pre></body></html>", html.EscapeString(diskFree()))
}

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
			bs.htmlStatusLine())
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

func diskFree() string {
	out, _ := exec.Command("df", "-h").Output()
	return string(out)
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
	if st.instName != "" {
		fmt.Fprintf(w, "  vm name: %s\n", st.instName)
	}
	fmt.Fprintf(w, "  started: %v\n", st.start)
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
	res, err := http.Get("https://build.golang.org/?mode=json")
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
			if _, ok := dashboard.Builders[builder]; !ok {
				// Not managed by the coordinator.
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
	for b := range dashboard.Builders {
		if knownToDashboard[b] {
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

	// mu guards state.
	// See LOCK ORDER comment above.
	mu sync.Mutex
	trySetState
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
		ch := make(chan builderRev, 1)
		bs := startBuildingInVM(bconf, key.Commit, ts, ch)
		brev := builderRev{name: bconf.Name, rev: key.Commit}
		status[brev] = bs
		ts.builds[i] = bs
		go ts.awaitTryBuild(i, bconf, bs, ch)
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
func (ts *trySet) awaitTryBuild(idx int, bconf dashboard.BuildConfig, bs *buildStatus, ch chan builderRev) {
	for {
	WaitCh:
		for {
			timeout := time.NewTimer(5 * time.Minute)
			select {
			case <-ch:
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

		// Sleep a bit and retry.
		time.Sleep(30 * time.Second)
		if !ts.wanted() {
			return
		}
		bs = startBuildingInVM(bconf, ts.Commit, ts, ch)
		brev := builderRev{name: bconf.Name, rev: ts.Commit}
		setStatus(brev, bs)
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
	brev := builderRev{name: bconf.Name, rev: ts.Commit}
	markDone(brev)

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
	anyFail := len(ts.failed) > 0
	var failList []string
	if remain == 0 {
		// Copy of it before we unlock mu, even though it's gone down to 0.
		failList = append([]string(nil), ts.failed...)
	}
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
		if err := gerritClient.SetReview(ts.ChangeID, ts.Commit, gerrit.ReviewInput{
			Message: fmt.Sprintf(
				"This change failed on %s:\n"+
					"See https://storage.googleapis.com/%s/%s\n\n"+
					"Consult https://build.golang.org/ to see whether it's a new failure.",
				bs.name, *buildLogBucket, objName),
		}); err != nil {
			log.Printf("Failed to call Gerrit: %v", err)
			return
		}
	}

	if remain == 0 {
		score, msg := 1, "TryBots are happy."
		if anyFail {
			score, msg = -1, fmt.Sprintf("%d of %d TryBots failed: %s", len(failList), len(ts.builds), strings.Join(failList, ", "))
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
}

func startBuilding(conf dashboard.BuildConfig, rev string) (*buildStatus, error) {
	return startBuildingInVM(conf, rev, nil, donec), nil
}

func randHex(n int) string {
	buf := make([]byte, n/2)
	_, err := rand.Read(buf)
	if err != nil {
		panic("Failed to get randomness: " + err.Error())
	}
	return fmt.Sprintf("%x", buf)
}

// startBuildingInVM starts a VM on GCE running the buildlet binary to build rev.
// The ts may be nil if this isn't a try build.
// The done channel always receives exactly 1 value.
// TODO(bradfitz): move this into a buildlet client package.
func startBuildingInVM(conf dashboard.BuildConfig, rev string, ts *trySet, done chan<- builderRev) *buildStatus {
	brev := builderRev{
		name: conf.Name,
		rev:  rev,
	}
	// name is the project-wide unique name of the GCE instance. It can't be longer
	// than 61 bytes, so we only use the first 8 bytes of the rev.
	name := "buildlet-" + conf.Name + "-" + rev[:8] + "-rn" + randHex(6)

	st := &buildStatus{
		builderRev: brev,
		start:      time.Now(),
		instName:   name,
		trySet:     ts,
	}

	go func() {
		err := buildInVM(conf, st)
		if err != nil {
			fmt.Fprintf(st, "\n\nError: %v\n", err)
		}
		st.setDone(err == nil)
		done <- builderRev{conf.Name, rev}
	}()
	return st
}

// We artifically limit ourselves to 60 VMs right now, assuming that
// each takes 2 CPU, and we have a current quota of 200 CPUs. That
// gives us headroom, but also doesn't account for SSD or memory
// quota.
// TODO(bradfitz): better quota system.
const maxVMs = 60

var vmCap = make(chan bool, maxVMs)

func awaitVMCountQuota() { vmCap <- true }
func putVMCountQuota()   { <-vmCap }

func buildInVM(conf dashboard.BuildConfig, st *buildStatus) (retErr error) {
	st.logEventTime("awaiting_gce_quota")
	awaitVMCountQuota()
	defer putVMCountQuota()

	needDelete := false
	defer func() {
		if !needDelete {
			return
		}
		deleteVM(projectZone, st.instName)
	}()

	st.logEventTime("creating_instance")
	log.Printf("Creating VM for %s, %s", conf.Name, st.rev)
	bc, err := buildlet.StartNewVM(tokenSource, st.instName, conf.Name, buildlet.VMOpts{
		ProjectID:   projectID,
		Zone:        projectZone,
		Description: fmt.Sprintf("Go Builder building %s %s", conf.Name, st.rev),
		DeleteIn:    vmDeleteTimeout,
		OnInstanceRequested: func() {
			needDelete = true
			st.logEventTime("instance_create_requested")
			log.Printf("%v now booting VM %v for build", st.builderRev, st.instName)
		},
		OnInstanceCreated: func() {
			st.logEventTime("instance_created")
			needDelete = true // redundant with OnInstanceRequested one, but fine.
		},
		OnGotInstanceInfo: func() {
			st.logEventTime("waiting_for_buildlet")
		},
	})
	if err != nil {
		log.Printf("Failed to create VM for %s, %s: %v", conf.Name, st.rev, err)
		return err
	}
	st.logEventTime("buildlet_up")
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
	if conf.Go14URL != "" {
		grp.Go(func() error {
			st.logEventTime("start_write_go14_tar")
			if err := bc.PutTarFromURL(conf.Go14URL, "go1.4"); err != nil {
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

	remoteErr, err := bc.Exec(path.Join("go", conf.AllScript()), buildlet.ExecOpts{
		Output:      st,
		OnStartExec: func() { st.logEventTime("running_exec") },
		ExtraEnv:    conf.Env(),
		Debug:       true,
	})
	if err != nil {
		return err
	}
	st.logEventTime("done")
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
		return fmt.Errorf("%s failed: %v", conf.AllScript(), remoteErr)
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
	start    time.Time
	trySet   *trySet // or nil
	instName string  // VM instance name

	mu        sync.Mutex   // guards following
	done      time.Time    // finished running
	succeeded bool         // set when done
	output    bytes.Buffer // stdout and stderr
	events    []eventAndTime
	watcher   []*logWatcher
}

func (st *buildStatus) setDone(succeeded bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.succeeded = succeeded
	st.done = time.Now()
	st.notifyWatchersLocked(true)
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

// htmlStatusLine returns the HTML to show within the <pre> block on
// the main page's list of active builds.
func (st *buildStatus) htmlStatusLine() string {
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

	fmt.Fprintf(&buf, " in VM <a href='%s'>%s</a>", st.logsURL(), st.instName)

	t := st.done
	if t.IsZero() {
		t = st.start
	}
	fmt.Fprintf(&buf, ", %v ago\n", time.Since(t))
	st.writeEventsLocked(&buf, true)
	return buf.String()
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

// check is only for things which should be impossible (not even rare)
// to fail.
func check(err error) {
	if err != nil {
		panic("previously assumed to never fail: " + err.Error())
	}
}
