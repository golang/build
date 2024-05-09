// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/maintner"
	"golang.org/x/build/maintner/godata"
	"golang.org/x/exp/maps"
)

type ToDo struct {
	message    string // what is to be done
	provenance string // where the TODO came from
}

// todo prints a report to w on which release notes need to be written.
// It takes the doc/next directory of the repo and the date of the last release.
func todo(w io.Writer, fsys fs.FS, prevRelDate time.Time) error {
	var todos []ToDo
	addToDo := func(td ToDo) { todos = append(todos, td) }

	mentionedIssues := map[int]bool{} // issues mentioned in the existing relnotes
	addIssue := func(num int) { mentionedIssues[num] = true }

	if err := infoFromDocFiles(fsys, addToDo, addIssue); err != nil {
		return err
	}
	if !prevRelDate.IsZero() {
		if err := todosFromCLs(prevRelDate, mentionedIssues, addToDo); err != nil {
			return err
		}
	}
	return writeToDos(w, todos)
}

// Collect TODOs and issue numbers from the markdown files in the main repo.
func infoFromDocFiles(fsys fs.FS, addToDo func(ToDo), addIssue func(int)) error {
	// This is essentially a grep.
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			if err := infoFromFile(fsys, path, addToDo, addIssue); err != nil {
				return err
			}
		}
		return nil
	})
}

var issueRE = regexp.MustCompile("/issue/([0-9]+)")

func infoFromFile(dir fs.FS, filename string, addToDo func(ToDo), addIssue func(int)) error {
	f, err := dir.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	ln := 0
	for scan.Scan() {
		ln++
		line := scan.Text()
		if strings.Contains(line, "TODO") {
			addToDo(ToDo{
				message:    line,
				provenance: fmt.Sprintf("%s:%d", filename, ln),
			})
		}
		for _, matches := range issueRE.FindAllStringSubmatch(line, -1) {
			num, err := strconv.Atoi(matches[1])
			if err != nil {
				return fmt.Errorf("%s:%d: %v", filename, ln, err)
			}
			addIssue(num)
		}
	}
	return scan.Err()
}

func todosFromCLs(cutoff time.Time, mentionedIssues map[int]bool, add func(ToDo)) error {
	ctx := context.Background()
	// The maintner corpus doesn't track inline comments. See go.dev/issue/24863.
	// So we need to use a Gerrit API client to fetch them instead. If maintner starts
	// tracking inline comments in the future, this extra complexity can be dropped.
	gerritClient := gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	matchedCLs, err := findCLsWithRelNote(gerritClient, cutoff)
	if err != nil {
		return err
	}
	corpus, err := godata.Get(ctx)
	if err != nil {
		return err
	}
	gh := corpus.GitHub().Repo("golang", "go")
	return corpus.Gerrit().ForeachProjectUnsorted(func(gp *maintner.GerritProject) error {
		if gp.Server() != "go.googlesource.com" {
			return nil
		}
		return gp.ForeachCLUnsorted(func(cl *maintner.GerritCL) error {
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
			// Add a TODO if the CL has a "RELNOTE=" comment.
			// These are deprecated, but we look for them just in case.
			if _, ok := matchedCLs[int(cl.Number)]; ok {
				if err := todoFromRelnote(ctx, cl, gerritClient, add); err != nil {
					return err
				}
			}
			// Add a TODO if the CL refers to an accepted proposal.
			todoFromProposal(cl, gh, mentionedIssues, add)
			return nil
		})
	})
}

func todoFromRelnote(ctx context.Context, cl *maintner.GerritCL, gc *gerrit.Client, add func(ToDo)) error {
	comments, err := gc.ListChangeComments(ctx, fmt.Sprint(cl.Number))
	if err != nil {
		return err
	}
	if rn := clRelNote(cl, comments); rn != "" {
		if rn == "yes" || rn == "y" {
			rn = "UNKNOWN"
		}
		add(ToDo{
			message:    "TODO:" + rn,
			provenance: fmt.Sprintf("RELNOTE comment in https://go.dev/cl/%d", cl.Number),
		})
	}
	return nil
}

func todoFromProposal(cl *maintner.GerritCL, gh *maintner.GitHubRepo, mentionedIssues map[int]bool, add func(ToDo)) {
	for _, num := range issueNumbers(cl) {
		if mentionedIssues[num] {
			continue
		}
		if issue := gh.Issue(int32(num)); issue != nil && hasLabel(issue, "Proposal-Accepted") {
			// Add a TODO for all issues, regardless of when or whether they are closed.
			// Any work on an accepted proposal is potentially worthy of a release note.
			add(ToDo{
				message:    fmt.Sprintf("TODO: accepted proposal https://go.dev/issue/%d", num),
				provenance: fmt.Sprintf("https://go.dev/cl/%d", cl.Number),
			})
		}
	}
}

func hasLabel(issue *maintner.GitHubIssue, label string) bool {
	for _, l := range issue.Labels {
		if l.Name == label {
			return true
		}
	}
	return false
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

var relNoteRx = regexp.MustCompile(`RELNOTES?=(.+)`)

// parseRelNote parses a RELNOTE annotation from the string s.
// It returns the empty string if no such annotation exists.
func parseRelNote(s string) string {
	m := relNoteRx.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

var numbersRE = regexp.MustCompile(`(?m)(?:^|\s|golang/go)#([0-9]{3,})`)
var golangGoNumbersRE = regexp.MustCompile(`(?m)golang/go#([0-9]{3,})`)

// issueNumbers returns the golang/go issue numbers referred to by the CL.
func issueNumbers(cl *maintner.GerritCL) []int {
	var re *regexp.Regexp
	if cl.Project.Project() == "go" {
		re = numbersRE
	} else {
		re = golangGoNumbersRE
	}

	var list []int
	for _, s := range re.FindAllStringSubmatch(cl.Commit.Msg, -1) {
		if n, err := strconv.Atoi(s[1]); err == nil && n < 1e9 {
			list = append(list, n)
		}
	}
	// Remove duplicates.
	slices.Sort(list)
	return slices.Compact(list)
}

func writeToDos(w io.Writer, todos []ToDo) error {
	// Group TODOs with the same message. This simplifies the output when a single
	// issue is implemented by multiple CLs.
	byMessage := map[string][]ToDo{}
	for _, td := range todos {
		byMessage[td.message] = append(byMessage[td.message], td)
	}
	msgs := maps.Keys(byMessage)
	slices.Sort(msgs) // for deterministic output
	for _, msg := range msgs {
		var provs []string
		for _, td := range byMessage[msg] {
			provs = append(provs, td.provenance)
		}
		if _, err := fmt.Fprintf(w, "%s (from %s)\n", msg, strings.Join(provs, ", ")); err != nil {
			return err
		}
	}
	return nil
}
