// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/ioutil"
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

	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/foreach"
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

func addHealthChecker(hc *healthChecker) {
	if _, dup := healthCheckerByID[hc.ID]; dup {
		panic("duplicate health checker ID " + hc.ID)
	}
	healthCheckers = append(healthCheckers, hc)
	healthCheckerByID[hc.ID] = hc
	http.Handle("/status/"+hc.ID, healthCheckerHandler(hc))
}

// basePinErr is the status of the start-up time basepin disk creation
// in gce.go. It's of type string; nil means no result yet, empty
// string means success, and non-empty means an error.
var basePinErr atomic.Value

func addHealthCheckers(ctx context.Context) {
	addHealthChecker(newMacHealthChecker())
	addHealthChecker(newScalewayHealthChecker())
	addHealthChecker(newPacketHealthChecker())
	addHealthChecker(newOSUPPC64Checker())
	addHealthChecker(newOSUPPC64leChecker())
	addHealthChecker(newJoyentSolarisChecker())
	addHealthChecker(newJoyentIllumosChecker())
	addHealthChecker(newBasepinChecker())
	addHealthChecker(newGitMirrorChecker())
	addHealthChecker(newTipGolangOrgChecker(ctx))
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

var lastGitMirrorErrors atomic.Value // of []string

func monitorGitMirror() {
	for {
		lastGitMirrorErrors.Store(gitMirrorErrors())
		time.Sleep(30 * time.Second)
	}
}

// $1 is repo; $2 is error message
var gitMirrorLineRx = regexp.MustCompile(`/debug/watcher/([\w-]+).?>.+</a> - (.*)`)

func gitMirrorErrors() (errs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequest("GET", "http://gitmirror/", nil)
	req = req.WithContext(ctx)
	res, err := watcherProxy.Transport.RoundTrip(req)
	if err != nil {
		return []string{err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return []string{res.Status}
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
			return []string{fmt.Sprintf("error parsing line %q", line)}
		}
		errs = append(errs, fmt.Sprintf("repo %s: %s", m[1], m[2]))
	}
	if err := bs.Err(); err != nil {
		errs = append(errs, err.Error())
	}
	return errs
}

func newGitMirrorChecker() *healthChecker {
	return &healthChecker{
		ID:     "gitmirror",
		Title:  "Git mirroring",
		DocURL: "https://github.com/golang/build/tree/master/cmd/gitmirror",
		Check: func(w *checkWriter) {
			ee, _ := lastGitMirrorErrors.Load().([]string)
			for _, v := range ee {
				w.error(v)
			}
		},
	}
}

func newTipGolangOrgChecker(ctx context.Context) *healthChecker {
	// tipError is the status of the tip.golang.org website.
	// It's of type string; nil means no result yet, empty
	// string means success, and non-empty means an error.
	var tipError atomic.Value
	go func() {
		for {
			tipError.Store(fetchTipGolangOrgError(ctx))
			time.Sleep(30 * time.Second)
		}
	}()
	return &healthChecker{
		ID:     "tip",
		Title:  "tip.golang.org website",
		DocURL: "https://github.com/golang/build/tree/master/cmd/tip",
		Check: func(w *checkWriter) {
			e, ok := tipError.Load().(string)
			if !ok {
				w.warn("still checking")
			} else if e != "" {
				w.error(e)
			}
		},
	}
}

// fetchTipGolangOrgError fetches the error= value from https://tip.golang.org/_tipstatus.
func fetchTipGolangOrgError(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequest(http.MethodGet, "https://tip.golang.org/_tipstatus", nil)
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.Status
	}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err.Error()
	}
	var e string
	err = foreach.Line(b, func(s []byte) error {
		if !bytes.HasPrefix(s, []byte("error=")) {
			return nil
		}
		e = string(s[len("error="):])
		return errFound
	})
	if err != errFound {
		return "missing error= line"
	} else if e != "<nil>" {
		return "_tipstatus page reports error: " + e
	}
	return ""
}

var errFound = errors.New("error= line was found")

