// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/build/cmd/watchflakes/internal/script"
	"rsc.io/github"
)

// An Issue is a single GitHub issue in the Test Flakes project:
// a plain github.Issue plus our associated data.
type Issue struct {
	*github.Issue
	ScriptText string         // extracted watchflakes script
	Script     *script.Script // compiled script

	// initialized by readComments
	Stale    bool                   // issue comments may be stale
	Comments []*github.IssueComment // all issue comments
	NewBody  bool                   // issue body (containing script) is newer than last watchflakes comment
	Mentions map[string]bool        // log URLs that have already been posted in watchflakes comments

	// what to send back to the issue
	Error string         // error message (markdown) to post back to issue
	Post  []*FailurePost // failures to post back to issue
}

func (i *Issue) String() string { return fmt.Sprintf("#%d", i.Number) }

var (
	gh         *github.Client
	repo       *github.Repo
	labels     map[string]*github.Label
	testFlakes *github.Project
	brokenBots *github.Project
)

// readIssues reads the GitHub issues in the Test Flakes project.
// It also sets up the repo, labels, and testFlakes variables for
// use by other functions below.
func readIssues(old []*Issue) ([]*Issue, error) {
	// Find repo.
	r, err := gh.Repo("golang", "go")
	if err != nil {
		return nil, err
	}
	repo = r

	// Find labels.
	list, err := gh.SearchLabels("golang", "go", "")
	if err != nil {
		return nil, err
	}
	labels = make(map[string]*github.Label)
	for _, label := range list {
		labels[label.Name] = label
	}

	// Find Test Flakes project.
	ps, err := gh.Projects("golang", "")
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Title == "Test Flakes" {
			testFlakes = p
			break
		}
	}
	if testFlakes == nil {
		return nil, fmt.Errorf("cannot find Test Flakes project")
	}

	cache := make(map[int]*Issue)
	for _, issue := range old {
		cache[issue.Number] = issue
	}
	// Read all issues in Test Flakes.
	var issues []*Issue
	items, err := gh.ProjectItems(testFlakes)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.Issue != nil {
			issue := &Issue{Issue: item.Issue, NewBody: true, Stale: true}
			if c := cache[item.Issue.Number]; c != nil {
				// Carry conservative NewBody, Mentions data forward
				// to avoid round trips about things we already know.
				if c.Issue.LastEditedAt.Equal(item.Issue.LastEditedAt) {
					issue.NewBody = c.NewBody
				}
				issue.Mentions = c.Mentions
			}
			issues = append(issues, issue)
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].Number < issues[j].Number
	})

	return issues, nil
}

// readBuilderIssues reads the GitHub issues in the Broken Bots project.
// It also sets up the repo, labels, and testFlakes variables for
// use by other functions below.
func readBuilderIssues() ([]*Issue, error) {
	// Find repo.
	r, err := gh.Repo("golang", "go")
	if err != nil {
		return nil, err
	}
	repo = r

	var builderLabel *github.Label

	// Find labels.
	list, err := gh.SearchLabels("golang", "go", "")
	if err != nil {
		return nil, err
	}
	for _, label := range list {
		if label.Name == "Builders" {
			builderLabel = label
			break
		}
	}
	if builderLabel == nil {
		return nil, fmt.Errorf("cannot find builder label")
	}

	labels = make(map[string]*github.Label)
	for _, label := range list {
		labels[label.Name] = label
	}

	// Find Test Flakes project.
	ps, err := gh.Projects("golang", "")
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Title == "Broken Bots" {
			brokenBots = p
			break
		}
	}
	if brokenBots == nil {
		return nil, fmt.Errorf("cannot find Broken Bots project")
	}

	// Read all issues in Test Flakes.
	var issues []*Issue
	items, err := gh.ProjectItems(brokenBots)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.Issue != nil {
			issues = append(issues, &Issue{Issue: item.Issue, NewBody: true, Stale: true})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].Number < issues[j].Number
	})
	return issues, nil
}

// findScripts finds the scripts in the issues,
// initializing issue.Script and .ScriptText or else .Error
// in each issue.
func findScripts(issues []*Issue) {
	for _, issue := range issues {
		findScript(issue)
	}
}

var noScriptError = `
Sorry, but I can't find a watchflakes script at the start of the issue description.
See https://go.dev/wiki/Watchflakes for details.
`

var parseScriptError = `
Sorry, but there were parse errors in the watch flakes script.
The script I found was:

%s

And the problems were:

%s

See https://go.dev/wiki/Watchflakes for details.
`

