// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/maintner"
)

const (
	labelProposal = "Proposal"

	prefixProposal = "proposal:"
	prefixDev      = "[dev."
)

// titleDirs returns a slice of prefix directories contained in a title. For
// devapp,maintner: my cool new change, it will return ["devapp", "maintner"].
// If there is no dir prefix, it will return nil.
func titleDirs(title string) []string {
	if i := strings.Index(title, "\n"); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	i := strings.Index(title, ":")
	if i < 0 {
		return nil
	}
	var (
		b bytes.Buffer
		r []string
	)
	for j := 0; j < i; j++ {
		switch title[j] {
		case ' ':
			continue
		case ',':
			r = append(r, b.String())
			b.Reset()
			continue
		default:
			b.WriteByte(title[j])
		}
	}
	if b.Len() > 0 {
		r = append(r, b.String())
	}
	return r
}

type releaseData struct {
	LastUpdated string
	Sections    []section

	// dirty is set if this data needs to be updated due to a corpus change.
	dirty bool
}

type section struct {
	Title  string
	Count  int
	Groups []group
}

type group struct {
	Dir   string
	Items []item
}

type item struct {
	Issue *maintner.GitHubIssue
	CLs   []*maintner.GerritCL
}

type itemsBySummary []item

func (x itemsBySummary) Len() int           { return len(x) }
func (x itemsBySummary) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }
func (x itemsBySummary) Less(i, j int) bool { return itemSummary(x[i]) < itemSummary(x[j]) }

func itemSummary(it item) string {
	if it.Issue != nil {
		return it.Issue.Title
	}
	for _, cl := range it.CLs {
		return cl.Subject()
	}
	return ""
}

var milestoneRE = regexp.MustCompile(`^Go1\.(\d+)(|\.(\d+))(|[A-Z].*)$`)

type milestone struct {
	title        string
	major, minor int
}

type milestonesByGoVersion []milestone

func (x milestonesByGoVersion) Len() int      { return len(x) }
func (x milestonesByGoVersion) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x milestonesByGoVersion) Less(i, j int) bool {
	a, b := x[i], x[j]
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return a.title < b.title
}

func (s *server) updateReleaseData() {
	log.Println("Updating release data ...")
	s.cMu.Lock()
	defer s.cMu.Unlock()

	dirToCLs := map[string][]*maintner.GerritCL{}
	issueToCLs := map[int32][]*maintner.GerritCL{}
	s.corpus.Gerrit().ForeachProjectUnsorted(func(p *maintner.GerritProject) error {
		p.ForeachOpenCL(func(cl *maintner.GerritCL) error {
			if strings.HasPrefix(cl.Subject(), prefixDev) {
				return nil
			}
			for _, r := range cl.GitHubIssueRefs {
				issueToCLs[r.Number] = append(issueToCLs[r.Number], cl)
			}
			dirs := titleDirs(cl.Subject())
			if len(dirs) == 0 {
				dirToCLs[""] = append(dirToCLs[""], cl)
			} else {
				for _, d := range dirs {
					dirToCLs[d] = append(dirToCLs[d], cl)
				}
			}
			return nil
		})
		return nil
	})

	dirToIssues := map[string][]*maintner.GitHubIssue{}
	s.repo.ForeachIssue(func(issue *maintner.GitHubIssue) error {
		// Issues in active milestones.
		if !issue.Closed && issue.Milestone != nil && !issue.Milestone.Closed {
			dirs := titleDirs(issue.Title)
			if len(dirs) == 0 {
				dirToIssues[""] = append(dirToIssues[""], issue)
			} else {
				for _, d := range dirs {
					dirToIssues[d] = append(dirToIssues[d], issue)
				}
			}
		}
		return nil
	})

	s.data.Sections = nil
	s.appendOpenIssues(dirToIssues, issueToCLs)
	s.appendPendingCLs(dirToCLs)
	s.appendPendingProposals(issueToCLs)
	s.appendClosedIssues()
	s.data.LastUpdated = time.Now().UTC().Format(time.UnixDate)
	s.data.dirty = false
}

