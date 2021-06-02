// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gitmirror binary watches the specified Gerrit repositories for
// new commits and syncs them to mirror repositories.
//
// It also serves tarballs over HTTP for the build system.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/gitauth"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	repospkg "golang.org/x/build/repos"
	"golang.org/x/sync/errgroup"
)

const (
	goBase = "https://go.googlesource.com/"
)

var (
	flagHTTPAddr     = flag.String("http", "", "If non-empty, the listen address to run an HTTP server on")
	flagCacheDir     = flag.String("cachedir", "", "git cache directory. If empty a temp directory is made.")
	flagPollInterval = flag.Duration("poll", 60*time.Second, "Remote repo poll interval")
	flagMirror       = flag.Bool("mirror", false, "whether to mirror to mirror repos; if disabled, it only runs in HTTP archive server mode")
)

func main() {
	flag.Parse()

	if *flagHTTPAddr != "" {
		go func() {
			err := http.ListenAndServe(*flagHTTPAddr, nil)
			log.Fatalf("http server failed: %v", err)
		}()
	}
	http.HandleFunc("/debug/env", handleDebugEnv)
	http.HandleFunc("/debug/goroutines", handleDebugGoroutines)

	if err := gitauth.Init(); err != nil {
		log.Fatalf("gitauth: %v", err)
	}

	cacheDir, err := createCacheDir()
	if err != nil {
		log.Fatalf("creating cache dir: %v", err)
	}

	m := &mirror{
		mux:          http.DefaultServeMux,
		repos:        map[string]*repo{},
		cacheDir:     cacheDir,
		gerritClient: gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth),
	}
	http.HandleFunc("/", m.handleRoot)

	var eg errgroup.Group
	for name := range repospkg.ByGerritProject {
		r := m.addRepo(name)
		eg.Go(r.init)
	}
	if err := eg.Wait(); err != nil {
		log.Fatalf("initializing repos: %v", err)
	}

	if *flagMirror {
		if err := writeCredentials(); err != nil {
			log.Fatalf("writing ssh credentials: %v", err)
		}
		if err := m.runMirrors(); err != nil {
			log.Fatalf("running mirror: %v", err)
		}
	}

	for _, repo := range m.repos {
		go repo.Loop()
	}
	go m.pollGerritAndTickle()
	go m.subscribeToMaintnerAndTickleLoop()

	select {}
}

func writeCredentials() error {
	sc := secret.MustNewClient()
	defer sc.Close()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	sshDir := filepath.Join(home, ".ssh")
	sshKey := filepath.Join(sshDir, "id_ed25519")
	if _, err := os.Stat(sshKey); err == nil {
		log.Printf("Using github ssh key at %v", sshKey)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	privKey, err := sc.Retrieve(ctx, secret.NameGitHubSSHKey)
	if err != nil || len(privKey) == 0 {
		return fmt.Errorf("can't mirror to github without %q GCP secret manager or file %v", secret.NameGitHubSSHKey, sshKey)
	}
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}
	if err := ioutil.WriteFile(sshKey, []byte(privKey+"\n"), 0600); err != nil {
		return err
	}
	log.Printf("Wrote %s from GCP secret manager.", sshKey)
	return nil
}

func createCacheDir() (string, error) {
	if *flagCacheDir == "" {
		dir, err := ioutil.TempDir("", "gitmirror")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		return dir, nil
	}

	fi, err := os.Stat(*flagCacheDir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(*flagCacheDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create watcher's git cache dir: %v", err)
		}
	} else {
		if err != nil {
			return "", fmt.Errorf("invalid -cachedir: %v", err)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("invalid -cachedir=%q; not a directory", *flagCacheDir)
		}
	}
	return *flagCacheDir, nil
}

// A mirror watches Gerrit repositories, fetching the latest commits and
// optionally mirroring them.
type mirror struct {
	mux          *http.ServeMux
	repos        map[string]*repo
	cacheDir     string
	gerritClient *gerrit.Client
}

func (m *mirror) addRepo(name string) *repo {
	r := &repo{
		name:    name,
		url:     goBase + name,
		root:    filepath.Join(m.cacheDir, name),
		changed: make(chan bool, 1),
		mirror:  m,
	}
	m.mux.Handle("/"+name+".tar.gz", r)
	m.mux.Handle("/debug/watcher/"+r.name, r)
	m.repos[name] = r
	return r
}

