// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The gitmirror binary watches the specified Gerrit repositories for
// new commits and syncs them to GitHub.
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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
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
)

const (
	goBase = "https://go.googlesource.com/"
)

var (
	httpAddr     = flag.String("http", "", "If non-empty, the listen address to run an HTTP server on")
	cacheDir     = flag.String("cachedir", "", "git cache directory. If empty a temp directory is made.")
	pollInterval = flag.Duration("poll", 60*time.Second, "Remote repo poll interval")
	mirror       = flag.Bool("mirror", false, "whether to mirror to github; if disabled, it only runs in HTTP archive server mode")
)

var gerritClient = gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)

func main() {
	flag.Parse()
	if err := gitauth.Init(); err != nil {
		log.Fatalf("gitauth: %v", err)
	}

	log.Printf("gitmirror running.")

	sc := mustCreateSecretClient()
	defer sc.Close()

	go pollGerritAndTickle()
	go subscribeToMaintnerAndTickleLoop()
	err := runGitMirror(sc)
	log.Fatalf("gitmirror exiting after failure: %v", err)
}

// runGitMirror is a little wrapper so we can use defer and return to signal
// errors. It should only return a non-nil error.
func runGitMirror(sc *secret.Client) error {
	if *mirror {
		sshDir := filepath.Join(homeDir(), ".ssh")
		sshKey := filepath.Join(sshDir, "id_ed25519")
		if _, err := os.Stat(sshKey); err == nil {
			log.Printf("Using github ssh key at %v", sshKey)
		} else {

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if privKey, err := sc.Retrieve(ctx, secret.NameGitHubSSHKey); err == nil && len(privKey) > 0 {
				if err := os.MkdirAll(sshDir, 0700); err != nil {
					return err
				}
				if err := ioutil.WriteFile(sshKey, []byte(privKey+"\n"), 0600); err != nil {
					return err
				}
				log.Printf("Wrote %s from GCP secret manager.", sshKey)
			} else {
				return fmt.Errorf("Can't mirror to github without %q GCP secret manager or file %v", secret.NameGitHubSSHKey, sshKey)
			}
		}
	}

	if *cacheDir == "" {
		dir, err := ioutil.TempDir("", "gitmirror")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		*cacheDir = dir
	} else {
		fi, err := os.Stat(*cacheDir)
		if os.IsNotExist(err) {
			if err := os.MkdirAll(*cacheDir, 0755); err != nil {
				return fmt.Errorf("failed to create watcher's git cache dir: %v", err)
			}
		} else {
			if err != nil {
				return fmt.Errorf("invalid -cachedir: %v", err)
			}
			if !fi.IsDir() {
				return fmt.Errorf("invalid -cachedir=%q; not a directory", *cacheDir)
			}
		}
	}

	if *httpAddr != "" {
		http.HandleFunc("/debug/env", handleDebugEnv)
		http.HandleFunc("/debug/goroutines", handleDebugGoroutines)
		ln, err := net.Listen("tcp", *httpAddr)
		if err != nil {
			return err
		}
		go http.Serve(ln, nil)
	}

	errc := make(chan error)

	startRepo := func(name string) {
		log.Printf("Starting watch of repo %s", name)
		url := goBase + name
		var dst string
		if *mirror {
			dst = shouldMirrorTo(name)
			if dst != "" {
				log.Printf("Starting mirror of subrepo %s", name)
			} else {
				log.Printf("Not mirroring repo %s", name)
			}
		}
		r, err := NewRepo(url, dst)
		if err != nil {
			errc <- err
			return
		}
		http.Handle("/"+name+".tar.gz", r)
		reposMu.Lock()
		repos = append(repos, r)
		sort.Slice(repos, func(i, j int) bool { return repos[i].name() < repos[j].name() })
		reposMu.Unlock()
		r.Loop()
	}

	if *mirror {
		gerritRepos, err := gerritMetaMap()
		if err != nil {
			return fmt.Errorf("gerritMetaMap: %v", err)
		}
		for name := range gerritRepos {
			go startRepo(name)
		}
	}

	http.HandleFunc("/", handleRoot)

	// Blocks forever if all the NewRepo calls succeed:
	return <-errc
}

var (
	reposMu sync.Mutex
	repos   []*Repo
)

