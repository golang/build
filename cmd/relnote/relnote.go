// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The relnote command summarizes the Go changes in Gerrit marked with
// RELNOTE annotations for the release notes.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/build/repos"
)

var (
	htmlMode = flag.Bool("html", false, "write HTML output")
	exclFile = flag.String("exclude-from", "", "optional path to release notes HTML file. If specified, any 'CL NNNN' occurence in the content will cause that CL to be excluded from this tool's output.")
)

// change is a change that was noted via a RELNOTE= comment.
type change struct {
	CL   *maintner.GerritCL
	Note string // the part after RELNOTE=
}

func (c change) TextLine() string {
	subj := c.CL.Subject()
	if c.Note != "yes" && c.Note != "y" {
		subj = c.Note + ": " + subj
	}
	return fmt.Sprintf("https://golang.org/cl/%d: %s", c.CL.Number, subj)
}

func main() {
	flag.Parse()

	// Releases are every 6 months. Walk forward by 6 month increments to next release.
	cutoff := time.Date(2016, time.August, 1, 00, 00, 00, 0, time.UTC)
	now := time.Now()
	for cutoff.Before(now) {
		cutoff = cutoff.AddDate(0, 6, 0)
	}

	// Previous release was 6 months earlier.
	cutoff = cutoff.AddDate(0, -6, 0)

	// The maintner corpus doesn't track inline comments. See golang.org/issue/24863.
	// So we need to use a Gerrit API client to fetch them instead. If maintner starts
	// tracking inline comments in the future, this extra complexity can be dropped.
	gerritClient := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	matchedCLs, err := findCLsWithRelNote(gerritClient, cutoff)
	if err != nil {
		log.Fatal(err)
	}

	var existingHTML []byte
	if *exclFile != "" {
		var err error
		existingHTML, err = ioutil.ReadFile(*exclFile)
		if err != nil {
			log.Fatal(err)
		}
	}

	corpus, err := godata.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	changes := map[string][]change{} // keyed by pkg
	corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
			if cl.Status != "merged" {
				return nil
			}
			if cl.Branch() != "master" {
				// Ignore CLs sent to development or release branches.
				return nil
			}
			if cl.Commit.CommitTime.Before(cutoff) {
				// Was in a previous release; not for this one.
				return nil
			}
			_, ok := matchedCLs[int(cl.Number)]
			if !ok {
				// Wasn't matched by the Gerrit API search query.
				// Return before making further Gerrit API calls.
				return nil
			}
			comments, err := gerritClient.ListChangeComments(context.Background(), fmt.Sprint(cl.Number))
			if err != nil {
				return err
			}
			relnote := clRelNote(cl, comments)
			if relnote == "" ||
				bytes.Contains(existingHTML, []byte(fmt.Sprintf("CL %d", cl.Number))) {
				return nil
			}
			pkg := clPackage(cl)
			changes[pkg] = append(changes[pkg], change{
				Note: relnote,
				CL:   cl,
			})
			return nil
		})
		return nil
	})

	var pkgs []string
	for pkg, changes := range changes {
		pkgs = append(pkgs, pkg)
		sort.Slice(changes, func(i, j int) bool {
			return changes[i].CL.Number < changes[j].CL.Number
		})
	}
	sort.Strings(pkgs)

	if *htmlMode {
		for _, pkg := range pkgs {
			if !strings.HasPrefix(pkg, "cmd/") {
				continue
			}
			for _, change := range changes[pkg] {
				fmt.Printf("<!-- CL %d: %s -->\n", change.CL.Number, change.TextLine())
			}
		}
		for _, pkg := range pkgs {
			if strings.HasPrefix(pkg, "cmd/") {
				continue
			}
			fmt.Printf("\n<dl id=%q><dt><a href=%q>%s</a></dt>\n  <dd>",
				pkg, "/pkg/"+pkg+"/", pkg)
			for _, change := range changes[pkg] {
				changeURL := fmt.Sprintf("https://golang.org/cl/%d", change.CL.Number)
				subj := change.CL.Subject()
				subj = strings.TrimPrefix(subj, pkg+": ")
				fmt.Printf("\n    <p><!-- CL %d -->\n      TODO: <a href=%q>%s</a>: %s\n    </p>\n",
					change.CL.Number, changeURL, changeURL, html.EscapeString(subj))
			}
			fmt.Printf("  </dd>\n</dl><!-- %s -->\n", pkg)
		}
	} else {
		for _, pkg := range pkgs {
			fmt.Printf("%s\n", pkg)
			for _, change := range changes[pkg] {
				fmt.Printf("  %s\n", change.TextLine())
			}
		}
	}
}

// findCLsWithRelNote finds CLs that contain a RELNOTE marker by
// using a Gerrit API client. Returned map is keyed by CL number.
func findCLsWithRelNote(client *gerrit.Client, since time.Time) (map[int]*gerrit.ChangeInfo, error) {
	// Gerrit search operators are documented at
	// https://gerrit-review.googlesource.com/Documentation/user-search.html#search-operators.
	query := fmt.Sprintf(`status:merged branch:master since:%s (comment:"RELNOTE" OR comment:"RELNOTES")`,
		since.Format("2006-01-02"))
	cs, err := client.QueryChanges(context.Background(), query)
	if err != nil {
		return nil, err
	}
	m := make(map[int]*gerrit.ChangeInfo) // CL Number → CL.
	for _, c := range cs {
		m[c.ChangeNumber] = c
	}
	return m, nil
}

// clPackage returns the package import path from the CL's commit message,
// or "??" if it's formatted unconventionally.
func clPackage(cl *maintner.GerritCL) string {
	var pkg string
	if i := strings.Index(cl.Subject(), ":"); i == -1 {
		return "??"
	} else {
		pkg = cl.Subject()[:i]
	}
	if r := repos.ByGerritProject[cl.Project.Project()]; r == nil {
		return "??"
	} else {
		pkg = path.Join(r.ImportPath, pkg)
	}
	return pkg
}

// clRelNote extracts a RELNOTE note from a Gerrit CL commit
// message and any inline comments. If there isn't a RELNOTE
// note, it returns the empty string.
func clRelNote(cl *maintner.GerritCL, comments map[string][]gerrit.CommentInfo) string {
	msg := cl.Commit.Msg
	if strings.Contains(msg, "RELNOTE") {
		return parseRelNote(msg)
	}
	// Since July 2020, Gerrit UI has replaced top-level comments
	// with patchset-level inline comments, so don't bother looking
	// for RELNOTE= in cl.Messages—there won't be any. Instead, do
	// look through all inline comments that we got via Gerrit API.
	for _, cs := range comments {
		for _, c := range cs {
			if strings.Contains(c.Message, "RELNOTE") {
				return parseRelNote(c.Message)
			}
		}
	}
	return ""
}

// parseRelNote parses a RELNOTE annotation from the string s.
// It returns the empty string if no such annotation exists.
func parseRelNote(s string) string {
	m := relNoteRx.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

var relNoteRx = regexp.MustCompile(`RELNOTES?=(.+)`)
