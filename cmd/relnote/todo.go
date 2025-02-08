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
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
func todo(w io.Writer, goroot string, treeOpenDate time.Time) error {
	// If not provided, determine when the tree was opened by looking
	// at when the version file was updated.
	if treeOpenDate.IsZero() {
		var err error
		treeOpenDate, err = findTreeOpenDate(goroot)
		if err != nil {
			return err
		}
	}
	log.Printf("collecting TODOs from %s since %s", goroot, treeOpenDate.Format(time.DateOnly))

	var todos []ToDo
	addToDo := func(td ToDo) { todos = append(todos, td) }

	mentioned := mentioned{Issues: make(map[int]bool), CLs: make(map[int]bool)}
	nextDir := filepath.Join(goroot, "doc", "next")
	if err := infoFromDocFiles(os.DirFS(nextDir), mentioned, addToDo); err != nil {
		return err
	}
	if err := todosFromCLs(treeOpenDate, mentioned, addToDo); err != nil {
		return err
	}
	return writeToDos(w, todos)
}

// mentioned collects mentions within the existing relnotes.
type mentioned struct {
	Issues map[int]bool
	CLs    map[int]bool
}

func (m mentioned) AddIssue(num int) { m.Issues[num] = true }
func (m mentioned) AddCL(num int)    { m.CLs[num] = true }

// findTreeOpenDate returns the time of the most recent commit to the file that
// determines the version of Go under development.
func findTreeOpenDate(goroot string) (time.Time, error) {
	versionFilePath := filepath.FromSlash("src/internal/goversion/goversion.go")
	if _, err := exec.LookPath("git"); err != nil {
		return time.Time{}, fmt.Errorf("looking for git binary: %v", err)
	}
	// List the most recent commit to versionFilePath, displaying the date and subject.
	outb, err := exec.Command("git", "-C", goroot, "log", "-n", "1",
		"--format=%cs %s", "--", versionFilePath).Output()
	if err != nil {
		return time.Time{}, err
	}
	out := string(outb)
	// The commit messages follow a standard form. Check for the right words to avoid mistakenly
	// choosing the wrong commit.
	const updateString = "update version to"
	if !strings.Contains(strings.ToLower(out), updateString) {
		return time.Time{}, fmt.Errorf("cannot determine tree-open date: most recent commit for %s does not contain %q",
			versionFilePath, updateString)
	}
	dateString, _, _ := strings.Cut(out, " ")
	return time.Parse(time.DateOnly, dateString)
}

// Collect TODOs and issue numbers from the markdown files in the main repo.
func infoFromDocFiles(fsys fs.FS, mentioned mentioned, addToDo func(ToDo)) error {
	// This is essentially a grep.
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			if err := infoFromFile(fsys, path, mentioned, addToDo); err != nil {
				return err
			}
		}
		return nil
	})
}

var (
	issueRE            = regexp.MustCompile("/issue/([1-9][0-9]*)")
	issueViaFilenameRE = regexp.MustCompile(`^\d+-stdlib/\d+-minor/.+/([1-9][0-9]*).md$`)
	clRE               = regexp.MustCompile("CL ([1-9][0-9]*)")
)

func infoFromFile(dir fs.FS, filename string, mentioned mentioned, addToDo func(ToDo)) error {
	if matches := issueViaFilenameRE.FindStringSubmatch(filename); matches != nil {
		num, err := strconv.Atoi(matches[1])
		if err != nil {
			return fmt.Errorf("%s: %v", filename, err)
		}
		mentioned.AddIssue(num)
	}
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
			mentioned.AddIssue(num)
		}
		for _, matches := range clRE.FindAllStringSubmatch(line, -1) {
			num, err := strconv.Atoi(matches[1])
			if err != nil {
				return fmt.Errorf("%s:%d: %v", filename, ln, err)
			}
			mentioned.AddCL(num)
		}
	}
	return scan.Err()
}

func todosFromCLs(cutoff time.Time, mentioned mentioned, add func(ToDo)) error {
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
			if _, ok := matchedCLs[int(cl.Number)]; ok && !mentioned.CLs[int(cl.Number)] {
				if err := todoFromRelnote(ctx, cl, gerritClient, add); err != nil {
					return err
				}
			}
			// Add a TODO if the CL refers to an accepted proposal.
			todoFromProposal(cl, gh, mentioned.Issues, add)
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
			rn = fmt.Sprintf("CL %d has a RELNOTE comment without a suggested text", cl.Number)
		}
		add(ToDo{
			message:    "TODO: " + rn,
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
		slices.Sort(provs) // for deterministic output
		if _, err := fmt.Fprintf(w, "%s (from %s)\n", msg, strings.Join(provs, ", ")); err != nil {
			return err
		}
	}
	return nil
}
