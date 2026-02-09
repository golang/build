// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Fetchlogs downloads build failure logs from the Go dashboard so
// they can be accessed and searched from the local file system.
//
// It organizes these logs into two directories created in the
// directory specified by the -dir flag (which typically defaults to
// ~/.cache/fetchlogs). The log/ directory contains all log files
// named the same way they are named by the dashboard (which happens
// to be the SHA-1 of their contents). The rev/ directory contains
// symlinks back to these logs named
//
//	rev/<ISO 8601 commit date>-<git revision>/<builder>
//
// Fetchlogs will reuse existing log files and revision symlinks, so
// it only has to download logs that are new since the last time it
// was run.
//
// This makes failures easily searchable with standard tools. For
// example, to list the revisions and builders with a particular
// failure, use:
//
//	grep -lR <regexp> rev | sort
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/build/repos"
	"golang.org/x/build/types"
)

var defaultDir = filepath.Join(xdgCacheDir(), "fetchlogs")

var (
	flagN         = flag.Int("n", 300, "limit to most recent `N` commits per repo")
	flagPar       = flag.Int("j", 5, "number of concurrent download `jobs`")
	flagDir       = flag.String("dir", defaultDir, "`directory` to save logs to")
	flagRepo      = flag.String("repo", "go", `comma-separated list of repos to fetch logs for, or "all" for all known repos`)
	flagBranch    = flag.String("branch", "", `comma-separated list of Go repo branches to fetch logs for; default branch if empty`)
	flagDashboard = flag.String("dashboard", "https://build.golang.org", `the dashboard root url`)
)

func main() {
	log.SetPrefix("fetchlogs: ")
	log.SetFlags(0)

	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}

	// If the top-level directory is the default XDG cache
	// directory, make sure it exists.
	if *flagDir == defaultDir {
		if err := xdgCreateDir(*flagDir); err != nil {
			log.Fatal(err)
		}
	}

	// Create directory structure.
	if err := os.Chdir(*flagDir); err != nil {
		log.Fatal(err)
	}
	ensureDir("log")
	ensureDir("rev")

	// Set up fetchers.
	fetcher := newFetcher(*flagPar)
	wg := sync.WaitGroup{}

	// Fetch dashboard pages.
	for _, repo := range parseRepoFlag() {
		for branch := range strings.SplitSeq(*flagBranch, ",") {
			project := repo.GoGerritProject
			haveCommits := 0
			for page := 0; haveCommits < *flagN; page++ {
				dashURL := fmt.Sprintf("%s/?mode=json&page=%d", *flagDashboard, page)
				if project != "go" {
					dashURL += "&repo=" + url.QueryEscape(repo.ImportPath)
				}
				if branch != "" {
					dashURL += "&branch=" + url.QueryEscape(branch)
				}
				index, err := fetcher.get(dashURL)
				if err != nil {
					log.Fatal(err)
				}

				var status types.BuildStatus
				if err = json.NewDecoder(index).Decode(&status); err != nil {
					log.Fatal("error unmarshalling result: ", err)
				}
				index.Close()

				if len(status.Revisions) == 0 {
					// We asked for a page of revisions and received a valid reply with none.
					// Assume that there are no more beyond this.
					break
				}

				for _, rev := range status.Revisions {
					if haveCommits >= *flagN {
						break
					}
					if rev.Repo != project {
						// The results for the "go" repo (fetched without the "&repo" query
						// parameter) empirically include some subrepo results for release
						// branches.
						//
						// Those aren't really relevant to the "go" repo — and they should be
						// included when we fetch the subrepo explicitly anyway — so filter
						// them out here.
						continue
					}
					haveCommits++

					// Create a revision directory. This way we
					// have a record of commits with no failures.
					date, err := parseRevDate(rev.Date)
					if err != nil {
						log.Fatal("malformed revision date: ", err)
					}
					var goDate time.Time
					if rev.GoRevision != "" {
						commit, err := goProject(useCached).GitCommit(rev.GoRevision)
						if err != nil {
							// A rare race is possible here: if a commit is added to the Go repo
							// after the initial maintner load, and a dashboard test run completes
							// for that commit before we're done fetching logs, the maintner data
							// might not include that commit. To rule out that possibility, refresh
							// the local maintner data before bailing out.
							commit, err = goProject(forceRefresh).GitCommit(rev.GoRevision)
							if err != nil {
								log.Fatal("invalid GoRevision: ", err)
							}
						}
						goDate = commit.CommitTime
					}
					revDir, revDirDepth := revToDir(rev.Revision, date, rev.GoRevision, goDate)
					ensureDir(revDir)

					if rev.GoRevision != "" {
						// In October 2021 we started creating a separate subdirectory for
						// each Go repo commit. (Previously, we overwrote the link for each
						// subrepo commit when downloading a new Go commit.) Remove the
						// previous links, if any, so that greplogs won't double-count them.
						prevRevDir, _ := revToDir(rev.Revision, date, "", time.Time{})
						if err := os.RemoveAll(prevRevDir); err != nil {
							log.Fatal(err)
						}
					}

					// Save revision metadata.
					buf := bytes.Buffer{}
					enc := json.NewEncoder(&buf)
					if err = enc.Encode(rev); err != nil {
						log.Fatal(err)
					}
					if err = writeFileAtomic(filepath.Join(revDir, ".rev.json"), &buf); err != nil {
						log.Fatal("error saving revision metadata: ", err)
					}

					// Save builders list so Results list can be
					// interpreted.
					if err = enc.Encode(status.Builders); err != nil {
						log.Fatal(err)
					}
					if err = writeFileAtomic(filepath.Join(revDir, ".builders.json"), &buf); err != nil {
						log.Fatal("error saving builders metadata: ", err)
					}

					// Fetch revision logs.
					for i, res := range rev.Results {
						if res == "" || res == "ok" {
							continue
						}

						wg.Add(1)
						go func(builder, logURL string) {
							defer wg.Done()
							logPath := filepath.Join("log", filepath.Base(logURL))
							err := fetcher.getFile(logURL, logPath)
							if err != nil {
								log.Fatal("error fetching log: ", err)
							}
							if err := linkLog(revDir, revDirDepth, builder, logPath); err != nil {
								log.Fatal("error linking log: ", err)
							}
						}(status.Builders[i], res)
					}
				}
			}
		}
	}

	wg.Wait()
}

