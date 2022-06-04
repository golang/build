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
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/build/repos"
)

var (
	verbose  = flag.Bool("v", false, "print verbose logging")
	htmlMode = flag.Bool("html", false, "write HTML output")
	exclFile = flag.String("exclude-from", "", "optional path to release notes HTML file. If specified, any 'CL NNNN' occurrence in the content will cause that CL to be excluded from this tool's output.")
)

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

func main() {
	log.SetPrefix("relnote: ")
	log.SetFlags(0)
	flag.Parse()

	// Releases are every 6 months. Walk forward by 6 month increments to next release.
	cutoff := time.Date(2016, time.August, 1, 00, 00, 00, 0, time.UTC)
	now := time.Now()
	for cutoff.Before(now) {
		cutoff = cutoff.AddDate(0, 6, 0)
	}

	// Previous release was 6 months earlier.
	cutoff = cutoff.AddDate(0, -6, 0)

	// The maintner corpus doesn't track inline comments. See go.dev/issue/24863.
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
	gh := corpus.GitHub().Repo("golang", "go")
	addedIssue := make(map[int32]bool)
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
			for _, num := range issueNumbers(cl) {
				if bytes.Contains(existingHTML, []byte(fmt.Sprintf("https://go.dev/issue/%d", num))) || addedIssue[num] {
					continue
				}
				if issue := gh.Issue(num); issue != nil && !issue.ClosedAt.Before(cutoff) && hasLabel(issue, "Proposal-Accepted") {
					if *verbose {
						log.Printf("CL %d mentions accepted proposal #%d (%s)", cl.Number, num, issue.Title)
					}
					pkg := issuePackage(issue)
					changes[pkg] = append(changes[pkg], change{Issue: issue})
					addedIssue[num] = true
				}
			}
			if bytes.Contains(existingHTML, []byte(fmt.Sprintf("CL %d", cl.Number))) {
				return nil
			}
			var relnote string
			if _, ok := matchedCLs[int(cl.Number)]; ok {
				comments, err := gerritClient.ListChangeComments(context.Background(), fmt.Sprint(cl.Number))
				if err != nil {
					return err
				}
				relnote = clRelNote(cl, comments)
			}
			if relnote == "" {
				// Invent a RELNOTE=modified api/name.txt if the commit modifies any API files.
				var api []string
				for _, f := range cl.Commit.Files {
					if strings.HasPrefix(f.File, "api/") && strings.HasSuffix(f.File, ".txt") {
						api = append(api, f.File)
					}
				}
				if len(api) > 0 {
					relnote = "modified " + strings.Join(api, ", ")
					if *verbose {
						log.Printf("CL %d %s", cl.Number, relnote)
					}
				}
			}
			if relnote != "" {
				pkg := clPackage(cl)
				changes[pkg] = append(changes[pkg], change{
					Note: relnote,
					CL:   cl,
				})
			}
			return nil
		})
		return nil
	})

	var pkgs []string
	for pkg, changes := range changes {
		pkgs = append(pkgs, pkg)
		sort.Slice(changes, func(i, j int) bool {
			x, y := &changes[i], &changes[j]
			if (x.Issue != nil) != (y.Issue != nil) {
				return x.Issue != nil
			}
			if x.Issue != nil {
				return x.Issue.Number < y.Issue.Number
			}
			return x.CL.Number < y.CL.Number
		})
	}
	sort.Strings(pkgs)

	if *htmlMode {
		for _, pkg := range pkgs {
			if !strings.HasPrefix(pkg, "cmd/") {
				continue
			}
			for _, change := range changes[pkg] {
				fmt.Printf("<!-- %s: %s -->\n", change.ID(), change.TextLine())
			}
		}
		for _, pkg := range pkgs {
			if strings.HasPrefix(pkg, "cmd/") {
				continue
			}
			fmt.Printf("\n<dl id=%q><dt><a href=%q>%s</a></dt>\n  <dd>",
				pkg, "/pkg/"+pkg+"/", pkg)
			for _, change := range changes[pkg] {
				fmt.Printf("\n    <p><!-- %s -->\n      TODO: <a href=%q>%s</a>: %s\n    </p>\n",
					change.ID(), change.URL(), change.URL(), html.EscapeString(change.TextLine()))
			}
			fmt.Printf("  </dd>\n</dl><!-- %s -->\n", pkg)
		}
	} else {
		for _, pkg := range pkgs {
			fmt.Printf("%s\n", pkg)
			for _, change := range changes[pkg] {
				fmt.Printf("  %s: %s\n", change.URL(), change.TextLine())
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

var numbersRE = regexp.MustCompile(`(?m)(?:^|\s)#([0-9]{3,})`)
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
	return list
}
