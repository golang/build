// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package rules specifies a simple set of rules for checking GitHub PRs or Gerrit CLs
// for certain common mistakes, like no package in the first line of the commit message
// or having long lines in the commit message body.
//
// The rules attempt to be graceful when encountering a new or unknown repo.
// A new repo usually does not require updating anything here, especially
// if the repo primarily contains Go code and follows typical patterns like
// using the main Go issue tracker. If a new repo is unusual in some way that
// causes a notable problem, some simple options include:
//   - adding the repo to skipAll to ignore the repo entirely
//   - adding the repo to the skip field on individiual rules
//   - updating the usesTracker and packageExample functions if needed
//
// A rule is primarily defined via a function that takes a Change (CL or PR) as input
// and reports zero or 1 findings, which is just a string (usually 1-2 short sentences).
// A rule can also optionally return a note, which might be auxiliary advice such as
// how to edit the commit message.
//
// The FormatResults function renders the results into markdown.
// Each finding is shown to the user, currently in a numbered list of findings.
// The auxiliary notes are deduplicated, so that for example a set of rules
// looking for problems in a commit message can all return the same editing advice
// but that advice is then only shown to the user only once.
//
// For example, for a commit message with a first line of:
//
//	fmt: Improve foo and bar.
//
// That would currently trigger two rules, with an example formatted result of
// the following (including the deduplicated advice at the end):
//
//	Possible problems detected:
//	  1. The first word in the commit title after the package should be a
//	     lowercase English word (usually a verb).
//	  2. The commit title should not end with a period.
//
//	 To edit the commit message, see instructions [here](...). For guidance on commit
//	 messages for the Go project, see [here](...).
//
// Rules currently err on the side of simplicity and avoiding false positives.
// It is intended to be straightforward to add a new rule.
//
// Rules can be arranged to trigger completely independently of one another,
// or alternatively a set of rules can optionally be arranged in groups that
// form a precedence based on order within the group, where at most one rule
// from that group will trigger. This can be helpful in cases like:
//   - You'd like to have a "catch all" rule that should only trigger if another rule
//     in the same group hasn't triggered.
//   - You have a rule that spots a more general problem and rules that spot
//     more specific or pickier variant of the same problem and want to have
//     the general problem reported if applicable without also reporting
//     pickier findings that would seem redundant.
//   - A subset of rules that are otherwise intended to be mutually exclusive.
package rules

import (
	"fmt"
	"strings"
)

// rule defines a single rule that can report a finding.
// See the package comment for an overview.
type rule struct {
	// name is a short internal rule name that don't expect to tweak frequently.
	// We don't show it to users, but do use in tests.
	name string

	// f is the rule function that reports a single finding and optionally
	// an auxiliary note, which for example could be advice on how to edit the commit message.
	// Notes are deduplicated across different rules, and ignored for a given rule if there is no finding.
	f func(change Change) (finding string, note string)

	// skip lists repos to skip for this rule.
	skip []string

	// only lists the only repos that this rule will run against.
	only []string
}

// skipAll lists repos that we ignore entirely by skipping all rules.
var skipAll = []string{"wiki"}

