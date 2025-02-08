// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v48/github"
	"go.opencensus.io/stats"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/coordinator/remote"
	"golang.org/x/build/internal/coordinator/schedule"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/kubernetes"
	"golang.org/x/build/kubernetes/api"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
)

// status
type statusLevel int

const (
	// levelInfo is an informational text that's not an error,
	// such as "coordinator just started recently, waiting to
	// start health check"
	levelInfo statusLevel = iota
	// levelWarn is a non-critical error, such as "missing 1 of 50
	// of ARM machines"
	levelWarn
	// levelError is something that should be fixed sooner, such
	// as "all Macs are gone".
	levelError
)

func (l statusLevel) String() string {
	switch l {
	case levelInfo:
		return "Info"
	case levelWarn:
		return "Warn"
	case levelError:
		return "Error"
	}
	return ""
}

type levelText struct {
	Level statusLevel
	Text  string
}

func (lt levelText) AsHTML() template.HTML {
	switch lt.Level {
	case levelInfo:
		return template.HTML(html.EscapeString(lt.Text))
	case levelWarn:
		return template.HTML(fmt.Sprintf("<span style='color: orange'>%s</span>", html.EscapeString(lt.Text)))
	case levelError:
		return template.HTML(fmt.Sprintf("<span style='color: red'><b>%s</b></span>", html.EscapeString(lt.Text)))
	}
	return ""
}

type checkWriter struct {
	Out []levelText
}

func (w *checkWriter) error(s string)                       { w.Out = append(w.Out, levelText{levelError, s}) }
func (w *checkWriter) errorf(a string, args ...interface{}) { w.error(fmt.Sprintf(a, args...)) }
func (w *checkWriter) info(s string)                        { w.Out = append(w.Out, levelText{levelInfo, s}) }
func (w *checkWriter) infof(a string, args ...interface{})  { w.info(fmt.Sprintf(a, args...)) }
func (w *checkWriter) warn(s string)                        { w.Out = append(w.Out, levelText{levelWarn, s}) }
func (w *checkWriter) warnf(a string, args ...interface{})  { w.warn(fmt.Sprintf(a, args...)) }
func (w *checkWriter) hasErrors() bool {
	for _, v := range w.Out {
		if v.Level == levelError {
			return true
		}
	}
	return false
}

type healthChecker struct {
	ID     string
	Title  string
	DocURL string

	// Check writes the health check status to a checkWriter.
	//
	// It's called when rendering the HTML page, so expensive
	// operations (network calls, etc.) should be done in a
	// separate goroutine and Check should report their results.
	Check func(*checkWriter)
}

func (hc *healthChecker) DoCheck() *checkWriter {
	cw := new(checkWriter)
	hc.Check(cw)
	return cw
}

var (
	healthCheckers    []*healthChecker
	healthCheckerByID = map[string]*healthChecker{}
)

func addHealthChecker(mux *http.ServeMux, hc *healthChecker) {
	if _, dup := healthCheckerByID[hc.ID]; dup {
		panic("duplicate health checker ID " + hc.ID)
	}
	healthCheckers = append(healthCheckers, hc)
	healthCheckerByID[hc.ID] = hc
	mux.Handle("/status/"+hc.ID, healthCheckerHandler(hc))
}

// basePinErr is the status of the start-up time basepin disk creation
// in gce.go. It's of type string; no value means no result yet,
// empty string means success, and non-empty means an error.
var basePinErr atomic.Value

func addHealthCheckers(ctx context.Context, mux *http.ServeMux, sc *secret.Client) {
	addHealthChecker(mux, newBasepinChecker())
	addHealthChecker(mux, newGitMirrorChecker())
	addHealthChecker(mux, newGitHubAPIChecker(ctx, sc))
}

func newBasepinChecker() *healthChecker {
	return &healthChecker{
		ID:     "basepin",
		Title:  "VM snapshots",
		DocURL: "https://golang.org/issue/21305",
		Check: func(w *checkWriter) {
			v := basePinErr.Load()
			if v == nil {
				w.warnf("still running")
				return
			}
			if v == "" {
				return
			}
			w.error(v.(string))
		},
	}
}