// runMirrors sets up and starts mirroring for the repositories that are
// configured to be mirrored.
func (m *mirror) runMirrors() error {
	for name, repo := range m.repos {
		meta, ok := repospkg.ByGerritProject[name]
		if !ok || !meta.MirrorToGitHub {
			continue
		}
		if err := repo.addRemote("github", "git@github.com:"+meta.GitHubRepo()+".git",
			// We want to include only the refs/heads/* and refs/tags/* namespaces
			// in the mirrors. They correspond to published branches and tags.
			// Leave out internal Gerrit namespaces such as refs/changes/*,
			// refs/users/*, etc., because they're not helpful on other hosts.
			"push = +refs/heads/*:refs/heads/*",
			"push = +refs/tags/*:refs/tags/*",
		); err != nil {
			return fmt.Errorf("adding remote: %v", err)
		}
	}
	return nil
}

// GET /
// or:
// GET /debug/watcher/
func (m *mirror) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/debug/watcher/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, "<html><body><pre>")
	var names []string
	for name := range m.repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "<a href='/debug/watcher/%s'>%s</a> - %s\n", name, name, m.repos[name].statusLine())
	}
	fmt.Fprint(w, "</pre></body></html>")
}

// a statusEntry is a status string at a specific time.
type statusEntry struct {
	status string
	t      time.Time
}

// statusRing is a ring buffer of timestamped status messages.
type statusRing struct {
	mu   sync.Mutex      // guards rest
	head int             // next position to fill
	ent  [50]statusEntry // ring buffer of entries; zero time means unpopulated
}

func (r *statusRing) add(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.ent[r.head] = statusEntry{status, time.Now()}
	r.head++
	if r.head == len(r.ent) {
		r.head = 0
	}
}

func (r *statusRing) foreachDesc(fn func(statusEntry)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	i := r.head
	for {
		i--
		if i < 0 {
			i = len(r.ent) - 1
		}
		if i == r.head || r.ent[i].t.IsZero() {
			return
		}
		fn(r.ent[i])
	}
}

// repo represents a repository to be watched.
type repo struct {
	name    string
	url     string
	root    string    // on-disk location of the git repo, *cacheDir/name
	changed chan bool // sent to when a change comes in
	status  statusRing
	dests   []string // destination remotes to mirror to
	mirror  *mirror

	mu        sync.Mutex
	err       error
	firstBad  time.Time
	lastBad   time.Time
	firstGood time.Time
	lastGood  time.Time
}

// init sets up the repo, cloning the repository to the local root.
func (r *repo) init() error {
	canReuse := true
	if _, err := os.Stat(filepath.Join(r.root, "FETCH_HEAD")); err != nil {
		canReuse = false
		r.logf("can't reuse git dir, no FETCH_HEAD: %v", err)
	}
	if canReuse {
		r.setStatus("reusing git dir; running git fetch")
		_, _, err := r.runGitLogged("fetch", "--prune", "origin")
		if err != nil {
			canReuse = false
			r.logf("git fetch failed; proceeding to wipe + clone instead")
		}
	}
	if !canReuse {
		r.setStatus("need clone; removing cache root")
		os.RemoveAll(r.root)
		_, _, err := r.runGitLogged("clone", "--mirror", r.url, r.root)
		if err != nil {
			return fmt.Errorf("cloning %s: %v", r.url, err)
		}
		r.setStatus("cloned")
	}
	return nil
}

func (r *repo) runGitLogged(args ...string) ([]byte, []byte, error) {
	start := time.Now()
	r.logf("running git %s", args)
	stdout, stderr, err := r.runGitQuiet(args...)
	if err == nil {
		r.logf("ran git %s in %v", args, time.Since(start))
	} else {
		r.logf("git %s failed after %v: %v\n%v", args, time.Since(start), err, string(stderr))
	}
	return stdout, stderr, err
}

func (r *repo) runGitQuiet(args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.root
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func (r *repo) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change := (r.err != nil) != (err != nil)
	now := time.Now()
	if err != nil {
		if change {
			r.firstBad = now
		}
		r.lastBad = now
	} else {
		if change {
			r.firstGood = now
		}
		r.lastGood = now
	}
	r.err = err
}

var startTime = time.Now()

