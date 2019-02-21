// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/build/internal/gophers"
	"golang.org/x/build/maintner"
)

type project struct {
	*maintner.GerritProject
	Changes []*change
}

// ReviewServer returns the hostname of the review server for a googlesource repo,
// e.g. "go-review.googlesource.com" for a "go.googlesource.com" server. For a
// non-googlesource.com server, it will return an empty string.
func (p *project) ReviewServer() string {
	const d = ".googlesource.com"
	s := p.Server()
	i := strings.Index(s, d)
	if i == -1 {
		return ""
	}
	return s[:i] + "-review" + d
}

type change struct {
	*maintner.GerritCL
	LastUpdate          time.Time
	FormattedLastUpdate string

	HasPlusTwo      bool
	HasPlusOne      bool
	HasMinusTwo     bool
	NoHumanComments bool
	SearchTerms     string
}

type reviewsData struct {
	Projects     []*project
	TotalChanges int

	// dirty is set if this data needs to be updated due to a corpus change.
	dirty bool
}

// handleReviews serves dev.golang.org/reviews.
func (s *server) handleReviews(t *template.Template, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.cMu.RLock()
	dirty := s.data.reviews.dirty
	s.cMu.RUnlock()
	if dirty {
		s.updateReviewsData()
	}

	s.cMu.RLock()
	defer s.cMu.RUnlock()

	ownerFilter := r.FormValue("owner")
	var (
		projects     []*project
		totalChanges int
	)
	if len(ownerFilter) > 0 {
		for _, p := range s.data.reviews.Projects {
			var cs []*change
			for _, c := range p.Changes {
				if o := c.Owner(); o != nil && o.Name() == ownerFilter {
					cs = append(cs, c)
					totalChanges++
				}
			}
			if len(cs) > 0 {
				projects = append(projects, &project{GerritProject: p.GerritProject, Changes: cs})
			}
		}
	} else {
		projects = s.data.reviews.Projects
		totalChanges = s.data.reviews.TotalChanges
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, struct {
		Projects     []*project
		TotalChanges int
	}{
		Projects:     projects,
		TotalChanges: totalChanges,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(w, &buf); err != nil {
		log.Printf("io.Copy(w, %+v) = %v", buf, err)
		return
	}
}

func (s *server) updateReviewsData() {
	log.Println("Updating reviews data ...")
	s.cMu.Lock()
	defer s.cMu.Unlock()
	var (
		projects     []*project
		totalChanges int
	)
	s.corpus.Gerrit().ForeachProjectUnsorted(func(p *maintner.GerritProject) error {
		proj := &project{GerritProject: p}
		p.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			if cl.WorkInProgress() ||
				cl.Owner() == nil ||
				strings.Contains(cl.Commit.Msg, "DO NOT REVIEW") {
				return nil
			}
			var searchTerms []string
			tags := cl.Meta.Hashtags()
			if tags.Contains("wait-author") ||
				tags.Contains("wait-release") ||
				tags.Contains("wait-issue") {
				return nil
			}
			c := &change{GerritCL: cl}
			searchTerms = append(searchTerms, "repo:"+p.Project())
			searchTerms = append(searchTerms, cl.Owner().Name())
			searchTerms = append(searchTerms, "owner:"+cl.Owner().Email())
			searchTerms = append(searchTerms, "involves:"+cl.Owner().Email())
			searchTerms = append(searchTerms, fmt.Sprint(cl.Number))
			searchTerms = append(searchTerms, cl.Subject())

			c.NoHumanComments = !hasHumanComments(cl)
			if c.NoHumanComments {
				searchTerms = append(searchTerms, "t:attn")
			}
			searchTerms = append(searchTerms, searchTermsFromReviewerFields(cl)...)
			for label, votes := range cl.Metas[len(cl.Metas)-1].LabelVotes() {
				for _, val := range votes {
					if label == "Code-Review" {
						switch val {
						case -2:
							c.HasMinusTwo = true
							searchTerms = append(searchTerms, "t:-2")
						case 1:
							c.HasPlusOne = true
							searchTerms = append(searchTerms, "t:+1")
						case 2:
							c.HasPlusTwo = true
							searchTerms = append(searchTerms, "t:+2")
						}
					}
				}
			}

			c.LastUpdate = cl.Commit.CommitTime
			if len(cl.Messages) > 0 {
				c.LastUpdate = cl.Messages[len(cl.Messages)-1].Date
			}
			c.FormattedLastUpdate = c.LastUpdate.Format("2006-01-02")
			searchTerms = append(searchTerms, c.FormattedLastUpdate)
			c.SearchTerms = strings.ToLower(strings.Join(searchTerms, " "))
			proj.Changes = append(proj.Changes, c)
			totalChanges++
			return nil
		})
		sort.Slice(proj.Changes, func(i, j int) bool {
			return proj.Changes[i].LastUpdate.Before(proj.Changes[j].LastUpdate)
		})
		projects = append(projects, proj)
		return nil
	})
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Project() < projects[j].Project()
	})
	s.data.reviews.Projects = projects
	s.data.reviews.TotalChanges = totalChanges
	s.data.reviews.dirty = false
}

// hasHumanComments reports whether cl has any comments from a human on it.
func hasHumanComments(cl *maintner.GerritCL) bool {
	const (
		gobotID     = "5976@62eb7196-b449-3ce5-99f1-c037f21e1705"
		gerritbotID = "12446@62eb7196-b449-3ce5-99f1-c037f21e1705"
	)

	for _, m := range cl.Messages {
		if email := m.Author.Email(); email != gobotID && email != gerritbotID {
			return true
		}
	}
	return false
}

// searchTermsFromReviewerFields returns a slice of terms generated from
// the reviewer and cc fields of a Gerrit change.
func searchTermsFromReviewerFields(cl *maintner.GerritCL) []string {
	var searchTerms []string
	reviewers := make(map[string]bool)
	ccs := make(map[string]bool)
	for _, m := range cl.Metas {
		if !strings.Contains(m.Commit.Msg, "Reviewer:") &&
			!strings.Contains(m.Commit.Msg, "CC:") &&
			!strings.Contains(m.Commit.Msg, "Removed:") {
			continue
		}
		maintner.ForeachLineStr(m.Commit.Msg, func(ln string) error {
			if !strings.HasPrefix(ln, "Reviewer:") &&
				!strings.HasPrefix(ln, "CC:") &&
				!strings.HasPrefix(ln, "Removed:") {
				return nil
			}
			gerritID := ln[strings.LastIndexByte(ln, '<')+1 : strings.LastIndexByte(ln, '>')]
			if strings.HasPrefix(ln, "Removed:") {
				delete(reviewers, gerritID)
				delete(ccs, gerritID)
			} else if strings.HasPrefix(ln, "Reviewer:") {
				delete(ccs, gerritID)
				reviewers[gerritID] = true
			} else if strings.HasPrefix(ln, "CC:") {
				delete(reviewers, gerritID)
				ccs[gerritID] = true
			}
			return nil
		})
	}
	for r := range reviewers {
		if p := gophers.GetPerson(r); p != nil && p.Gerrit != cl.Owner().Email() {
			searchTerms = append(searchTerms, "involves:"+p.Gerrit)
			searchTerms = append(searchTerms, "reviewer:"+p.Gerrit)
		}
	}
	for r := range ccs {
		if p := gophers.GetPerson(r); p != nil && p.Gerrit != cl.Owner().Email() {
			searchTerms = append(searchTerms, "involves:"+p.Gerrit)
			searchTerms = append(searchTerms, "cc:"+p.Gerrit)
		}
	}
	return searchTerms
}
