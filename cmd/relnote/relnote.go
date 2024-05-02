// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The relnote command works with release notes.
// It can be used to look for unfinished notes and to generate the
// final markdown file.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/repos"
)

var verbose = flag.Bool("v", false, "print verbose logging")

// change is a change that was noted via a RELNOTE= comment.
type change struct {
	CL    *maintner.GerritCL
	Note  string // the part after RELNOTE=
	Issue *maintner.GitHubIssue
}

func (c change) ID() string {
	switch {
	default:
		panic("invalid change")
	case c.CL != nil:
		return fmt.Sprintf("CL %d", c.CL.Number)
	case c.Issue != nil:
		return fmt.Sprintf("https://go.dev/issue/%d", c.Issue.Number)
	}
}

func (c change) URL() string {
	switch {
	default:
		panic("invalid change")
	case c.CL != nil:
		return fmt.Sprint("https://go.dev/cl/", c.CL.Number)
	case c.Issue != nil:
		return fmt.Sprint("https://go.dev/issue/", c.Issue.Number)
	}
}

func (c change) Subject() string {
	switch {
	default:
		panic("invalid change")
	case c.CL != nil:
		subj := c.CL.Subject()
		subj = strings.TrimPrefix(subj, clPackage(c.CL)+":")
		return strings.TrimSpace(subj)
	case c.Issue != nil:
		return issueSubject(c.Issue)
	}
}

func (c change) TextLine() string {
	switch {
	default:
		panic("invalid change")
	case c.CL != nil:
		subj := c.CL.Subject()
		if c.Note != "yes" && c.Note != "y" {
			subj += "; " + c.Note
		}
		return subj
	case c.Issue != nil:
		return issueSubject(c.Issue)
	}
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "usage:\n")
	fmt.Fprintf(out, "   relnote generate [GOROOT]\n")
	fmt.Fprintf(out, "      generate release notes from doc/next under GOROOT (default: runtime.GOROOT())\n")
	fmt.Fprintf(out, "   relnote todo PREVIOUS_RELEASE_DATE\n")
	fmt.Fprintf(out, "      report which release notes need to be written; use YYYY-MM-DD format for date of last release\n")
	flag.PrintDefaults()
}

func main() {
	log.SetPrefix("relnote: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	goroot := runtime.GOROOT()
	if goroot == "" {
		log.Fatalf("missing GOROOT")
	}

	// Read internal/goversion to find the next release.
	data, err := os.ReadFile(filepath.Join(goroot, "src/internal/goversion/goversion.go"))
	if err != nil {
		log.Fatal(err)
	}
	m := regexp.MustCompile(`Version = (\d+)`).FindStringSubmatch(string(data))
	if m == nil {
		log.Fatalf("cannot find Version in src/internal/goversion/goversion.go")
	}
	version := m[1]

	// Dispatch to a subcommand if one is provided.
	if cmd := flag.Arg(0); cmd != "" {
		switch cmd {
		case "generate":
			err = generate(version, flag.Arg(1))
		case "todo":
			prevDate := flag.Arg(1)
			if prevDate == "" {
				log.Fatal("need previous release date")
			}
			prevDateTime, err := time.Parse("2006-01-02", prevDate)
			if err != nil {
				log.Fatalf("previous release date: %s", err)
			}
			nextDir := filepath.Join(goroot, "doc", "next")
			err = todo(os.Stdout, os.DirFS(nextDir), prevDateTime)
		default:
			err = fmt.Errorf("unknown command %q", cmd)
		}
		if err != nil {
			log.Fatal(err)
		}
	} else {
		usage()
		log.Fatal("missing subcommand")
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

// packagePrefix returns the package prefix at the start of s.
// For example packagePrefix("net/http: add HTTP 5 support") == "net/http".
// If there's no package prefix, packagePrefix returns "".
func packagePrefix(s string) string {
	i := strings.Index(s, ":")
	if i < 0 {
		return ""
	}
	s = s[:i]
	if strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789/") != "" {
		return ""
	}
	return s
}

// clPackage returns the package import path from the CL's commit message,
// or "??" if it's formatted unconventionally.
func clPackage(cl *maintner.GerritCL) string {
	pkg := packagePrefix(cl.Subject())
	if pkg == "" {
		return "??"
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

// issuePackage returns the package import path from the issue's title,
// or "??" if it's formatted unconventionally.
func issuePackage(issue *maintner.GitHubIssue) string {
	pkg := packagePrefix(issue.Title)
	if pkg == "" {
		return "??"
	}
	return pkg
}

// issueSubject returns the issue's title with the package prefix removed.
func issueSubject(issue *maintner.GitHubIssue) string {
	pkg := packagePrefix(issue.Title)
	if pkg == "" {
		return issue.Title
	}
	return strings.TrimSpace(strings.TrimPrefix(issue.Title, pkg+":"))
}

func hasLabel(issue *maintner.GitHubIssue, label string) bool {
	for _, l := range issue.Labels {
		if l.Name == label {
			return true
		}
	}
	return false
}

var numbersRE = regexp.MustCompile(`(?m)(?:^|\s|golang/go)#([0-9]{3,})`)
var golangGoNumbersRE = regexp.MustCompile(`(?m)golang/go#([0-9]{3,})`)

// issueNumbers returns the golang/go issue numbers referred to by the CL.
func issueNumbers(cl *maintner.GerritCL) []int32 {
	var re *regexp.Regexp
	if cl.Project.Project() == "go" {
		re = numbersRE
	} else {
		re = golangGoNumbersRE
	}

	var list []int32
	for _, s := range re.FindAllStringSubmatch(cl.Commit.Msg, -1) {
		if n, err := strconv.Atoi(s[1]); err == nil && n < 1e9 {
			list = append(list, int32(n))
		}
	}
	// Remove duplicates.
	slices.Sort(list)
	return slices.Compact(list)
}
