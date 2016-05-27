// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package godash

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// reviewScale is the relative weight of a single review compared to
// this many authored CLs.
const reviewScale = 1000000000

type reviewer struct {
	addr  string
	count int64 // #reviews * reviewScale + #CLs
}

type reviewersByCount []reviewer

func (x reviewersByCount) Len() int      { return len(x) }
func (x reviewersByCount) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x reviewersByCount) Less(i, j int) bool {
	if x[i].count != x[j].count {
		return x[i].count > x[j].count
	}
	return x[i].addr < x[j].addr
}

// Reviewers tracks the popularity of reviewers in a Git
// repository. It can be used to resolve e-mail addresses into short
// names and vice versa.
type Reviewers struct {
	// data contains the information that should be serialized if
	// the Reviewers struct is serialized. (MarshalJSON and
	// UnmarshalJSON will transparently use data instead of the
	// full Reviewers struct.)
	data struct {
		// IsReviewer maps full e-mail addresses to booleans.
		IsReviewer map[string]bool // rsc@golang.org -> true
		// CountByAddr maps full e-mail address to a score of
		// the number of CLs authored and reviewed.
		CountByAddr map[string]int64
		// GitHubByAddr maps full e-mail address to GitHub username.
		GitHubByAddr map[string]string
		// LastSHA and LastTime track the SHA and time of the
		// last commit included in these stats.
		LastSHA  string
		LastTime time.Time
	}
	// addrByGitHub maps GitHub usernames to preferred address
	// (the address with the highest review count).
	addrByGitHub map[string]string
	// mailLookup maps short names to full e-mail addresses.
	mailLookup map[string]string // rsc -> rsc@golang.org
}

// IsReviewer reports whether the provided address is a known reviewer.
func (r *Reviewers) IsReviewer(addr string) bool {
	return r.data.IsReviewer[addr]
}

// Shorten will potentially shorten a full e-mail address if the short
// version maps back to that full address.
func (r *Reviewers) Shorten(addr string) string {
	if i := strings.Index(addr, "@"); i >= 0 {
		if r.mailLookup[addr[:i]] == addr {
			return addr[:i]
		}
	}
	return addr
}

// Resolve takes a short username and returns the matching full e-mail
// address, or "" if the username could not be resolved.
func (r *Reviewers) Resolve(short string) string {
	return r.mailLookup[short]
}

// Preferred takes an address and returns the preferred e-mail address
// for that user, which may be the same. It does this by resolving the
// GitHub username and then returning the address most-used for
// commits on that username.
func (r *Reviewers) Preferred(addr string) string {
	if out := r.addrByGitHub[r.data.GitHubByAddr[addr]]; out != "" {
		return out
	}
	return addr
}

// ResolveGitHub takes a GitHub login name and returns the matching
// full e-mail address, or "" if the name could not be resolved.
func (r *Reviewers) ResolveGitHub(login string) string {
	return r.addrByGitHub[login]
}

// add increments a reviewer's count. recalculate must be called to
// regenerate the mail lookup table.
func (r *Reviewers) add(addr string, isReviewer bool) {
	if !strings.Contains(addr, "@") {
		return
	}
	if r.data.IsReviewer == nil {
		r.data.IsReviewer = make(map[string]bool)
	}
	if r.data.CountByAddr == nil {
		r.data.CountByAddr = make(map[string]int64)
	}
	if r.data.GitHubByAddr == nil {
		r.data.GitHubByAddr = make(map[string]string)
	}
	if isReviewer {
		r.data.IsReviewer[addr] = true
		r.data.CountByAddr[addr] += reviewScale
	} else {
		r.data.CountByAddr[addr] += 1
	}
}

func (r *Reviewers) recalculate() {
	reviewers := []reviewer{}
	for addr, count := range r.data.CountByAddr {
		reviewers = append(reviewers, reviewer{addr, count})
	}
	sort.Sort(reviewersByCount(reviewers))
	r.mailLookup = map[string]string{}
	for _, rev := range reviewers {
		short := rev.addr
		if i := strings.Index(short, "@"); i >= 0 {
			short = short[:i]
		}
		if r.mailLookup[short] == "" {
			r.mailLookup[short] = rev.addr
		}
	}
	r.addrByGitHub = map[string]string{}
	for addr, user := range r.data.GitHubByAddr {
		if r.addrByGitHub[user] == "" || r.data.CountByAddr[r.addrByGitHub[user]] < r.data.CountByAddr[addr] {
			r.addrByGitHub[user] = addr
		}
	}
}

func (r *Reviewers) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.data)
}

func (r *Reviewers) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &r.data); err != nil {
		return err
	}
	r.recalculate()
	return nil
}

func (r *Reviewers) GobEncode() ([]byte, error) {
	var out bytes.Buffer
	e := gob.NewEncoder(&out)
	if err := e.Encode(&r.data); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (r *Reviewers) GobDecode(b []byte) error {
	d := gob.NewDecoder(bytes.NewBuffer(b))
	if err := d.Decode(&r.data); err != nil {
		return err
	}
	r.recalculate()
	return nil
}
