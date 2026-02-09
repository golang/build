// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package dash reads build.golang.org's dashboards.
package dash

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// A Board is a single dashboard.
type Board struct {
	Repo      string   // repo being displayed: "go", "arch", and so on
	Branch    string   // branch in repo
	Builders  []string // builder columns
	Revisions []*Line  // commit lines, newest to oldest
}

// A Line is a single commit line on a Board b.
type Line struct {
	Repo       string    // same as b.Repo
	Branch     string    // same as b.Branch
	Revision   string    // revision of Repo
	GoRevision string    // for Repo != "go", revision of go repo being used
	GoBranch   string    // for Repo != "go", branch of go repo being used
	Date       time.Time // date of commit
	Author     string    // author of commit
	Desc       string    // commit description

	// // Results[i] reports b.Builders[i]'s result:
	// "" (not run), "ok" (passed), or the URL of the failure log
	// ("https://build.golang.org/log/...")
	Results []string
}

// Read reads and returns all the dashboards on build.golang.org
// (for the main repo, the main repo release branches, and subrepos),
// including all results up to the given time limit.
// It guarantees that all the returned boards will have the same b.Builders slices,
// so that any line.Results[i] even for different boards refers to a consistent
// builder for a given i.
func Read(limit time.Time) ([]*Board, error) {
	return Update(nil, limit)
}

// Update is like Read but takes a starting set of boards from
// a previous call to Read or Update and avoids redownloading
// information from those boards.
// It does not modify the boards passed in as input.
func Update(old []*Board, limit time.Time) ([]*Board, error) {
	// Read the front page to derive the Go repo branches and subrepos.
	_, goBranches, repos, err := readPage("", "", 0)
	if err != nil {
		return nil, err
	}
	repos = append([]string{"go"}, repos...)

	// Build cache of existing boards.
	type key struct{ repo, branch string }
	cache := make(map[key]*Board)
	for _, b := range old {
		cache[key{b.Repo, b.Branch}] = b
	}

	// For each repo and branch, fetch that repo's list of board pages.
	var boards []*Board
	var errors []error
	var wg sync.WaitGroup
	for _, r := range repos {
		branches := []string{""}
		if r == "go" {
			branches = goBranches
		}
		for _, branch := range branches {
			if branch == "master" || branch == "main" {
				branch = ""
			}
			// Only read up to what we already have in old, respecting limit.
			old := cache[key{r, branch}]
			oldLimit := limit
			if old != nil && len(old.Revisions) > 0 && old.Revisions[0].Date.After(limit) {
				oldLimit = old.Revisions[0].Date
			}
			i := len(boards)
			boards = append(boards, nil)
			errors = append(errors, nil)
			wg.Add(1)
			go func() {
				defer wg.Done()
				boards[i], errors[i] = readRepo(r, branch, oldLimit)
				if errors[i] == nil {
					boards[i] = update(boards[i], old, limit)
				}
			}()
		}
	}
	wg.Wait()

	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}

	// Remap all the boards to have a consistent Builders array.
	// It is slightly inefficient that readRepo does this remap as well,
	// but all the downloads take more time.
	remap(boards)

	return boards, nil
}

// update returns the result of merging b and old,
// discarding revisions older than limit and removing duplicates.
// It modifies b but not old.
func update(b, old *Board, limit time.Time) *Board {
	if old == nil || !same(b.Builders, old.Builders) {
		if old == nil {
			old = new(Board)
		} else {
			old = old.clone()
		}
		remap([]*Board{b, old})
	}

	type key struct {
		rev   string
		gorev string
	}
	have := make(map[key]bool)
	keep := b.Revisions[:0]
	for _, list := range [][]*Line{b.Revisions, old.Revisions} {
		for _, r := range list {
			if !r.Date.Before(limit) && !have[key{r.Revision, r.GoRevision}] {
				have[key{r.Revision, r.GoRevision}] = true
				keep = append(keep, r)
			}
		}
	}
	b.Revisions = keep
	return b
}

// clone returns a deep copy of b.
func (b *Board) clone() *Board {
	b1 := &Board{
		Repo:      b.Repo,
		Branch:    b.Branch,
		Builders:  make([]string, len(b.Builders)),
		Revisions: make([]*Line, len(b.Revisions)),
	}
	copy(b1.Builders, b.Builders)
	for i := range b1.Revisions {
		r := new(Line)
		*r = *b.Revisions[i]
		results := make([]string, len(r.Results))
		copy(results, r.Results)
		r.Results = results
		b1.Revisions[i] = r
	}
	return b1
}