// GET /
// or:
// GET /debug/watcher/
func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/debug/watcher/" {
		http.NotFound(w, r)
		return
	}
	reposMu.Lock()
	defer reposMu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, "<html><body><pre>")
	for _, r := range repos {
		fmt.Fprintf(w, "<a href='/debug/watcher/%s'>%s</a> - %s\n", r.name(), r.name(), r.statusLine())
	}
	fmt.Fprint(w, "</pre></body></html>")
}

// shouldMirrorTo returns the GitHub repository the named repo should be
// mirrored to or "" if it should not be mirrored.
func shouldMirrorTo(name string) (dst string) {
	if r, ok := repospkg.ByGerritProject[name]; ok && r.MirrorToGitHub {
		return "git@github.com:" + r.GitHubRepo() + ".git"
	}
	return ""
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

// Repo represents a repository to be watched.
type Repo struct {
	root   string // on-disk location of the git repo, *cacheDir/name
	mirror bool   // push new commits to 'dest' remote
	status statusRing

	mu        sync.Mutex
	err       error
	firstBad  time.Time
	lastBad   time.Time
	firstGood time.Time
	lastGood  time.Time
}

// NewRepo checks out a new instance of the git repository
// specified by srcURL.
//
// If dstURL is not empty, changes from the source repository will
// be mirrored to the specified destination repository.
func NewRepo(srcURL, dstURL string) (*Repo, error) {
	name := path.Base(srcURL) // "go", "net", etc
	root := filepath.Join(*cacheDir, name)
	r := &Repo{
		root:   root,
		mirror: dstURL != "",
	}

	http.Handle("/debug/watcher/"+r.name(), r)

	needClone := true
	if r.shouldTryReuseGitDir(dstURL) {
		r.setStatus("reusing git dir; running git fetch")
		cmd := exec.Command("git", "fetch", "--prune", "origin")
		cmd.Dir = r.root
		r.logf("running git fetch")
		t0 := time.Now()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			r.logf("git fetch failed; proceeding to wipe + clone instead; err: %v, stderr: %s", err, stderr.Bytes())
		} else {
			needClone = false
			r.logf("ran git fetch in %v", time.Since(t0))
		}
	}
	if needClone {
		r.setStatus("need clone; removing cache root")
		os.RemoveAll(r.root)
		t0 := time.Now()
		r.setStatus("running fresh git clone --mirror")
		r.logf("cloning %v into %s", srcURL, r.root)
		cmd := exec.Command("git", "clone", "--mirror", srcURL, r.root)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("cloning %s: %v\n\n%s", srcURL, err, out)
		}
		r.setStatus("cloned")
		r.logf("cloned in %v", time.Since(t0))
	}

	if r.mirror {
		r.setStatus("adding dest remote")
		if err := r.addRemote("dest", dstURL,
			// We want to include only the refs/heads/* and refs/tags/* namespaces
			// in the GitHub mirrors. They correspond to published branches and tags.
			// Leave out internal Gerrit namespaces such as refs/changes/*,
			// refs/users/*, etc., because they're not helpful on github.com/golang.
			"push = +refs/heads/*:refs/heads/*",
			"push = +refs/tags/*:refs/tags/*",
		); err != nil {
			r.setStatus("failed to add dest")
			return nil, fmt.Errorf("adding remote: %v", err)
		}
		r.setStatus("added dest remote")
	}

	return r, nil
}