func (r *repo) statusLine() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.lastGood.IsZero() {
		if r.err != nil {
			return fmt.Sprintf("broken; permanently? always failing, for %v", time.Since(r.firstBad))
		}
		if time.Since(startTime) < 5*time.Minute {
			return "ok; starting up, no report yet"
		}
		return fmt.Sprintf("hung; hang at start-up? no report since start %v ago", time.Since(startTime))
	}
	if r.err == nil {
		if sinceGood := time.Since(r.lastGood); sinceGood > 6*time.Minute {
			return fmt.Sprintf("hung? no activity since last success %v ago", sinceGood)
		}
		if r.lastBad.After(time.Now().Add(-1 * time.Hour)) {
			return fmt.Sprintf("ok; recent failure %v ago", time.Since(r.lastBad))
		}
		return "ok"
	}
	return fmt.Sprintf("broken for %v", time.Since(r.lastGood))
}

func (r *repo) setStatus(status string) {
	r.status.add(status)
}

func (r *repo) addRemote(name, url string, opts ...string) error {
	r.dests = append(r.dests, name)
	if _, _, err := r.runGitQuiet("remote", "remove", name); err != nil {
		// Exit status 2 means not found, which is fine.
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 {
			return err
		}
	}
	gitConfig := filepath.Join(r.root, "config")
	f, err := os.OpenFile(gitConfig, os.O_WRONLY|os.O_APPEND, os.ModePerm)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "\n[remote %q]\n\turl = %v\n", name, url)
	if err != nil {
		f.Close()
		return err
	}
	for _, o := range opts {
		_, err := fmt.Fprintf(f, "\t%s\n", o)
		if err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// Loop continuously runs "git fetch" in the repo, checks for new
// commits and mirrors commits to a destination repo (if enabled).
func (r *repo) Loop() {
outer:
	for {
		if err := r.fetch(); err != nil {
			r.logf("fetch failed in repo loop: %v", err)
			r.setErr(err)
			time.Sleep(10 * time.Second)
			continue
		}
		for _, dest := range r.dests {
			if err := r.push(dest); err != nil {
				r.logf("push failed in repo loop: %v", err)
				r.setErr(err)
				time.Sleep(10 * time.Second)
				continue outer
			}
		}

		r.setErr(nil)
		r.setStatus("waiting")
		// We still run a timer but a very slow one, just
		// in case the mechanism updating the repo tickler
		// breaks for some reason.
		timer := time.NewTimer(5 * time.Minute)
		select {
		case <-r.changed:
			r.setStatus("got update tickle")
			timer.Stop()
		case <-timer.C:
			r.setStatus("poll timer fired")
		}
	}
}

func (r *repo) logf(format string, args ...interface{}) {
	log.Printf(r.name+": "+format, args...)
}

// fetch runs "git fetch" in the repository root.
// It tries three times, just in case it failed because of a transient error.
func (r *repo) fetch() error {
	err := try(3, func(attempt int) error {
		r.setStatus(fmt.Sprintf("running git fetch origin, attempt %d", attempt))
		if _, stderr, err := r.runGitLogged("fetch", "--prune", "origin"); err != nil {
			return fmt.Errorf("%v\n\n%s", err, stderr)
		}
		return nil
	})
	if err != nil {
		r.setStatus("git fetch failed")
	} else {
		r.setStatus("ran git fetch")
	}
	return err
}

// push runs "git push -f --mirror dest" in the repository root.
// It tries three times, just in case it failed because of a transient error.
func (r *repo) push(dest string) error {
	err := try(3, func(attempt int) error {
		r.setStatus(fmt.Sprintf("syncing to %v, attempt %d", dest, attempt))
		if _, stderr, err := r.runGitLogged("push", "-f", "--mirror", dest); err != nil {
			return fmt.Errorf("%v\n\n%s", err, stderr)
		}
		return nil
	})
	if err != nil {
		r.setStatus("sync to " + dest + " failed")
	} else {
		r.setStatus("did sync to " + dest)
	}
	return err
}

func (r *repo) fetchRevIfNeeded(ctx context.Context, rev string) error {
	if _, _, err := r.runGitQuiet("cat-file", "-e", rev); err == nil {
		return nil
	}
	r.logf("attempting to fetch missing revision %s from origin", rev)
	_, _, err := r.runGitLogged("fetch", "origin", rev)
	return err
}

// GET /<name>.tar.gz
// GET /debug/watcher/<name>
func (r *repo) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(req.URL.Path, "/debug/watcher/") {
		r.serveStatus(w, req)
		return
	}
	rev := req.FormValue("rev")
	if rev == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	defer cancel()
	if err := r.fetchRevIfNeeded(ctx, rev); err != nil {
		// Try the archive anyway, it might work
		r.logf("error fetching revision %s: %v", rev, err)
	}
	tgz, _, err := r.runGitQuiet("archive", "--format=tgz", rev)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(tgz)))
	w.Header().Set("Content-Type", "application/x-compressed")
	w.Write(tgz)
}

