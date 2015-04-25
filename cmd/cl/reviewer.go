// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"log"
	"os/exec"
	"sort"
	"strings"
)

// Adapted from git-codereview/mail.go, but uses Author lines
// in addition to Reviewed-By lines. The effect should be the same,
// since the most common reviewers are the most common authors too,
// but admitting authors lets us shorten CL owners too.

// reviewers is the list of reviewers for the current repository,
// sorted by how many reviews each has done.
var reviewers []reviewer

const ReviewScale = 1000000000

type reviewer struct {
	addr  string
	count int64 // #reviews * ReviewScale + #CLs
}

var mailLookup = map[string]string{} // rsc -> rsc@golang.org
var isReviewer = map[string]bool{}   // rsc@golang.org -> true

// loadReviewers reads the reviewer list from the current git repo
// and leaves it in the global variable reviewers.
// See the comment on mailLookup for a description of how the
// list is generated and used.
func loadReviewers() {
	if reviewers != nil {
		return
	}
	output, err := exec.Command("go", "env", "GOROOT").CombinedOutput()
	if err != nil {
		log.Fatalf("go env GOROOT: %v\n%s", err, output)
	}
	goroot := strings.TrimSpace(string(output))
	cmd := exec.Command("git", "log", "--format=format:Author: <%aE>%n%B")
	cmd.Dir = goroot
	output, err = cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("git log: %v\n%s", err, output)
	}
	countByAddr := map[string]int64{}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "Reviewed-by:") || strings.HasPrefix(line, "Author:") {
			delta := int64(1)
			if strings.HasPrefix(line, "Reviewed-by:") {
				delta = ReviewScale
			}
			f := strings.Fields(line)
			addr := f[len(f)-1]
			if strings.HasPrefix(addr, "<") && strings.Contains(addr, "@") && strings.HasSuffix(addr, ">") {
				email := addr[1 : len(addr)-1]
				countByAddr[email] += delta
				if delta == ReviewScale {
					isReviewer[email] = true
				}
			}
		}
	}

	reviewers = []reviewer{}
	for addr, count := range countByAddr {
		reviewers = append(reviewers, reviewer{addr, count})
	}
	sort.Sort(reviewersByCount(reviewers))

	for _, r := range reviewers {
		short := r.addr
		if i := strings.Index(short, "@"); i >= 0 {
			short = short[:i]
		}
		if mailLookup[short] == "" {
			mailLookup[short] = r.addr
		}
	}
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