// ruleGroups defines our live set of rules. It is a [][]rule
// because the individual rules are arranged into groups of rules
// that are mutually exclusive, where the first triggering rule wins
// within a []rule in ruleGroups. See the package comment for details.
var ruleGroups = [][]rule{
	{
		// We have two rules in this rule group, and hence we report at most one of them.
		// The second (pickier) rule is checked only if the first doesn't trigger.
		{
			name: "title: no package found",
			skip: []string{"proposal"}, // We allow the proposal repo to have an irregular title.
			f: func(change Change) (finding string, note string) {
				component, example := packageExample(change.Repo)
				finding = fmt.Sprintf("The commit title should start with the primary affected %s name followed by a colon, like \"%s\".", component, example)
				start, _, ok := strings.Cut(change.Title, ":")
				if !ok {
					// No colon.
					return finding, commitMessageAdvice
				}
				var pattern string
				switch change.Repo {
				default:
					// A single package starts with a lowercase ASCII and has no spaces prior to the colon.
					pattern = `^[a-z][^ ]+$`
				case "website":
					// Allow leading underscore (e.g., "_content").
					pattern = `^[a-z_][^ ]+$`
				case "vscode-go":
					// Allow leading uppercase and period (e.g., ".github/workflow").
					pattern = `^[a-zA-Z.][^ ]+$`
				}
				if match(pattern, start) {
					return "", ""
				}
				if match(`^(README|DEBUG)`, start) {
					// Rare, but also allow a root README or DEBUG file as a component.
					return "", ""
				}
				pkgs := strings.Split(start, ", ")
				if match(`^[a-z]`, start) && len(pkgs) > 1 && !matchAny(` `, pkgs) {
					// We also allow things like "go/types, types2: ..." (but not "hello, fun world: ...").
					return "", ""
				}
				return finding, commitMessageAdvice
			},
		},
		{
			name: "title: no colon then single space after package",
			skip: []string{"proposal"}, // We allow the proposal repo to have an irregular title.
			f: func(change Change) (finding string, note string) {
				component, example := packageExample(change.Repo)
				finding = fmt.Sprintf("The %s in the commit title should be followed by a colon and single space, like \"%s\".", component, example)
				if !match(`[^ ]: [^ ]`, change.Title) {
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		{
			name: "title: no lowercase word after a first colon",
			skip: []string{"proposal"}, // We allow the proposal repo to have an irregular title.
			f: func(change Change) (finding string, note string) {
				component, _ := packageExample(change.Repo)
				finding = fmt.Sprintf("The first word in the commit title after the %s should be a lowercase English word (usually a verb).", component)
				_, after, ok := strings.Cut(change.Title, ":")
				if !ok {
					// No colon. Someone who doesn't have any colon probably can use a reminder about what comes next.
					return finding, commitMessageAdvice
				}
				if !match(`^[ ]*[a-z]+\b`, after) {
					// Probably not a lowercase English verb.
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		{
			name: "title: ends with period",
			f: func(change Change) (finding string, note string) {
				finding = "The commit title should not end with a period."
				if len(change.Title) > 0 && change.Title[len(change.Title)-1] == '.' {
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		// We have two rules in this group, and hence we report at most one of them.
		// The second rule is pickier.
		{
			name: "body: short",
			f: func(change Change) (finding string, note string) {
				finding = "The commit message body is %s. " +
					"That can be OK if the change is trivial like correcting spelling or fixing a broken link, " +
					"but usually the description should provide context for the change and explain what it does in complete sentences."
				if !mightBeTrivial(change) {
					size := len([]rune(change.Body))
					switch {
					case size == 0:
						return fmt.Sprintf(finding, "missing"), commitMessageAdvice
					case size < 24: // len("Updates golang/go#12345") == 23
						return fmt.Sprintf(finding, "very brief"), commitMessageAdvice
					}
				}
				return "", ""
			},
		},
		{
			name: "body: no sentence candidates found",
			f: func(change Change) (finding string, note string) {
				finding = "Are you describing the change in complete sentences with correct punctuation in the commit message body, including ending sentences with periods?"
				if !mightBeTrivial(change) && !match("[a-zA-Z0-9\"'`)][.:?!)]( |\\n|$)", change.Body) {
					// A complete English sentence usually ends with an alphanumeric immediately followed by a terminating punctuation.
					// (This is an approximate test, but currently has a very low false positive rate. Brief manual checking suggests
					// ~zero false positives in a corpus of 1000 CLs).
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		{
			name: "body: long lines",
			f: func(change Change) (finding string, note string) {
				finding = "Lines in the commit message should be wrapped at ~76 characters unless needed for things like URLs or tables. You have a %d character line."
				// Check if something smells like a table, ASCII art, or benchstat output.
				if match("----|____|[\u2500-\u257F]", change.Body) || match(` ± [0-9.]+%`, change.Body) {
					// This might be something that is allowed to be wide.
					// (The Unicode character range above covers box drawing characters, which is probably overkill).
					// We are lenient here and don't check the rest of the body,
					// mainly to be friendly and also we don't want to guess
					// line by line whether something looks like a table.
					return "", ""
				}
				longest := 0
				for _, line := range splitLines(change.Body) {
					if match(`https?://|www\.|\.(com|org|dev)`, line) || matchCount(`[/\\]`, line) > 4 {
						// Might be a long url or other path.
						continue
					}
					l := len([]rune(line))
					if longest < l {
						longest = l
					}
				}
				if longest > 78 { // Official guidance on wiki is "~76".
					return fmt.Sprintf(finding, longest), commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		{
			name: "body: might use markdown",
			f: func(change Change) (finding string, note string) {
				finding = "Are you using markdown? Markdown should not be used to augment text in the commit message."
				if match(`(?i)markdown`, change.Title) || match(`(?i)markdown`, change.Body) {
					// Could be a markdown-related fix.
					return "", ""
				}
				if matchCount("(?m)^```", change.Body) >= 2 {
					// Looks like a markdown code block.
					return finding, commitMessageAdvice
				}
				// Now look for markdown backticks (which might be most common offender),
				// but with a mild attempt to avoid flagging a Go regexp or other Go raw string.
				if !match(`regex|string`, change.Title) {
					for _, line := range splitLines(change.Body) {
						if strings.ContainsAny(line, "={}()[]") || match(`regex|string`, line) {
							// This line might contain Go code. This is fairly lenient, but
							// in practice, if someone uses markdown once in a CL, they tend to use
							// it multiple times, so we often have multiple chances to find a hit.
							continue
						}
						if match("`.+`", line) {
							// Could be a markdown backtick.
							return finding, commitMessageAdvice
						}
					}
				}
				if match(`\[.+]\(https?://.+\)`, change.Body) {
					// Looks like a markdown link. (Sorry, currently our ugliest regexp).
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		{
			name: "body: still contains PR instructions",
			f: func(change Change) (finding string, note string) {
				finding = "Do you still have the GitHub PR instructions in your commit message text? The PR instructions should be deleted once you have applied them."
				if strings.Contains(change.Body, "Delete these instructions once you have read and applied them") {
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		{
			name: "body: contains Signed-off-by",
			f: func(change Change) (finding string, note string) {
				finding = "Please do not use 'Signed-off-by'. We instead rely on contributors signing CLAs."
				if match(`(?mi)^Signed-off-by: `, change.Body) {
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
	{
		// We have three rules in this group, and hence we report at most one of them.
		// The three rules get progressively pickier.
		// We exempt the proposal repo from these rules because almost all GitHub PRs
		// for the proposal repo are for typos or similar.
		{
			name: "body: no bug reference candidate found",
			skip: []string{"proposal"},
			f: func(change Change) (finding string, note string) {
				finding = "You usually need to reference a bug number for all but trivial or cosmetic fixes. " +
					"%s at the end of the commit message. Should you have a bug reference?"
				if mightBeTrivial(change) {
					return "", ""
				}
				var bugPattern string
				switch usesTracker(change.Repo) {
				case mainTracker:
					bugPattern = `#\d{4}`
				default:
					bugPattern = `#\d{2}`
				}
				if !match(bugPattern, change.Body) {
					return fmt.Sprintf(finding, bugExamples(change.Repo)), commitMessageAdvice
				}
				return "", ""
			},
		},
		{
			name: "body: bug format looks incorrect",
			skip: []string{"proposal"},
			f: func(change Change) (finding string, note string) {
				finding = "Do you have the right bug reference format? %s at the end of the commit message."
				if mightBeTrivial(change) {
					return "", ""
				}
				var bugPattern string
				switch {
				case change.Repo == "go":
					bugPattern = `#\d{4,}`
				case usesTracker(change.Repo) == mainTracker:
					bugPattern = `golang/go#\d{4,}`
				case usesTracker(change.Repo) == ownTracker:
					bugPattern = fmt.Sprintf(`golang/%s#\d{2,}`, change.Repo)
				default:
					bugPattern = `golang/go#\d{4,}`
				}
				if !match(`(?m)^(Fixes|Updates|For|Closes|Resolves) `+bugPattern+`\.?$`, change.Body) {
					return fmt.Sprintf(finding, bugExamples(change.Repo)), commitMessageAdvice
				}
				return "", ""
			},
		},
		{
			name: "body: no bug reference candidate at end",
			skip: []string{"proposal"},
			f: func(change Change) (finding string, note string) {
				// If this rule is running, it means it passed the earlier bug-related rules
				// in this group, so we know there is what looks like a well-formed bug.
				finding = "It looks like you have a properly formated bug reference, but the convention is to " +
					"put bug references at the bottom of the commit message, even if a bug is also mentioned " +
					"in the body of the message."
				if mightBeTrivial(change) {
					return "", ""
				}
				var bugPattern string
				switch {
				case usesTracker(change.Repo) == mainTracker:
					bugPattern = `#\d{4}`
				default:
					bugPattern = `#\d{2}`
				}
				lines := splitLines(change.Body)
				if len(lines) > 0 && !match(bugPattern, lines[len(lines)-1]) {
					return finding, commitMessageAdvice
				}
				return "", ""
			},
		},
	},
}

// Auxiliary advice.
var commitMessageAdvice = "The commit title and commit message body come from the GitHub PR title and description, and must be edited in the GitHub web interface (not via git). " +
	"For instructions, see [here](https://go.dev/wiki/GerritBot/#how-does-gerritbot-determine-the-final-commit-message). " +
	"For guidelines on commit messages for the Go project, see [here](https://go.dev/doc/contribute#commit_messages)."

// mightBeTrivial reports if a change looks like something
// where a commit body is often allowed to be skipped, such
// as correcting spelling, fixing a broken link, or
// adding or correcting an example.
func mightBeTrivial(change Change) bool {
	return match(`(?i)typo|spell|grammar|grammatical|comment|readme|document|\bdocs?\b|example|(fix|correct|broken|wrong).*(link|url)`, change.Title)
}

// bugExamples returns a snippet of text that includes example bug references
// formatted as expected for a repo.
func bugExamples(repo string) string {
	switch {
	case repo == "go":
		return "For this repo, the format is usually 'Fixes #12345' or 'Updates #12345'"
	case usesTracker(repo) == mainTracker:
		return fmt.Sprintf("For the %s repo, the format is usually 'Fixes golang/go#12345' or 'Updates golang/go#12345'", repo)
	case usesTracker(repo) == ownTracker:
		return fmt.Sprintf("For the %s repo, the format is usually 'Fixes golang/%s#1234' or 'Updates golang/%s#1234'", repo, repo, repo)
	default:
		// We don't know how issues should be referenced for this repo, including this might be
		// some future created after these rules were last updated, so be a bit vaguer.
		return "For most repos outside the main go repo, the format is usually 'Fixes golang/go#12345' or 'Updates golang/go#12345'"
	}
}

// packageExample returns an example usage of a package/component in a commit title
// for a given repo, along with what to call the leading words before the first
// colon (e.g., "package" vs. "component").
func packageExample(repo string) (component string, example string) {
	switch repo {
	default:
		return "package", "net/http: improve [...]"
	case "website":
		return "component", "_content/doc/go1.21: fix [...]"
	case "vscode-go":
		return "component", "src/goInstallTools: improve [...]"
	case "wiki":
		return "page name or component", "MinimumRequirements: update [...]"
	}
}

// tracker is the issue tracker used by a repo.
type tracker int

const (
	mainTracker    tracker = iota // uses the main golang/go issue tracker.
	ownTracker                    // uses a repo-specific tracker, like golang/vscode-go.
	unknownTracker                // we don't know what tracker is used, which usually means the main tracker is used.
)

// usesTracker reports if a repo uses the main golang/go tracker or a repo-specific tracker.
// Callers should deal gracefully with unknownTracker being reported (such as by giving less precise
// advice that is still generally useful), which might happen with a less commonly used repo or some future repo.
func usesTracker(repo string) tracker {
	// It's OK for a repo to be missing from our list. If something is missing, either it is not frequently used
	// and hence hopefully not a major problem, or in the rare case we are missing something frequently used,
	// this can be updated if someone complains.
	// TODO: not immediately clear where vulndb should go. In practice, it seems to use the main golang/go tracker,
	// but it also has its own tracker (which might just be for security reports?). Leaving unspecified for now,
	// which results in vaguer advice.
	// TODO: oauth2 might transition from its own tracker to the main tracker (https://go.dev/issue/56402), so
	// we also leave unspecified for now.
	switch repo {
	case "go", "arch", "build", "crypto", "debug", "exp", "image", "mobile", "mod", "net", "perf", "pkgsite", "playground",
		"proposal", "review", "sync", "sys", "telemetry", "term", "text", "time", "tools", "tour", "vuln", "website", "wiki", "xerrors":
		return mainTracker
	case "vscode-go", "oscar":
		return ownTracker
	default:
		return unknownTracker
	}
}
