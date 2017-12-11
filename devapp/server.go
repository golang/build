// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
)

// A server is an http.Handler that serves content within staticDir at root and
// the dynamically-generated dashboards at their respective endpoints.
type server struct {
	mux         *http.ServeMux
	staticDir   string
	templateDir string

	cMu              sync.RWMutex // Used to protect the fields below.
	corpus           *maintner.Corpus
	repo             *maintner.GitHubRepo
	helpWantedIssues []int32
	data             pageData

	// GopherCon-specific fields. Must still hold cMu when reading/writing these.
	userMapping map[int]*maintner.GitHubUser // Gerrit Owner ID => GitHub user
	activities  []activity                   // All contribution activities
	totalPoints int
}

type pageData struct {
	release releaseData
	reviews reviewsData
}

func newServer(mux *http.ServeMux, staticDir, templateDir string) *server {
	s := &server{
		mux:         mux,
		staticDir:   staticDir,
		templateDir: templateDir,
		userMapping: map[int]*maintner.GitHubUser{},
	}
	s.mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
	s.mux.HandleFunc("/favicon.ico", s.handleFavicon)
	s.mux.HandleFunc("/release", s.withTemplate("/release.tmpl", s.handleRelease))
	s.mux.HandleFunc("/reviews", s.withTemplate("/reviews.tmpl", s.handleReviews))
	s.mux.HandleFunc("/dir/", handleDirRedirect)
	for _, p := range []string{"/imfeelinghelpful", "/imfeelinglucky"} {
		s.mux.HandleFunc(p, s.handleRandomHelpWantedIssue)
	}
	s.mux.HandleFunc("/_/activities", s.handleActivities)
	return s
}

func (s *server) withTemplate(tmpl string, fn func(*template.Template, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	t := template.Must(template.ParseFiles(path.Join(s.templateDir, tmpl)))
	return func(w http.ResponseWriter, r *http.Request) { fn(t, w, r) }
}

// initCorpus fetches a full maintner corpus, overwriting any existing data.
func (s *server) initCorpus(ctx context.Context) error {
	s.cMu.Lock()
	defer s.cMu.Unlock()
	corpus, err := godata.Get(ctx)
	if err != nil {
		return fmt.Errorf("godata.Get: %v", err)
	}
	s.corpus = corpus
	s.repo = s.corpus.GitHub().Repo("golang", "go") // The golang/go repo.
	if s.repo == nil {
		return fmt.Errorf(`s.corpus.GitHub().Repo("golang", "go") = nil`)
	}
	return nil
}

// corpusUpdateLoop continuously updates the server’s corpus until ctx’s Done
// channel is closed.
func (s *server) corpusUpdateLoop(ctx context.Context) {
	log.Println("Starting corpus update loop ...")
	for {
		log.Println("Updating help wanted issues ...")
		s.updateHelpWantedIssues()
		log.Println("Updating activities ...")
		s.updateActivities()
		s.cMu.Lock()
		s.data.release.dirty = true
		s.data.reviews.dirty = true
		s.cMu.Unlock()
		err := s.corpus.UpdateWithLocker(ctx, &s.cMu)
		if err != nil {
			if err == maintner.ErrSplit {
				log.Println("Corpus out of sync. Re-fetching corpus.")
				s.initCorpus(ctx)
			} else {
				log.Printf("corpus.Update: %v; sleeping 15s", err)
				time.Sleep(15 * time.Second)
				continue
			}
		}

		select {
		case <-ctx.Done():
			return
		default:
			continue
		}
	}
}

const (
	issuesURLBase = "https://golang.org/issue/"

	labelHelpWantedID = 150880243
)

func (s *server) updateHelpWantedIssues() {
	s.cMu.Lock()
	defer s.cMu.Unlock()

	var ids []int32
	s.repo.ForeachIssue(func(i *maintner.GitHubIssue) error {
		if i.Closed {
			return nil
		}
		if i.HasLabelID(labelHelpWantedID) {
			ids = append(ids, i.Number)
		}
		return nil
	})
	s.helpWantedIssues = ids
}

func (s *server) handleRandomHelpWantedIssue(w http.ResponseWriter, r *http.Request) {
	s.cMu.RLock()
	defer s.cMu.RUnlock()
	if len(s.helpWantedIssues) == 0 {
		http.Redirect(w, r, issuesURLBase, http.StatusSeeOther)
		return
	}
	rid := s.helpWantedIssues[rand.Intn(len(s.helpWantedIssues))]
	http.Redirect(w, r, issuesURLBase+strconv.Itoa(int(rid)), http.StatusSeeOther)
}

func (s *server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	// Need to specify content type for consistent tests, without this it's
	// determined from mime.types on the box the test is running on
	w.Header().Set("Content-Type", "image/x-icon")
	http.ServeFile(w, r, path.Join(s.staticDir, "/favicon.ico"))
}

// ServeHTTP satisfies the http.Handler interface.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS != nil {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; preload")
	}
	s.mux.ServeHTTP(w, r)
}

// handleDirRedirect accepts requests of the form:
//     /dir/REPO/some/dir/
// And redirects them to either:
//     https://github.com/golang/REPO/tree/master/some/dir/
// or:
//     https://go.googlesource.com/REPO/+/master/some/dir/
// ... depending on the Referer. This is so we can make links
// in Markdown docs that are clickable on both GitHub and
// in the go.googlesource.com viewer. If detection fails, we
// default to GitHub.
func handleDirRedirect(w http.ResponseWriter, r *http.Request) {
	useGoog := strings.Contains(r.Referer(), "googlesource.com")
	path := r.URL.Path
	if !strings.HasPrefix(path, "/dir/") {
		http.Error(w, "bad mux", http.StatusInternalServerError)
		return
	}
	path = strings.TrimPrefix(path, "/dir/")
	// path is now "REPO/some/dir/"
	var repo string
	slash := strings.IndexByte(path, '/')
	if slash == -1 {
		repo, path = path, ""
	} else {
		repo, path = path[:slash], path[slash+1:]
	}
	path = strings.TrimSuffix(path, "/")
	var target string
	if useGoog {
		target = fmt.Sprintf("https://go.googlesource.com/%s/+/master/%s", repo, path)
	} else {
		target = fmt.Sprintf("https://github.com/golang/%s/tree/master/%s", repo, path)
	}
	http.Redirect(w, r, target, http.StatusFound)
}