// gitMirrorStatus is the latest known status of the gitmirror service.
var gitMirrorStatus = struct {
	sync.Mutex
	Errors   []string
	Warnings []string
}{Warnings: []string{"still checking"}}

func monitorGitMirror(kcl *kubernetes.Client) {
	for {
		errs, warns := gitMirrorErrors(kcl)
		gitMirrorStatus.Lock()
		gitMirrorStatus.Errors, gitMirrorStatus.Warnings = errs, warns
		gitMirrorStatus.Unlock()
		time.Sleep(30 * time.Second)
	}
}

// gitMirrorErrors queries the status pages of all
// running gitmirror instances and reports errors.
func gitMirrorErrors(kcl *kubernetes.Client) (errs, warns []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pods, err := kcl.GetPods(ctx)
	if err != nil {
		log.Println("gitMirrorErrors: goKubeClient.GetPods:", err)
		return []string{"failed to get pods; can't query gitmirror status"}, nil
	}
	var runningGitMirror []api.Pod
	for _, p := range pods {
		if !strings.HasPrefix(p.Labels["app"], "gitmirror") || p.Status.Phase != "Running" {
			continue
		}
		runningGitMirror = append(runningGitMirror, p)
	}
	if len(runningGitMirror) == 0 {
		return []string{"no running gitmirror instances"}, nil
	}
	for _, pod := range runningGitMirror {
		// The gitmirror -http=:8585 status page URL is hardcoded here.
		// If the ReplicationController configuration changes (rare), this
		// health check will begin to fail until it's updated accordingly.
		instErrs, instWarns := gitMirrorInstanceErrors(ctx, fmt.Sprintf("http://%s:8585/", pod.Status.PodIP))
		for _, err := range instErrs {
			errs = append(errs, fmt.Sprintf("instance %s: %s", pod.Name, err))
		}
		for _, warn := range instWarns {
			warns = append(warns, fmt.Sprintf("instance %s: %s", pod.Name, warn))
		}
	}
	return errs, warns
}

func gitMirrorInstanceErrors(ctx context.Context, url string) (errs, warns []string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{err.Error()}, nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return []string{res.Status}, nil
	}
	// TODO: add a JSON mode to gitmirror so we don't need to parse HTML.
	// This works for now. We control its output.
	bs := bufio.NewScanner(res.Body)
	for bs.Scan() {
		// Lines look like:
		//    <html><body><pre><a href='/debug/watcher/arch'>arch</a> - ok
		// or:
		//    <a href='/debug/watcher/arch'>arch</a> - ok
		// (See https://farmer.golang.org/debug/watcher/)
		line := bs.Text()
		if strings.HasSuffix(line, " - ok") {
			continue
		}
		m := gitMirrorLineRx.FindStringSubmatch(line)
		if len(m) != 3 {
			if strings.Contains(line, "</html>") {
				break
			}
			return []string{fmt.Sprintf("error parsing line %q", line)}, nil
		}
		if strings.HasPrefix(m[2], "ok; ") {
			// If the status begins with "ok", it can't be that bad.
			warns = append(warns, fmt.Sprintf("repo %s: %s", m[1], m[2]))
			continue
		}
		errs = append(errs, fmt.Sprintf("repo %s: %s", m[1], m[2]))
	}
	if err := bs.Err(); err != nil {
		errs = append(errs, err.Error())
	}
	return errs, warns
}

// $1 is repo; $2 is error message
var gitMirrorLineRx = regexp.MustCompile(`/debug/watcher/([\w-]+).?>.+</a> - (.*)`)

func newGitMirrorChecker() *healthChecker {
	return &healthChecker{
		ID:     "gitmirror",
		Title:  "Git mirroring",
		DocURL: "https://github.com/golang/build/tree/master/cmd/gitmirror",
		Check: func(w *checkWriter) {
			gitMirrorStatus.Lock()
			errs, warns := gitMirrorStatus.Errors, gitMirrorStatus.Warnings
			gitMirrorStatus.Unlock()
			for _, v := range errs {
				w.error(v)
			}
			for _, v := range warns {
				w.warn(v)
			}
		},
	}
}