// readRepo reads and returns the pages for the given repo and branch,
// stopping when it finds a page that contains no results newer than limit.
func readRepo(repo, branch string, limit time.Time) (*Board, error) {
	path := ""
	if repo != "go" {
		path = "golang.org/x/" + repo
	}
	var pages []*Board
	for page := 0; ; page++ {
		b, _, _, err := readPage(path, branch, page)
		if err != nil {
			return merge(pages), err
		}

		// If there's nothing new enough on the whole page, stop.
		keep := b.Revisions[:0]
		for _, r := range b.Revisions {
			if !r.Date.Before(limit) {
				keep = append(keep, r)
			}
		}
		if len(keep) == 0 {
			break
		}
		b.Revisions = keep
		b.Repo = repo
		b.Branch = branch
		pages = append(pages, b)
	}
	return merge(pages), nil
}

// merge merges all the pages into a single board.
func merge(pages []*Board) *Board {
	if len(pages) == 0 {
		return new(Board)
	}

	remap(pages)
	for _, b := range pages {
		if !same(b.Builders, pages[0].Builders) || b.Repo != pages[0].Repo || b.Branch != pages[0].Branch {
			panic("misuse of merge")
		}
	}

	merged := &Board{Repo: pages[0].Repo, Branch: pages[0].Branch, Builders: pages[0].Builders}
	for _, b := range pages {
		merged.Revisions = append(merged.Revisions, b.Revisions...)
	}
	return merged
}

// remap remaps all the results in all the boards
// to use a consistent set of Builders.
func remap(boards []*Board) {
	// Collect list of all builders across all boards.
	var builders []string
	index := make(map[string]int)
	for _, b := range boards {
		for _, builder := range b.Builders {
			if index[builder] == 0 {
				index[builder] = 1
				builders = append(builders, builder)
			}
		}
	}
	sort.Strings(builders)
	for i, builder := range builders {
		index[builder] = i
	}

	// Remap.
	for _, b := range boards {
		for _, r := range b.Revisions {
			results := make([]string, len(builders))
			for i, ok := range r.Results {
				results[index[b.Builders[i]]] = ok
			}
			r.Results = results
		}
		b.Builders = builders
	}
}

// readPage reads the build.golang.org page for repo, branch.
// It returns the board on that page.
// When repo == "go" and branch == "" and page == 0,
// build.golang.org also sends back information about the
// other go repo branches and the subrepos.
// readPage("go", "", 0) returns those lists of go branches
// and subrepos as extra results.
func readPage(repo, branch string, page int) (b *Board, branches, repos []string, err error) {
	if repo == "" {
		repo = "go"
	}
	u := "https://build.golang.org/?mode=json&repo=" + url.QueryEscape(repo) + "&branch=" + url.QueryEscape(branch) + "&page=" + fmt.Sprint(page)
	log.Printf("read %v", u)
	resp, err := http.Get(u)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s page %d: %v", repo, page, err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s page %d: %v", repo, page, err)
	}
	if resp.StatusCode != 200 {
		return nil, nil, nil, fmt.Errorf("%s page %d: %s\n%s", repo, page, resp.Status, data)
	}

	b = new(Board)
	if err := json.Unmarshal(data, b); err != nil {
		return nil, nil, nil, fmt.Errorf("%s page %d: %v", repo, page, err)
	}

	// Use empty string consistently to denote master/main branch.
	for _, r := range b.Revisions {
		if r.Branch == "master" || r.Branch == "main" {
			r.Branch = ""
		}
		if r.GoBranch == "master" || r.GoBranch == "main" {
			r.GoBranch = ""
		}
	}

	// https://build.golang.org/?mode=json (main repo, no branch, page 0)
	// sends back a bit about the subrepos too. Filter that out.
	if repo == "go" {
		var save []*Line
		for _, r := range b.Revisions {
			if r.Repo == "go" {
				save = append(save, r)
			} else {
				branches = append(branches, r.GoBranch)
				repos = append(repos, r.Repo)
			}
		}
		b.Revisions = save
		branches = uniq(branches)
		repos = uniq(repos)
	}

	return b, branches, repos, nil
}

// same reports whether x and y are the same slice.
func same(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	for i, s := range x {
		if y[i] != s {
			return false
		}
	}
	return true
}

// uniq sorts and removes duplicates from list, returning the result.
// uniq reuses list's storage for its result.
func uniq(list []string) []string {
	sort.Strings(list)
	keep := list[:0]
	for _, s := range list {
		if len(keep) == 0 || s != keep[len(keep)-1] {
			keep = append(keep, s)
		}
	}
	return keep
}
