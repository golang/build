// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
)

// A server is an http.Handler that serves content within staticDir at root and
// the dynamically-generated dashboards at their respective endpoints.
type server struct {
	mux       *http.ServeMux
	staticDir string

	cMu              sync.RWMutex // Used to protect the fields below.
	corpus           *maintner.Corpus
	helpWantedIssues []int32
}

func newServer(mux *http.ServeMux, staticDir string) *server {
	s := &server{
		mux:       mux,
		staticDir: staticDir,
	}
	s.mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
	s.mux.HandleFunc("/favicon.ico", s.handleFavicon)
	s.mux.HandleFunc("/release", handleRelease)
	s.mux.HandleFunc("/gophercon", s.handleGopherCon)
	for _, p := range []string{"/imfeelinghelpful", "/imfeelinglucky"} {
		s.mux.HandleFunc(p, s.handleRandomHelpWantedIssue)
	}
	return s
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
	return nil
}

// corpusUpdateLoop continuously updates the server’s corpus until ctx’s Done
// channel is closed.
func (s *server) corpusUpdateLoop(ctx context.Context) {
	log.Println("Starting corpus update loop ...")
	for {
		log.Println("Updating help wanted issues ...")
		s.updateHelpWantedIssues()
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
	labelIDHelpWanted = 150880243
	issuesURLBase     = "https://github.com/golang/go/issues/"
)

func (s *server) updateHelpWantedIssues() {
	s.cMu.Lock()
	defer s.cMu.Unlock()
	repo := s.corpus.GitHub().Repo("golang", "go")
	if repo == nil {
		log.Printf(`s.corpus.GitHub().Repo("golang", "go") == nil`)
		return
	}

	ids := []int32{}
	repo.ForeachIssue(func(i *maintner.GitHubIssue) error {
		if i.Closed {
			return nil
		}
		if _, ok := i.Labels[labelIDHelpWanted]; ok {
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

var (
	pageStoreMu sync.Mutex
	pageStore   = map[string][]byte{}
)

func getPage(name string) ([]byte, error) {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	p, ok := pageStore[name]
	if ok {
		return p, nil
	}
	return nil, fmt.Errorf("page key %s not found", name)
}

func writePage(key string, content []byte) error {
	pageStoreMu.Lock()
	defer pageStoreMu.Unlock()
	pageStore[key] = content
	return nil
}

func servePage(w http.ResponseWriter, r *http.Request, key string) {
	b, err := getPage(key)
	if err != nil {
		log.Printf("getPage(%q) = %v", key, err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func handleRelease(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, "release")
}