func newMacOSARM64Checker() *healthChecker {
	var expect int // Number of expected darwin/arm64 reverse builders based on x/build/dashboard.
	for hostType, hc := range dashboard.Hosts {
		if !strings.HasPrefix(hostType, "host-darwin-arm64-") || !hc.IsReverse {
			continue
		}
		expect += hc.ExpectNum
	}
	var hosts []string
	for i := 1; i <= expect; i++ {
		hosts = append(hosts, fmt.Sprintf("fishbowl-%02d.local", i))
	}
	return &healthChecker{
		ID:     "macos-arm64",
		Title:  "macOS ARM64 (M1 Mac minis)",
		DocURL: "https://golang.org/issue/39782",
		Check:  reverseHostChecker(hosts),
	}
}

func expectedHosts(hostType string) int {
	hc, ok := dashboard.Hosts[hostType]
	if !ok {
		panic(fmt.Sprintf("unknown host type %q", hostType))
	}
	return hc.ExpectNum
}

func reverseHostChecker(hosts []string) func(cw *checkWriter) {
	const recentThreshold = 2 * time.Minute // let VMs be away 2 minutes; assume ~1 minute bootup + slop
	checkStart := time.Now().Add(recentThreshold)

	hostSet := map[string]bool{}
	for _, v := range hosts {
		hostSet[v] = true
	}

	// TODO(amedee): rethink how this is implemented. It has been
	// modified due to golang.org/issues/36841
	// instead of a single lock being held while all of the
	// operations are performed, there is now a lock held
	// during each BuildletLastSeen call and again when
	// the buildlet host names are retrieved.
	return func(cw *checkWriter) {
		p := pool.ReversePool()

		now := time.Now()
		wantGoodSince := now.Add(-recentThreshold)
		numMissing := 0
		numGood := 0
		// Check last good times
		for _, host := range hosts {
			lastGood, ok := p.BuildletLastSeen(host)
			if ok && lastGood.After(wantGoodSince) {
				numGood++
				continue
			}
			if now.Before(checkStart) {
				cw.infof("%s not yet connected", host)
				continue
			}
			if ok {
				cw.warnf("%s missing, not seen for %v", host, time.Now().Sub(lastGood).Round(time.Second))
			} else {
				cw.warnf("%s missing, never seen (at least %v)", host, uptime())
			}
			numMissing++
		}
		if numMissing > 0 {
			sum := numMissing + numGood
			percentMissing := float64(numMissing) / float64(sum)
			msg := fmt.Sprintf("%d machines missing, %.0f%% of capacity", numMissing, percentMissing*100)
			if percentMissing >= 0.15 {
				cw.error(msg)
			} else {
				cw.warn(msg)
			}
		}

		// And check that we don't have more than 1
		// connected of any type.
		count := map[string]int{}
		for _, hostname := range p.BuildletHostnames() {
			if hostSet[hostname] {
				count[hostname]++
			}
		}
		for name, n := range count {
			if n > 1 {
				cw.errorf("%q is connected from %v machines", name, n)
			}
		}
	}
}