// findScript finds the script in issue and parses it.
// If the script is not found or has any parse errors,
// issue.Error is filled in.
// Otherwise issue.ScriptText and issue.Script are filled in.
func findScript(issue *Issue) {
	// Extract ```-fenced or indented code block at start of issue description (body).
	body := strings.ReplaceAll(issue.Body, "\r\n", "\n")
	lines := strings.SplitAfter(body, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	text := ""
	if len(lines) > 0 && strings.HasPrefix(lines[0], "```") {
		marker := lines[0]
		n := 0
		for n < len(marker) && marker[n] == '`' {
			n++
		}
		marker = marker[:n]
		i := 1
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], marker) && strings.TrimSpace(strings.TrimLeft(lines[i], "`")) == "" {
				text = strings.Join(lines[1:i], "")
				break
			}
		}
		if i < len(lines) {
		}
	} else if strings.HasPrefix(lines[0], "\t") || strings.HasPrefix(lines[0], "    ") {
		i := 1
		for i < len(lines) && (strings.HasPrefix(lines[i], "\t") || strings.HasPrefix(lines[i], "    ")) {
			i++
		}
		text = strings.Join(lines[:i], "")
	}

	// Must start with #!watchflakes so we're sure it is for us.
	hdr, _, _ := strings.Cut(text, "\n")
	hdr = strings.TrimSpace(hdr)
	if hdr != "#!watchflakes" {
		issue.Error = noScriptError
		return
	}

	// Parse script.
	issue.ScriptText = text
	s, errs := script.Parse("script", text, fields)
	if len(errs) > 0 {
		var errtext strings.Builder
		for _, err := range errs {
			errtext.WriteString(err.Error())
			errtext.WriteString("\n")
		}
		issue.Error = fmt.Sprintf(parseScriptError, indent("\t", text), indent("\t", errtext.String()))
		return
	}

	issue.Script = s
}

func postIssueErrors(issues []*Issue) []error {
	var errors []error
	for _, issue := range issues {
		if issue.Error != "" && issue.NewBody {
			readComments(issue)
			if issue.NewBody {
				fmt.Printf(" - #%d script error\n", issue.Number)
				if *verbose {
					fmt.Printf("\n%s\n", indent(spaces[:7], issue.Error))
				}
				if *post {
					if err := postComment(issue, issue.Error); err != nil {
						errors = append(errors, err)
						continue
					}
					issue.NewBody = false
				}
			}
		}
	}
	return errors
}

// updateText returns the text for the GitHub update on issue.
func updateText(issue *Issue) string {
	if len(issue.Post) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found new dashboard test flakes for:\n\n%s", indent(spaces[:4], issue.ScriptText))
	for _, f := range issue.Post {
		b.WriteString("\n")
		b.WriteString(f.Markdown())
	}
	return b.String()
}

// prepareNew creates and returns a new issue for reporting the failure.
// It doesn't post the issue to GitHub. If *post is true, one needs to
// call postNew to post.
func prepareNew(fp *FailurePost) (*Issue, error) {
	var pattern, title string
	if fp.Pkg != "" {
		pattern = fmt.Sprintf("pkg == %q && test == %q", fp.Pkg, fp.Test)
		test := fp.Test
		if test == "" {
			test = "unrecognized"
		}
		title = shortPkg(fp.Pkg) + ": " + test + " failures"
	} else if fp.Test != "" {
		pattern = fmt.Sprintf("repo == %q && pkg == %q && test == %q", fp.Repo, "", fp.Test)
		title = "build: " + fp.Test + " failures"
	} else if fp.IsBuildFailure() {
		pattern = fmt.Sprintf("builder == %q && repo == %q && mode == %q", fp.Builder, fp.Repo, "build")
		title = "build: build failure on " + fp.Builder
	} else {
		pattern = fmt.Sprintf("builder == %q && repo == %q && pkg == %q && test == %q", fp.Builder, fp.Repo, "", "")
		title = "build: unrecognized failures on " + fp.Builder
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "```\n#!watchflakes\ndefault <- %s\n```\n\n", pattern)
	fmt.Fprintf(&msg, "Issue created automatically to collect these failures.\n\n")
	fmt.Fprintf(&msg, "Example ([log](%s)):\n\n%s", fp.URL, indent(spaces[:4], fp.Snippet))

	// TODO: for a single test failure, add a link to LUCI history page.

	fmt.Printf("# new issue: %s\n%s\n%s\n%s\n\n%s\n", title, fp.String(), fp.URL, pattern, fp.Snippet)
	if *verbose {
		fmt.Printf("\n%s\n", indent(spaces[:3], msg.String()))
	}

	issue := new(Issue)
	issue.Issue = &github.Issue{Title: title, Body: msg.String()}
	findScript(issue)
	if issue.Error != "" {
		return nil, fmt.Errorf("cannot find script in generated issue:\nBody:\n%s\n\nError:\n%s", issue.Body, issue.Error)
	}
	issue.Post = append(issue.Post, fp)
	return issue, nil
}