// requires s.cMu be locked.
func (s *server) appendOpenIssues(dirToIssues map[string][]*maintner.GitHubIssue, issueToCLs map[int32][]*maintner.GerritCL) {
	var issueDirs []string
	for d := range dirToIssues {
		issueDirs = append(issueDirs, d)
	}
	sort.Strings(issueDirs)
	ms := s.allMilestones()
	for _, m := range ms {
		var (
			issueGroups []group
			issueCount  int
		)
		for _, d := range issueDirs {
			issues, ok := dirToIssues[d]
			if !ok {
				continue
			}
			var items []item
			for _, i := range issues {
				if i.Milestone.Title != m.title {
					continue
				}

				items = append(items, item{
					Issue: i,
					CLs:   issueToCLs[i.Number],
				})
				issueCount++
			}
			if len(items) == 0 {
				continue
			}
			sort.Sort(itemsBySummary(items))
			issueGroups = append(issueGroups, group{
				Dir:   d,
				Items: items,
			})
		}
		s.data.Sections = append(s.data.Sections, section{
			Title:  m.title,
			Count:  issueCount,
			Groups: issueGroups,
		})
	}
}

// requires s.cMu be locked.
func (s *server) appendPendingCLs(dirToCLs map[string][]*maintner.GerritCL) {
	var clDirs []string
	for d := range dirToCLs {
		clDirs = append(clDirs, d)
	}
	sort.Strings(clDirs)
	var (
		clGroups []group
		clCount  int
	)
	for _, d := range clDirs {
		if cls, ok := dirToCLs[d]; ok {
			clCount += len(cls)
			g := group{Dir: d}
			g.Items = append(g.Items, item{CLs: cls})
			sort.Sort(itemsBySummary(g.Items))
			clGroups = append(clGroups, g)
		}
	}
	s.data.Sections = append(s.data.Sections, section{
		Title:  "Pending CLs",
		Count:  clCount,
		Groups: clGroups,
	})
}

// requires s.cMu be locked.
func (s *server) appendPendingProposals(issueToCLs map[int32][]*maintner.GerritCL) {
	var proposals group
	s.repo.ForeachIssue(func(issue *maintner.GitHubIssue) error {
		if issue.Closed {
			return nil
		}
		if issue.HasLabel(labelProposal) || strings.HasPrefix(issue.Title, prefixProposal) {
			proposals.Items = append(proposals.Items, item{
				Issue: issue,
				CLs:   issueToCLs[issue.Number],
			})
		}
		return nil
	})
	sort.Sort(itemsBySummary(proposals.Items))
	s.data.Sections = append(s.data.Sections, section{
		Title:  "Pending Proposals",
		Count:  len(proposals.Items),
		Groups: []group{proposals},
	})
}

// requires s.cMu be locked.
func (s *server) appendClosedIssues() {
	var (
		closed   group
		lastWeek = time.Now().Add(-(7*24 + 12) * time.Hour)
	)
	s.repo.ForeachIssue(func(issue *maintner.GitHubIssue) error {
		if !issue.Closed {
			return nil
		}
		if issue.Updated.After(lastWeek) {
			closed.Items = append(closed.Items, item{Issue: issue})
		}
		return nil
	})
	sort.Sort(itemsBySummary(closed.Items))
	s.data.Sections = append(s.data.Sections, section{
		Title:  "Closed Last Week",
		Count:  len(closed.Items),
		Groups: []group{closed},
	})
}

// requires s.cMu be read locked.
func (s *server) allMilestones() []milestone {
	var ms []milestone
	s.repo.ForeachMilestone(func(m *maintner.GitHubMilestone) error {
		if m.Closed {
			return nil
		}
		sm := milestoneRE.FindStringSubmatch(m.Title)
		if sm == nil {
			return nil
		}
		major, _ := strconv.Atoi(sm[1])
		minor, _ := strconv.Atoi(sm[3])
		ms = append(ms, milestone{
			title: m.Title,
			major: major,
			minor: minor,
		})
		return nil
	})
	sort.Sort(milestonesByGoVersion(ms))
	return ms
}

// handleRelease serves dev.golang.org/release.
func (s *server) handleRelease(t *template.Template, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.cMu.RLock()
	dirty := s.data.dirty
	s.cMu.RUnlock()
	if dirty {
		s.updateReleaseData()
	}

	s.cMu.RLock()
	defer s.cMu.RUnlock()
	if err := t.Execute(w, s.data); err != nil {
		log.Printf("t.Execute(w, nil) = %v", err)
		return
	}
}