// newGitHubAPIChecker creates a GitHub API health checker
// that queries the remaining rate limit at regular invervals
// and reports when the hourly quota has been exceeded.
//
// It also records metrics to track remaining rate limit over time.
func newGitHubAPIChecker(ctx context.Context, sc *secret.Client) *healthChecker {
	// githubRate is the status of the GitHub API v3 client.
	// It's of type *github.Rate; no value means no result yet,
	// nil value means no recent result.
	var githubRate atomic.Value

	hc := &healthChecker{
		ID:     "githubapi",
		Title:  "GitHub API Rate Limit",
		DocURL: "https://golang.org/issue/44406",
		Check: func(w *checkWriter) {
			rate, ok := githubRate.Load().(*github.Rate)
			if !ok {
				w.warn("still checking")
			} else if rate == nil {
				w.warn("no recent result")
			} else if rate.Remaining == 0 {
				resetIn := "a minute or so"
				if t := time.Until(rate.Reset.Time); t > time.Minute {
					resetIn = t.Round(time.Second).String()
				}
				w.warnf("hourly GitHub API rate limit exceeded; reset in %s", resetIn)
			}
		},
	}

	// Start measuring and reporting the remaining GitHub API v3 rate limit.
	if sc == nil {
		hc.Check = func(w *checkWriter) {
			w.info("check disabled; credentials were not provided")
		}
		return hc
	}
	token, err := sc.Retrieve(ctx, secret.NameMaintnerGitHubToken)
	if err != nil {
		log.Printf("newGitHubAPIChecker: sc.Retrieve(_, %q) failed, err = %v\n", secret.NameMaintnerGitHubToken, err)
		hc.Check = func(w *checkWriter) {
			// The check is displayed publicly, so don't include details from err.
			w.error("failed to retrieve API token")
		}
		return hc
	}
	gh := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})))
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			// Fetch the current rate limit from the GitHub API.
			// This endpoint is special in that it doesn't consume rate limit quota itself.
			var rate *github.Rate
			rateLimitsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			rl, _, err := gh.RateLimits(rateLimitsCtx)
			cancel()
			if rle := (*github.RateLimitError)(nil); errors.As(err, &rle) {
				rate = &rle.Rate
			} else if err != nil {
				log.Println("GitHubAPIChecker: github.RateLimits:", err)
			} else {
				rate = rl.GetCore()
			}

			// Store the result of fetching, and record the current rate limit, if any.
			githubRate.Store(rate)
			if rate != nil {
				stats.Record(ctx, mGitHubAPIRemaining.M(int64(rate.Remaining)))
			}

			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
	}()

	return hc
}

func healthCheckerHandler(hc *healthChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := new(checkWriter)
		hc.Check(cw)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if cw.hasErrors() {
			w.WriteHeader(500)
		} else {
			w.WriteHeader(200)
		}
		if len(cw.Out) == 0 {
			io.WriteString(w, "ok\n")
			return
		}
		fmt.Fprintf(w, "# %q status: %s\n", hc.ID, hc.Title)
		if hc.DocURL != "" {
			fmt.Fprintf(w, "# Notes: %v\n", hc.DocURL)
		}
		for _, v := range cw.Out {
			fmt.Fprintf(w, "%s: %s\n", v.Level, v.Text)
		}
	})
}

func uptime() time.Duration { return time.Since(processStartTime).Round(time.Second) }