func parseRepoFlag() (rs []*repos.Repo) {
	if *flagRepo == "all" {
		for p, repo := range repos.ByGerritProject {
			if p == "go" || repo.ShowOnDashboard() {
				rs = append(rs, repo)
			}
		}
	} else {
		for p := range strings.SplitSeq(*flagRepo, ",") {
			p = strings.TrimSpace(p)
			repo := repos.ByGerritProject[p]
			if repo == nil {
				log.Fatalf("unknown repo %s", *flagRepo)
			}
			rs = append(rs, repo)
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		pi := rs[i].GoGerritProject
		pj := rs[j].GoGerritProject

		// Read "go" first because it doesn't require maintner data.
		if pj == "go" {
			return false // Nothing is before "go".
		} else if pi == "go" {
			return true // "go" is before everything else.
		}

		return pi < pj
	})

	if len(rs) == 0 {
		log.Fatal("-repo flag does not contain any repos")
	}
	if rs[0].GoGerritProject == "go" && len(rs) > 1 {
		go func() {
			// Prefetch maintner data, since we'll likely need it and can hide
			// some of the latency behind processing the "go" project
			// (which does not need it).
			//
			// If the first repo is not "go", then we'll either need the maintner data
			// right away (in which case we can't hide any substantial latency) or not
			// at all (in which case we shouldn't bother churning memory and disk
			// pages to load it).
			_ = goProject(useCached)
		}()
	}

	return rs
}

// A fetcher downloads files over HTTP concurrently. It allows
// limiting the number of concurrent downloads and correctly handles
// multiple (possibly concurrent) fetches from the same URL to the
// same file.
type fetcher struct {
	tokens chan struct{}

	pending struct {
		sync.Mutex
		m map[string]*pendingFetch
	}
}

type pendingFetch struct {
	wchan chan struct{} // closed when fetch completes

	// err is the error, if any, that occurred during this fetch.
	// It will be set before wchan is closed.
	err error
}