func newMacHealthChecker() *healthChecker {
	var hosts []string
	const numMacHosts = 10 // physical Mac minis, not reverse buildlet connections
	for i := 1; i <= numMacHosts; i++ {
		for _, suf := range []string{"a", "b"} {
			name := fmt.Sprintf("macstadium_host%02d%s", i, suf)
			hosts = append(hosts, name)
		}
	}
	checkHosts := reverseHostChecker(hosts)

	// And check that the makemac daemon is listening.
	var makeMac struct {
		sync.Mutex
		lastCheck  time.Time // currently unused
		lastErrors []string
		lastWarns  []string
	}
	setMakeMacStatus := func(errs, warns []string) {
		makeMac.Lock()
		defer makeMac.Unlock()
		makeMac.lastCheck = time.Now()
		makeMac.lastErrors = errs
		makeMac.lastWarns = warns
	}
	go func() {
		for {
			errs, warns := fetchMakeMacStatus()
			setMakeMacStatus(errs, warns)
			time.Sleep(15 * time.Second)
		}
	}()
	return &healthChecker{
		ID:     "macs",
		Title:  "MacStadium Mac VMs",
		DocURL: "https://github.com/golang/build/tree/master/env/darwin/macstadium",
		Check: func(w *checkWriter) {
			// Check hosts.
			checkHosts(w)
			// Check makemac daemon.
			makeMac.Lock()
			defer makeMac.Unlock()
			for _, v := range makeMac.lastWarns {
				w.warnf("makemac daemon: %v", v)
			}
			for _, v := range makeMac.lastErrors {
				w.errorf("makemac daemon: %v", v)
			}
		},
	}
}

func fetchMakeMacStatus() (errs, warns []string) {
	c := &http.Client{Timeout: 15 * time.Second}
	res, err := c.Get("http://macstadiumd.golang.org:8713")
	if err != nil {
		return []string{fmt.Sprintf("failed to fetch status: %v", err)}, nil
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return []string{fmt.Sprintf("HTTP status %v", res.Status)}, nil
	}
	if res.Header.Get("Content-Type") != "application/json" {
		return []string{fmt.Sprintf("unexpected content-type %q; want JSON", res.Header.Get("Content-Type"))}, nil
	}
	var resj struct {
		Errors   []string
		Warnings []string
	}
	if err := json.NewDecoder(res.Body).Decode(&resj); err != nil {
		return []string{fmt.Sprintf("reading status response body: %v", err)}, nil
	}
	return resj.Errors, resj.Warnings
}

func newJoyentSolarisChecker() *healthChecker {
	return &healthChecker{
		ID:     "joyent-solaris",
		Title:  "Joyent solaris/amd64 machines",
		DocURL: "https://github.com/golang/build/tree/master/env/solaris-amd64/joyent",
		Check:  hostTypeChecker("host-solaris-amd64"),
	}
}

func newJoyentIllumosChecker() *healthChecker {
	return &healthChecker{
		ID:     "joyent-illumos",
		Title:  "Joyent illumos/amd64 machines",
		DocURL: "https://github.com/golang/build/tree/master/env/illumos-amd64-joyent",
		Check:  hostTypeChecker("host-illumos-amd64-joyent"),
	}
}

func hostTypeChecker(hostType string) func(cw *checkWriter) {
	want := expectedHosts(hostType)
	return func(cw *checkWriter) {
		p := reversePool
		p.mu.Lock()
		defer p.mu.Unlock()
		n := 0
		for _, b := range p.buildlets {
			if b.hostType == hostType {
				n++
			}
		}
		if n < want {
			cw.errorf("%d connected; want %d", n, want)
		}
	}
}

func expectedHosts(hostType string) int {
	hc, ok := dashboard.Hosts[hostType]
	if !ok {
		panic(fmt.Sprintf("unknown host type %q", hostType))
	}
	return hc.ExpectNum
}

func newScalewayHealthChecker() *healthChecker {
	var hosts []string
	for i := 1; i <= expectedHosts("host-linux-arm-scaleway"); i++ {
		name := fmt.Sprintf("scaleway-prod-%02d", i)
		hosts = append(hosts, name)
	}
	return &healthChecker{
		ID:     "scaleway",
		Title:  "Scaleway linux/arm machines",
		DocURL: "https://github.com/golang/build/tree/master/env/linux-arm/scaleway",
		Check:  reverseHostChecker(hosts),
	}
}