// signature is the signature we add to the end of every comment or issue body
// we post on GitHub. It links to documentation for users, and it also serves as
// a way to identify the comments that we posted, since watchflakes can be run
// as gopherbot or as an ordinary user.
const signature = "\n\n— [watchflakes](https://go.dev/wiki/Watchflakes)\n"

// keep in sync with buildURL function in luci.go
// An older version reported ci.chromium.org/ui/b instead of ci.chromium.org/b,
// match them as well.
var buildUrlRE = regexp.MustCompile(`[("']https://ci.chromium.org/(ui/)?b/[0-9]+['")]`)

// readComments loads the comments for the given issue,
// setting the Comments, NewBody, and Mentions fields.
func readComments(issue *Issue) {
	if issue.Number == 0 || !issue.Stale {
		return
	}
	log.Printf("readComments %d", issue.Number)
	comments, err := gh.IssueComments(issue.Issue)
	if err != nil {
		log.Fatal(err)
	}
	issue.Comments = comments
	mtime := issue.LastEditedAt
	if mtime.IsZero() {
		mtime = issue.CreatedAt
	}
	issue.Mentions = make(map[string]bool)
	issue.NewBody = true // until proven otherwise
	for _, com := range comments {
		// Only consider comments we signed.
		if !strings.Contains(com.Body, "\n— watchflakes") && !strings.Contains(com.Body, "\n— [watchflakes](") {
			continue
		}
		if com.CreatedAt.After(issue.LastEditedAt) {
			issue.NewBody = false
		}
		for _, link := range buildUrlRE.FindAllString(com.Body, -1) {
			l := strings.Trim(link, "()\"'")
			issue.Mentions[l] = true
			// An older version reported ci.chromium.org/ui/b instead of ci.chromium.org/b,
			// match them as well.
			issue.Mentions[strings.Replace(l, "ci.chromium.org/ui/b/", "ci.chromium.org/b/", 1)] = true
		}
	}
	issue.Stale = false
}

// postNew creates a new issue with the given title and body,
// setting the NeedsInvestigation label and placing the issue in
// the Test Flakes project.
// It automatically caps issue body length and adds signature to it.
func postNew(title, body string) *github.Issue {
	var args []any
	if lab := labels["NeedsInvestigation"]; lab != nil {
		args = append(args, lab)
	}
	args = append(args, testFlakes)

	if len(body) > 50000 {
		// As of 2025-11-27, GitHub GraphQL API limits body length to 65536.
		body = body[:50000] + "\n</details>\n(... long body truncated ...)\n"
	}
	issue, err := gh.CreateIssue(repo, title, body+signature, args...)
	if err != nil {
		log.Fatal(err)
	}
	return issue
}

// postNewBrokenBot creates a new issue with the given title and body,
// setting the NeedsInvestigation label and placing the issue in
// the Broken Bots project.
// It automatically adds signature to the body.
func postNewBrokenBot(title, body string) (*github.Issue, error) {
	var args []any
	if lab := labels["NeedsInvestigation"]; lab != nil {
		args = append(args, lab)
	}
	if lab := labels["Builders"]; lab != nil {
		args = append(args, lab)
	}

	args = append(args, brokenBots)
	issue, err := gh.CreateIssue(repo, title, body+signature, args...)
	return issue, err
}

// postComment posts a new comment on the issue.
// It automatically caps comment body length and adds signature to it.
func postComment(issue *Issue, body string) error {
	if issue.Issue.Closed {
		reopen := false
		for _, p := range issue.Post {
			if p.Time.After(issue.ClosedAt) {
				reopen = true
				break
			}
		}
		if reopen {
			if err := gh.ReopenIssue(issue.Issue); err != nil {
				return err
			}
		}
	}
	if len(body) > 50000 {
		// As of 2025-11-27, GitHub GraphQL API limits comment length to 65536.
		body = body[:50000] + "\n</details>\n(... long comment truncated ...)\n"
	}
	return gh.AddIssueComment(issue.Issue, body+signature)
}