func (r *repo) serveStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><head><title>watcher: %s</title><body><h1>watcher status for repo: %q</h1>\n",
		r.name, r.name)
	fmt.Fprintf(w, "<pre>\n")
	nowRound := time.Now().Round(time.Second)
	r.status.foreachDesc(func(ent statusEntry) {
		fmt.Fprintf(w, "%v   %-20s %v\n",
			ent.t.In(time.UTC).Format(time.RFC3339),
			nowRound.Sub(ent.t.Round(time.Second)).String()+" ago",
			ent.status)
	})
	fmt.Fprintf(w, "\n</pre></body></html>")
}

func try(n int, fn func(attempt int) error) error {
	var err error
	for tries := 0; tries < n; tries++ {
		time.Sleep(time.Duration(tries) * 5 * time.Second) // Linear back-off.
		if err = fn(tries); err == nil {
			break
		}
	}
	return err
}

func (m *mirror) notifyChanged(name string) {
	repo, ok := m.repos[name]
	if !ok {
		return
	}
	select {
	case repo.changed <- true:
	default:
	}
}

// pollGerritAndTickle polls Gerrit's JSON meta URL of all its URLs
// and their current branch heads.  When this sees that one has
// changed, it tickles the channel for that repo and wakes up its
// poller, if its poller is in a sleep.
func (m *mirror) pollGerritAndTickle() {
	last := map[string]string{} // repo -> last seen hash
	for {
		gerritRepos, err := m.gerritMetaMap()
		if err != nil {
			log.Printf("pollGerritAndTickle: gerritMetaMap failed, skipping: %v", err)
			gerritRepos = nil
		}
		for repo, hash := range gerritRepos {
			if hash != last[repo] {
				last[repo] = hash
				m.notifyChanged(repo)
			}
		}
		time.Sleep(*flagPollInterval)
	}
}

// subscribeToMaintnerAndTickleLoop subscribes to maintner.golang.org
// and watches for any ref changes in realtime.
func (m *mirror) subscribeToMaintnerAndTickleLoop() {
	for {
		if err := m.subscribeToMaintnerAndTickle(); err != nil {
			log.Printf("maintner loop: %v; retrying in 30 seconds", err)
			time.Sleep(30 * time.Second)
		}
	}
}

func (m *mirror) subscribeToMaintnerAndTickle() error {
	ctx := context.Background()
	retryTicker := time.NewTicker(10 * time.Second)
	defer retryTicker.Stop() // we never return, though
	for {
		err := maintner.TailNetworkMutationSource(ctx, godata.Server, func(e maintner.MutationStreamEvent) error {
			if e.Mutation != nil && e.Mutation.Gerrit != nil {
				gm := e.Mutation.Gerrit
				if strings.HasPrefix(gm.Project, "go.googlesource.com/") {
					proj := strings.TrimPrefix(gm.Project, "go.googlesource.com/")
					log.Printf("maintner refs for %s changed", gm.Project)
					m.notifyChanged(proj)
				}
			}
			return e.Err
		})
		log.Printf("maintner tail error: %v; sleeping+restarting", err)

		// prevent retry looping faster than once every 10
		// seconds; but usually retry immediately in the case
		// where we've been runing for a while already.
		<-retryTicker.C
	}
}

// gerritMetaMap returns the map from repo name (e.g. "go") to its
// latest master hash.
func (m *mirror) gerritMetaMap() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	meta, err := m.gerritClient.GetProjects(ctx, "master")
	if err != nil {
		return nil, fmt.Errorf("gerritClient.GetProjects: %v", err)
	}
	result := map[string]string{}
	for repo, v := range meta {
		if master, ok := v.Branches["master"]; ok {
			result[repo] = master
		}
	}
	return result, nil
}

// GET /debug/goroutines
func handleDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	buf := make([]byte, 1<<20)
	w.Write(buf[:runtime.Stack(buf, true)])
}

// GET /debug/env
func handleDebugEnv(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, kv := range os.Environ() {
		fmt.Fprintf(w, "%s\n", kv)
	}
}