func (r *Repo) setErr(err error) {
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

func (r *Repo) statusLine() string {
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

func (r *Repo) setStatus(status string) {
	r.status.add(status)
}

// shouldTryReuseGitDir reports whether we should try to reuse r.root as the git
// directory. (The directory may be corrupt, though.)
// dstURL is optional, and is the desired remote URL for a remote named "dest".
func (r *Repo) shouldTryReuseGitDir(dstURL string) bool {
	if _, err := os.Stat(filepath.Join(r.root, "FETCH_HEAD")); err != nil {
		if os.IsNotExist(err) {
			r.logf("not reusing git dir; no FETCH_HEAD at %s", r.root)
		} else {
			r.logf("not reusing git dir; %v", err)
		}
		return false
	}
	if dstURL == "" {
		r.logf("not reusing git dir because dstURL is empty")
		return true
	}

	// Does the "dest" remote match? If not, we return false and nuke
	// the world and re-clone out of laziness.
	cmd := exec.Command("git", "remote", "-v")
	cmd.Dir = r.root
	out, err := cmd.Output()
	if err != nil {
		log.Printf("git remote -v: %v", err)
	}
	foundWrong := false
	for _, ln := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(ln, "dest") {
			continue
		}
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		if f[0] == "dest" {
			if f[1] == dstURL {
				return true
			}
			if !foundWrong {
				foundWrong = true
				r.logf("found dest of %q, which doesn't equal sought %q", f[1], dstURL)
			}
		}
	}
	r.logf("not reusing old repo: remote \"dest\" URL doesn't match")
	return false
}

func (r *Repo) addRemote(name, url string, opts ...string) error {
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
func (r *Repo) Loop() {
	tickler := repoTickler(r.name())
	for {
		if err := r.fetch(); err != nil {
			r.logf("fetch failed in repo loop: %v", err)
			r.setErr(err)
			time.Sleep(10 * time.Second)
			continue
		}
		if r.mirror {
			if err := r.push(); err != nil {
				r.logf("push failed in repo loop: %v", err)
				r.setErr(err)
				time.Sleep(10 * time.Second)
				continue
			}
		}

		r.setErr(nil)
		r.setStatus("waiting")
		// We still run a timer but a very slow one, just
		// in case the mechanism updating the repo tickler
		// breaks for some reason.
		timer := time.NewTimer(5 * time.Minute)
		select {
		case <-tickler:
			r.setStatus("got update tickle")
			timer.Stop()
		case <-timer.C:
			r.setStatus("poll timer fired")
		}
	}
}

func (r *Repo) name() string {
	return filepath.Base(r.root)
}

func (r *Repo) logf(format string, args ...interface{}) {
	log.Printf(r.name()+": "+format, args...)
}

// fetch runs "git fetch" in the repository root.
// It tries three times, just in case it failed because of a transient error.
func (r *Repo) fetch() (err error) {
	n := 0
	r.setStatus("running git fetch origin")
	defer func() {
		if err != nil {
			r.setStatus("git fetch failed")
		} else {
			r.setStatus("ran git fetch")
		}
	}()
	return try(3, func() error {
		n++
		if n > 1 {
			r.setStatus(fmt.Sprintf("running git fetch origin, attempt %d", n))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "fetch", "--prune", "origin")
		cmd.Dir = r.root
		if out, err := cmd.CombinedOutput(); err != nil {
			err = fmt.Errorf("%v\n\n%s", err, out)
			r.logf("git fetch: %v", err)
			return err
		}
		return nil
	})
}

// push runs "git push -f --mirror dest" in the repository root.
// It tries three times, just in case it failed because of a transient error.
func (r *Repo) push() (err error) {
	n := 0
	r.setStatus("syncing to github")
	defer func() {
		if err != nil {
			r.setStatus("sync to github failed")
		} else {
			r.setStatus("did sync to github")
		}
	}()
	return try(3, func() error {
		n++
		if n > 1 {
			r.setStatus(fmt.Sprintf("syncing to github, attempt %d", n))
		}
		cmd := exec.Command("git", "push", "-f", "--mirror", "dest")
		cmd.Dir = r.root
		if out, err := cmd.CombinedOutput(); err != nil {
			err = fmt.Errorf("%v\n\n%s", err, out)
			r.logf("git push failed: %v", err)
			return err
		}
		return nil
	})
}

// hasRev returns true if the repo contains the commit-ish rev.
func (r *Repo) hasRev(ctx context.Context, rev string) bool {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-t", rev)
	cmd.Dir = r.root
	return cmd.Run() == nil
}

// if non-nil, used by r.archive to create a "git archive" command.
var testHookArchiveCmd func(context.Context, string, ...string) *exec.Cmd

// if non-nil, used by r.archive to create a "git fetch" command.
var testHookFetchCmd func(context.Context, string, ...string) *exec.Cmd

// archive exports the git repository at the given rev and returns the
// compressed repository.
func (r *Repo) archive(ctx context.Context, rev string) ([]byte, error) {
	var cmd *exec.Cmd
	if testHookArchiveCmd == nil {
		cmd = exec.CommandContext(ctx, "git", "archive", "--format=tgz", rev)
	} else {
		cmd = testHookArchiveCmd(ctx, "git", "archive", "--format=tgz", rev)
	}
	cmd.Dir = r.root
	return cmd.Output()
}

// fetchRev attempts to fetch rev from remote.
func (r *Repo) fetchRev(ctx context.Context, remote, rev string) error {
	var cmd *exec.Cmd
	if testHookFetchCmd == nil {
		cmd = exec.CommandContext(ctx, "git", "fetch", remote, rev)
	} else {
		cmd = testHookFetchCmd(ctx, "git", "fetch", remote, rev)
	}
	cmd.Dir = r.root
	return cmd.Run()
}

func (r *Repo) fetchRevIfNeeded(ctx context.Context, rev string) error {
	if r.hasRev(ctx, rev) {
		return nil
	}
	r.logf("attempting to fetch missing revision %s from origin", rev)
	return r.fetchRev(ctx, "origin", rev)
}

// GET /<name>.tar.gz
// GET /debug/watcher/<name>
func (r *Repo) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
	tgz, err := r.archive(ctx, rev)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(tgz)))
	w.Header().Set("Content-Type", "application/x-compressed")
	w.Write(tgz)
}

