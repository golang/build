// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rules

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/exp/slices"
)

// Change represents a Gerrit CL and/or GitHub PR that we want to check rules against.
type Change struct {
	// Repo is the repository as reported by Gerrit (e.g., "go", "tools", "vscode-go", "website").
	Repo string
	// Title is the commit message first line.
	Title string
	// Body is the commit message body (skipping the title on the first line and the blank second line,
	// and without the footers).
	Body string

	// TODO: could consider a Footer field, if useful, though I think GerritBot & Gerrit manage those.
	// TODO: could be useful in future to have CL and PR fields as well, e.g., if we wanted to
	// spot check for something that looks like test files in the changed file list.
}

func ParseCommitMessage(repo string, text string) (Change, error) {
	change := Change{Repo: repo}
	lines := splitLines(text)
	if len(lines) < 3 {
		return Change{}, fmt.Errorf("rules: ParseCommitMessage: short commit message: %q", text)
	}
	change.Title = lines[0]
	if lines[1] != "" {
		return Change{}, fmt.Errorf("rules: ParseCommitMessage: second line is not blank in commit message: %q", text)
	}

	// Find the body.
	// Trim the footers, starting at bottom and stopping at first blank line seen after any footers.
	// It is an error to not have any footers (which likely means we are seeing something not managed
	// by GerritBot and/or Gerrit).
	body := lines[2:]
	sawFooter := false
	for i := len(body) - 1; i >= 0; i-- {
		if match(`^[a-zA-Z][^ ]*: `, body[i]) {
			body = body[:i]
			sawFooter = true
			continue
		}
		if match(`^\(cherry picked from commit [a-f0-9]+\)$`, body[i]) {
			// One CL in our corpus (CL 346093) has this intermixed with the footers.
			continue
		}
		if body[i] == "" {
			if !sawFooter {
				continue // We leniently skip any blank lines at bottom of commit message.
			}
			body = body[:i]
			break
		}
		return Change{}, fmt.Errorf("rules: ParseCommitMessage: found non-footer line at end of commit message. line: %q, commit message: %q", body[i], text)
	}
	if !sawFooter {
		return Change{}, fmt.Errorf("rules: ParseCommitMessage: did not find any footers preceded by blank line for commit message: %q", text)
	}
	change.Body = strings.Join(body, "\n")

	return change, nil
}

// Result contains the result of a single rule check against a Change.
type Result struct {
	Name    string
	Finding string
	Note    string
}

// Check runs the defined rules against one Change.
func Check(change Change) (results []Result) {
	if slices.Contains(skipAll, change.Repo) {
		return nil
	}
	for _, group := range ruleGroups {
		for _, rule := range group {
			if slices.Contains(rule.skip, change.Repo) || len(rule.only) > 0 && !slices.Contains(rule.only, change.Repo) {
				continue
			}
			finding, advice := rule.f(change)
			if finding != "" {
				results = append(results, Result{
					Name:    rule.name,
					Finding: finding,
					Note:    advice,
				})
				break // Only report the first finding per rule group.
			}
		}
	}
	return results
}

// FormatResults returns the findings and notes ready to be placed in a CL comment,
// formatted as simple markdown.
func FormatResults(results []Result) (findings string, notes string) {
	if len(results) == 0 {
		return "", ""
	}
	var b strings.Builder
	cnt := 1
	for _, r := range results {
		fmt.Fprintf(&b, "  %d. %s\n", cnt, r.Finding)
		cnt++
	}
	advice := formatAdvice(results)
	return b.String(), advice
}

// formatAdvice returns a deduplicated string containing all the advice in results.
func formatAdvice(results []Result) string {
	var s []string
	seen := make(map[string]bool)
	for _, r := range results {
		if !seen[r.Note] {
			s = append(s, r.Note)
		}
		seen[r.Note] = true
	}
	return strings.Join(s, " ")
}

// match reports whether the regexp pattern matches s,
// returning false for a bad regexp after logging the bad regexp.
func match(pattern string, s string) bool {
	re := regexp.MustCompile(pattern)
	return re.MatchString(s)
}

// matchAny reports whether the regexp pattern matches any string in list,
// returning false for a bad regexp after logging the bad regexp.
func matchAny(pattern string, list []string) bool {
	re := regexp.MustCompile(pattern)
	for _, s := range list {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// matchCount reports the count of matches for the regexp in s,
// returning 0 for a bad regexp after logging the bad regexp.
func matchCount(pattern string, s string) int {
	re := regexp.MustCompile(pattern)
	return len(re.FindAllString(s, -1))
}

// splitLines returns s split into lines, without trailing \n.
func splitLines(s string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}