// grpcHandlerFunc creates handler which intercepts requests intended for a GRPC server and directs the calls to the server.
// All other requests are directed toward the passed in handler.
func grpcHandlerFunc(gs *grpc.Server, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gs.ServeHTTP(w, r)
			return
		}
		h(w, r)
	}
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	df := diskFree()

	statusMu.Lock()
	data := statusData{
		Total:          len(status),
		Uptime:         uptime(),
		Recent:         append([]*buildStatus{}, statusDone...),
		DiskFree:       df,
		Version:        Version,
		NumFD:          fdCount(),
		NumGoroutine:   runtime.NumGoroutine(),
		HealthCheckers: healthCheckers,
	}
	for _, st := range status {
		if st.HasBuildlet() {
			data.ActiveBuilds++
			data.Active = append(data.Active, st)
			if st.conf.IsReverse() {
				data.ActiveReverse++
			}
		} else {
			data.Pending = append(data.Pending, st)
		}
	}
	// TODO: make this prettier.
	var buf bytes.Buffer
	for _, key := range tryList {
		if ts := tries[key]; ts != nil {
			state := ts.state()
			fmt.Fprintf(&buf, "Change-ID: %v Commit: %v (<a href='/try?commit=%v'>status</a>)\n",
				key.ChangeTriple(), key.Commit, key.Commit[:8])
			fmt.Fprintf(&buf, "   Remain: %d, fails: %v\n", state.remain, state.failed)
			for _, bs := range ts.builds {
				fmt.Fprintf(&buf, "  %s: running=%v\n", bs.Name, bs.isRunning())
			}
		}
	}
	statusMu.Unlock()

	gce := pool.NewGCEConfiguration()
	data.GomoteInstances = remoteSessionStatus()

	sort.Sort(byAge(data.Active))
	sort.Sort(byAge(data.Pending))
	sort.Sort(sort.Reverse(byAge(data.Recent)))
	if gce.TryDepsErr() != nil {
		data.TrybotsErr = gce.TryDepsErr().Error()
	} else {
		if buf.Len() == 0 {
			data.Trybots = template.HTML("<i>(none)</i>")
		} else {
			data.Trybots = template.HTML("<pre>" + buf.String() + "</pre>")
		}
	}

	buf.Reset()
	gce.BuildletPool().WriteHTMLStatus(&buf)
	data.GCEPoolStatus = template.HTML(buf.String())
	buf.Reset()

	buf.Reset()
	pool.EC2BuildetPool().WriteHTMLStatus(&buf)
	data.EC2PoolStatus = template.HTML(buf.String())
	buf.Reset()

	pool.ReversePool().WriteHTMLStatus(&buf)
	data.ReversePoolStatus = template.HTML(buf.String())

	data.SchedState = sched.State()

	buf.Reset()
	if err := statusTmpl.Execute(&buf, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

func fdCount() int {
	f, err := os.Open("/proc/self/fd")
	if err != nil {
		return -1
	}
	defer f.Close()
	n := 0
	for {
		names, err := f.Readdirnames(1000)
		n += len(names)
		if err == io.EOF {
			return n
		}
		if err != nil {
			return -1
		}
	}
}

func friendlyDuration(d time.Duration) string {
	if d > 10*time.Second {
		d2 := ((d + 50*time.Millisecond) / (100 * time.Millisecond)) * (100 * time.Millisecond)
		return d2.String()
	}
	if d > time.Second {
		d2 := ((d + 5*time.Millisecond) / (10 * time.Millisecond)) * (10 * time.Millisecond)
		return d2.String()
	}
	d2 := ((d + 50*time.Microsecond) / (100 * time.Microsecond)) * (100 * time.Microsecond)
	return d2.String()
}

func diskFree() string {
	out, _ := exec.Command("df", "-h").Output()
	return string(out)
}

// statusData is the data that fills out statusTmpl.
type statusData struct {
	Total             int // number of total builds (including those waiting for a buildlet)
	ActiveBuilds      int // number of running builds (subset of Total with a buildlet)
	ActiveReverse     int // subset of ActiveBuilds that are reverse buildlets
	NumFD             int
	NumGoroutine      int
	Uptime            time.Duration
	Active            []*buildStatus // have a buildlet
	Pending           []*buildStatus // waiting on a buildlet
	Recent            []*buildStatus
	TrybotsErr        string
	Trybots           template.HTML
	GCEPoolStatus     template.HTML // TODO: embed template
	EC2PoolStatus     template.HTML // TODO: embed template
	ReversePoolStatus template.HTML // TODO: embed template
	GomoteInstances   template.HTML
	SchedState        schedule.SchedulerState
	DiskFree          string
	Version           string
	HealthCheckers    []*healthChecker
}

//go:embed templates/base.html
var baseTmplStr string

// baseTmpl defines common templates for reuse in other coordinator templates.
var baseTmpl = template.Must(template.New("").Parse(baseTmplStr))

//go:embed templates/status.html
var statusTmplStr string

var statusTmpl = template.Must(baseTmpl.New("status").Parse(statusTmplStr))

//go:embed style.css
var styleCSS []byte

func handleStyleCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, private, max-age=0")
	http.ServeContent(w, r, "style.css", processStartTime, bytes.NewReader(styleCSS))
}

// statusSessionPool to be used exclusively in the status file.
var statusSessionPool *remote.SessionPool

// setSessionPool sets the session pool for use in the status file.
func setSessionPool(sp *remote.SessionPool) {
	statusSessionPool = sp
}

// remoteSessionStatus creates the status HTML for the sessions in the session pool.
func remoteSessionStatus() template.HTML {
	sessions := statusSessionPool.List()
	if len(sessions) == 0 {
		return "<i>(none)</i>"
	}
	var buf bytes.Buffer
	buf.WriteString("<ul>")
	for _, s := range sessions {
		fmt.Fprintf(&buf, "<li><b>%s</b>, created %v ago, expires in %v</li>\n",
			html.EscapeString(s.ID),
			time.Since(s.Created), time.Until(s.Expires))
	}
	buf.WriteString("</ul>")
	return template.HTML(buf.String())
}