func (r *Repo) serveStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><head><title>watcher: %s</title><body><h1>watcher status for repo: %q</h1>\n",
		r.name(), r.name())
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

func try(n int, fn func() error) error {
	var err error
	for tries := 0; tries < n; tries++ {
		time.Sleep(time.Duration(tries) * 5 * time.Second) // Linear back-off.
		if err = fn(); err == nil {
			break
		}
	}
	return err
}

func homeDir() string {
	switch runtime.GOOS {
	case "plan9":
		return os.Getenv("home")
	case "windows":
		return os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
	}
	return os.Getenv("HOME")
}

var (
	ticklerMu sync.Mutex
	ticklers  = make(map[string]chan bool)
)

// repo is the gerrit repo: e.g. "go", "net", "crypto", ...
func repoTickler(repo string) chan bool {
	ticklerMu.Lock()
	defer ticklerMu.Unlock()
	if c, ok := ticklers[repo]; ok {
		return c
	}
	c := make(chan bool, 1)
	ticklers[repo] = c
	return c
}

// pollGerritAndTickle polls Gerrit's JSON meta URL of all its URLs
// and their current branch heads.  When this sees that one has
// changed, it tickles the channel for that repo and wakes up its
// poller, if its poller is in a sleep.
func pollGerritAndTickle() {
	last := map[string]string{} // repo -> last seen hash
	for {
		gerritRepos, err := gerritMetaMap()
		if err != nil {
			log.Printf("pollGerritAndTickle: gerritMetaMap failed, skipping: %v", err)
			gerritRepos = nil
		}
		for repo, hash := range gerritRepos {
			if hash != last[repo] {
				last[repo] = hash
				select {
				case repoTickler(repo) <- true:
				default:
				}
			}
		}
		time.Sleep(*pollInterval)
	}
}

// subscribeToMaintnerAndTickleLoop subscribes to maintner.golang.org
// and watches for any ref changes in realtime.
func subscribeToMaintnerAndTickleLoop() {
	for {
		if err := subscribeToMaintnerAndTickle(); err != nil {
			log.Printf("maintner loop: %v; retrying in 30 seconds", err)
			time.Sleep(30 * time.Second)
		}
	}
}

func subscribeToMaintnerAndTickle() error {
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
					select {
					case repoTickler(proj) <- true:
					default:
					}
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
func gerritMetaMap() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	meta, err := gerritClient.GetProjects(ctx, "master")
	if err != nil {
		return nil, fmt.Errorf("gerritClient.GetProjects: %v", err)
	}
	m := map[string]string{}
	for repo, v := range meta {
		if master, ok := v.Branches["master"]; ok {
			m[repo] = master
		}
	}
	return m, nil
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

func mustCreateSecretClient() *secret.Client {
	client, err := secret.NewClient()
	if err != nil {
		log.Fatalf("unable to create secret client %v", err)
	}
	return client
}