func newFetcher(jobs int) *fetcher {
	f := new(fetcher)

	f.tokens = make(chan struct{}, *flagPar)
	for i := 0; i < jobs; i++ {
		f.tokens <- struct{}{}
	}

	f.pending.m = make(map[string]*pendingFetch)

	return f
}

// get performs an HTTP GET for URL and returns the body, while
// obeying the job limit on fetcher.
func (f *fetcher) get(url string) (io.ReadCloser, error) {
	<-f.tokens
	fmt.Println("fetching", url)
	resp, err := http.Get(url)
	f.tokens <- struct{}{}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %v %s", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	return resp.Body, nil
}

// getFile performs an HTTP GET for URL and writes it to filename. If
// the destination file already exists, this returns immediately. If
// another goroutine is currently fetching filename, this blocks until
// the fetch is done and then returns.
func (f *fetcher) getFile(url string, filename string) error {
	// Do we already have it?
	if _, err := os.Stat(filename); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	// Check if another fetcher is working on it.
	f.pending.Lock()
	if p, ok := f.pending.m[filename]; ok {
		f.pending.Unlock()
		<-p.wchan
		return p.err
	}

	p := &pendingFetch{wchan: make(chan struct{})}
	f.pending.m[filename] = p
	f.pending.Unlock()

	r, err := f.get(url)
	if err == nil {
		err = writeFileAtomic(filename, r)
		r.Close()
	}
	p.err = err

	close(p.wchan)
	return p.err
}

var (
	goProjectMu     sync.Mutex
	cachedGoProject *maintner.GerritProject
	goProjectErr    error
)

func getGoProject(ctx context.Context) (*maintner.GerritProject, error) {
	corpus, err := godata.Get(ctx)
	if err != nil {
		return nil, err
	}

	gp := corpus.Gerrit().Project("go.googlesource.com", "go")
	if gp == nil {
		return nil, fmt.Errorf("go.googlesource.com/go Gerrit project not found")
	}

	return gp, nil
}

func goProject(policy refreshPolicy) *maintner.GerritProject {
	goProjectMu.Lock()
	defer goProjectMu.Unlock()
	if policy == forceRefresh || (cachedGoProject == nil && goProjectErr == nil) {
		cachedGoProject, goProjectErr = getGoProject(context.Background())
	}

	if goProjectErr != nil {
		log.Fatal(goProjectErr)
	}
	return cachedGoProject
}

type refreshPolicy int8

const (
	useCached refreshPolicy = iota
	forceRefresh
)

// ensureDir creates directory name if it does not exist.
func ensureDir(name string) {
	err := os.MkdirAll(name, 0777)
	if err != nil {
		log.Fatal("error creating directory ", name, ": ", err)
	}
}

// writeFileAtomic atomically creates a file called filename and
// copies the data from r to the file.
func writeFileAtomic(filename string, r io.Reader) error {
	tmpPath := filename + ".tmp"
	if f, err := os.Create(tmpPath); err != nil {
		return err
	} else {
		_, err := io.Copy(f, r)
		if err == nil {
			err = f.Sync()
		}
		err2 := f.Close()
		if err == nil {
			err = err2
		}
		if err != nil {
			os.Remove(tmpPath)
			return err
		}
	}
	if err := os.Rename(tmpPath, filename); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// linkLog creates a symlink for finding logPath based on its git
// revision and builder.
func linkLog(revDir string, revDirDepth int, builder, logPath string) error {
	// Create symlink.
	err := os.Symlink(strings.Repeat("../", revDirDepth)+logPath, filepath.Join(revDir, builder))
	if err != nil && !os.IsExist(err) {
		return err
	}

	return nil
}

// parseRevDate parses a revision date in RFC3339.
func parseRevDate(date string) (time.Time, error) {
	return time.Parse(time.RFC3339, date)
}

// revToDir returns the path of the revision directory for revision.
func revToDir(revision string, date time.Time, goRev string, goDate time.Time) (dir string, depth int) {
	if goDate.After(date) {
		date = goDate
	}
	dateStr := date.Format("2006-01-02T15:04:05")

	parts := []string{dateStr, revision[:7]}
	if goRev != "" {
		parts = append(parts, goRev[:7])
	}

	return filepath.Join("rev", strings.Join(parts, "-")), 2
}