func newPacketHealthChecker() *healthChecker {
	var hosts []string
	for i := 1; i <= expectedHosts("host-linux-arm64-packet"); i++ {
		name := fmt.Sprintf("packet%02d", i)
		hosts = append(hosts, name)
	}
	return &healthChecker{
		ID:     "packet",
		Title:  "Packet linux/arm64 machines",
		DocURL: "https://github.com/golang/build/tree/master/env/linux-arm64/packet",
		Check:  reverseHostChecker(hosts),
	}
}

func newOSUPPC64Checker() *healthChecker {
	var hosts []string
	for i := 1; i <= expectedHosts("host-linux-ppc64-osu"); i++ {
		name := fmt.Sprintf("go-be-%v", i)
		hosts = append(hosts, name)
	}
	return &healthChecker{
		ID:     "osuppc64",
		Title:  "OSU linux/ppc64 machines",
		DocURL: "https://github.com/golang/build/tree/master/env/linux-ppc64/osuosl",
		Check:  reverseHostChecker(hosts),
	}
}

func newOSUPPC64leChecker() *healthChecker {
	var hosts []string
	for i := 1; i <= expectedHosts("host-linux-ppc64le-osu"); i++ {
		name := fmt.Sprintf("go-le-%v", i)
		hosts = append(hosts, name)
	}
	return &healthChecker{
		ID:     "osuppc64le",
		Title:  "OSU linux/ppc64le machines",
		DocURL: "https://github.com/golang/build/tree/master/env/linux-ppc64le/osuosl",
		Check:  reverseHostChecker(hosts),
	}
}

func reverseHostChecker(hosts []string) func(cw *checkWriter) {
	const recentThreshold = 2 * time.Minute // let VMs be away 2 minutes; assume ~1 minute bootup + slop
	checkStart := time.Now().Add(recentThreshold)

	hostSet := map[string]bool{}
	for _, v := range hosts {
		hostSet[v] = true
	}

	return func(cw *checkWriter) {
		p := reversePool
		p.mu.Lock()
		defer p.mu.Unlock()

		now := time.Now()
		wantGoodSince := now.Add(-recentThreshold)
		numMissing := 0
		numGood := 0
		// Check last good times
		for _, host := range hosts {
			lastGood, ok := p.hostLastGood[host]
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
		for _, b := range p.buildlets {
			if hostSet[b.hostname] {
				count[b.hostname]++
			}
		}
		for name, n := range count {
			if n > 1 {
				cw.errorf("%q is connected from %v machines", name, n)
			}
		}
	}
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
		if atomic.LoadInt32(&st.hasBuildlet) != 0 {
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

	data.RemoteBuildlets = template.HTML(remoteBuildletStatus())

	sort.Sort(byAge(data.Active))
	sort.Sort(byAge(data.Pending))
	sort.Sort(sort.Reverse(byAge(data.Recent)))
	if errTryDeps != nil {
		data.TrybotsErr = errTryDeps.Error()
	} else {
		if buf.Len() == 0 {
			data.Trybots = template.HTML("<i>(none)</i>")
		} else {
			data.Trybots = template.HTML("<pre>" + buf.String() + "</pre>")
		}
	}

	buf.Reset()
	gcePool.WriteHTMLStatus(&buf)
	data.GCEPoolStatus = template.HTML(buf.String())
	buf.Reset()

	kubePool.WriteHTMLStatus(&buf)
	data.KubePoolStatus = template.HTML(buf.String())
	buf.Reset()

	reversePool.WriteHTMLStatus(&buf)
	data.ReversePoolStatus = template.HTML(buf.String())

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
	KubePoolStatus    template.HTML // TODO: embed template
	ReversePoolStatus template.HTML // TODO: embed template
	RemoteBuildlets   template.HTML
	DiskFree          string
	Version           string
	HealthCheckers    []*healthChecker
}

var statusTmpl = template.Must(template.New("status").Parse(`
<!DOCTYPE html>
<html>
<head><link rel="stylesheet" href="/style.css"/><title>Go Farmer</title></head>
<body>
<header>
	<h1>Go Build Coordinator</h1>
	<nav>
		<a href="https://build.golang.org">Dashboard</a>
		<a href="/builders">Builders</a>
	</nav>
	<div class="clear"></div>
</header>

<h2>Running</h2>
<p>{{printf "%d" .Total}} total builds; {{printf "%d" .ActiveBuilds}} active ({{.ActiveReverse}} reverse). Uptime {{printf "%s" .Uptime}}. Version {{.Version}}.

<h2 id=health>Health <a href='#health'>¶</a></h2>
<ul>{{range .HealthCheckers}}
  <li><a href="/status/{{.ID}}">{{.Title}}</a>{{if .DocURL}} [<a href="{{.DocURL}}">docs</a>]{{end -}}: {{with .DoCheck.Out}}
      <ul>
        {{- range .}}
          <li>{{ .AsHTML}}</li>
        {{- end}}
      </ul>
    {{else}}ok{{end}}
  </li>
{{end}}</ul>

<h2 id=trybots>Active Trybot Runs <a href='#trybots'>¶</a></h2>
{{- if .TrybotsErr}}
<b>trybots disabled:</b>: {{.TrybotsErr}}
{{else}}
{{.Trybots}}
{{end}}

<h2 id=remote>Remote buildlets <a href='#remote'>¶</a></h3>
{{.RemoteBuildlets}}

<h2 id=pools>Buildlet pools <a href='#pools'>¶</a></h2>
<ul>
	<li>{{.GCEPoolStatus}}</li>
	<li>{{.KubePoolStatus}}</li>
	<li>{{.ReversePoolStatus}}</li>
</ul>

<h2 id=active>Active builds <a href='#active'>¶</a></h2>
<ul>
	{{range .Active}}
	<li><pre>{{.HTMLStatusLine}}</pre></li>
	{{end}}
</ul>

<h2 id=pending>Pending builds <a href='#pending'>¶</a></h2>
<ul>
	{{range .Pending}}
	<li><pre>{{.HTMLStatusLine}}</pre></li>
	{{end}}
</ul>

<h2 id=completed>Recently completed <a href='#completed'>¶</a></h2>
<ul>
	{{range .Recent}}
	<li><span>{{.HTMLStatusLine_done}}</span></li>
	{{end}}
</ul>

<h2 id=disk>Disk Space <a href='#disk'>¶</a></h2>
<pre>{{.DiskFree}}</pre>

<h2 id=fd>File Descriptors <a href='#fd'>¶</a></h2>
<p>{{.NumFD}}</p>

<h2 id=goroutines>Goroutines <a href='#goroutines'>¶</a></h2>
<p>{{.NumGoroutine}} <a href='/debug/goroutines'>goroutines</a></p>

</body>
</html>
`))

func handleStyleCSS(w http.ResponseWriter, r *http.Request) {
	src := strings.NewReader(styleCSS)
	http.ServeContent(w, r, "style.css", processStartTime, src)
}

const styleCSS = `
body {
	font-family: sans-serif;
	color: #222;
	padding: 10px;
	margin: 0;
}

h1, h2, h1 > a, h2 > a, h1 > a:visited, h2 > a:visited {
	color: #375EAB;
}
h1 { font-size: 24px; }
h2 { font-size: 20px; }

h1 > a, h2 > a {
	display: none;
	text-decoration: none;
}

h1:hover > a, h2:hover > a {
	display: inline;
}

h1 > a:hover, h2 > a:hover {
	text-decoration: underline;
}

pre {
	font-family: monospace;
	font-size: 9pt;
}

header {
	margin: -10px -10px 0 -10px;
	padding: 10px 10px;
	background: #E0EBF5;
}
header a { color: #222; }
header h1 {
	display: inline;
	margin: 0;
	padding-top: 5px;
}
header nav {
	display: inline-block;
	margin-left: 20px;
}
header nav a {
	display: inline-block;
	padding: 10px;
	margin: 0;
	margin-right: 5px;
	color: white;
	background: #375EAB;
	text-decoration: none;
	font-size: 16px;
	border: 1px solid #375EAB;
	border-radius: 5px;
}

table {
	border-collapse: collapse;
	font-size: 9pt;
}

table td, table th, table td, table th {
	text-align: left;
	vertical-align: top;
	padding: 2px 6px;
}

table thead tr {
	background: #fff !important;
}
`
