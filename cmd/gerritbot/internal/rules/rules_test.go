// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rules

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFormatRuleResults(t *testing.T) {
	tests := []struct {
		title        string
		repo         string // if empty string, treated as "go" repo
		body         string
		wantFindings string
		wantNotes    string
	}{
		{
			title:        `fmt: improve some things`, // We consider this a good commit message title.
			body:         goodCommitBody,
			wantFindings: ``,
			wantNotes:    ``,
		},
		{
			title: `a bad commit message title.`,
			body:  goodCommitBody,
			wantFindings: `  1. The commit title should start with the primary affected package name followed by a colon, like "net/http: improve [...]".
  2. The first word in the commit title after the package should be a lowercase English word (usually a verb).
  3. The commit title should not end with a period.
`,
			wantNotes: commitMessageAdvice,
		},
		{
			title: `A bad vscode-go commit title`, // This verifies we complain about a "component" rather than "package".
			repo:  "vscode-go",
			body:  "This includes a bad bug format for vscode-go repo.\nFixes #1234",
			wantFindings: `  1. The commit title should start with the primary affected component name followed by a colon, like "src/goInstallTools: improve [...]".
  2. The first word in the commit title after the component should be a lowercase English word (usually a verb).
  3. Do you have the right bug reference format? For the vscode-go repo, the format is usually 'Fixes golang/vscode-go#1234' or 'Updates golang/vscode-go#1234' at the end of the commit message.
`,
			wantNotes: commitMessageAdvice,
		},
		{
			title:        `A bad wiki commit title we allow`, // We ignore the wiki repo.
			repo:         "wiki",
			body:         "A bad body we allow",
			wantFindings: "",
			wantNotes:    "",
		},
		{
			title:        goodCommitTitle,
			body:         "This commit body is missing a bug reference.",
			wantFindings: "  1. You usually need to reference a bug number for all but trivial or cosmetic fixes. For this repo, the format is usually 'Fixes #12345' or 'Updates #12345' at the end of the commit message. Should you have a bug reference?\n",
			wantNotes:    commitMessageAdvice,
		},
		{
			title: goodCommitTitle,
			body:  "Some `backticks`\n" + strings.Repeat("long line", 20) + "\n" + goodCommitBody,
			wantFindings: `  1. You have a long 180 character line in the commit message body. Please add line breaks to long lines that should be wrapped. Lines in the commit message body should be wrapped at ~76 characters unless needed for things like URLs or tables. (Note: GitHub might render long lines as soft-wrapped, so double-check in the Gerrit commit message shown above.)
  2. It looks like you are using markdown in the commit message. If so, please remove it. Be sure to double-check the plain text shown in the Gerrit commit message above for any markdown backticks, markdown links, or other markdown formatting.
`,
			wantNotes: commitMessageAdvice,
		},
	}
	for _, tt := range tests {
		t.Run("title "+tt.title, func(t *testing.T) {
			commit := commitMessage(tt.title, tt.body, goodCommitFooters)
			repo := "go"
			if tt.repo != "" {
				repo = tt.repo
			}
			change, err := ParseCommitMessage(repo, commit)
			if err != nil {
				t.Fatalf("ParseCommitMessage failed: %v", err)
			}
			results := Check(change)
			gotFindings, gotNotes := FormatResults(results)
			t.Log("FormatResults:\n" + gotFindings)

			if diff := cmp.Diff(tt.wantFindings, gotFindings); diff != "" {
				t.Errorf("checkRules() findings mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantNotes, gotNotes); diff != "" {
				t.Errorf("checkRules() notes mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTitleRules(t *testing.T) {
	tests := []struct {
		title string
		repo  string // if empty string, treated as "go" repo
		body  string
		want  []string
	}{
		// We consider these good titles.
		{
			title: "fmt: good",
			want:  nil,
		},
		{
			title: "go/types, types2: good",
			want:  nil,
		},
		{
			title: "go/types, types2, types3: good",
			want:  nil,
		},
		{
			title: "fmt: improve & make: Good",
			want:  nil,
		},
		{
			title: "README: fix something",
			want:  nil,
		},
		{
			title: "_content/doc/go1.21: fix something",
			repo:  "website",
			want:  nil,
		},
		{
			title: "Fix a proposal", // We are lenient with proposal repo titles.
			repo:  "proposal",
			want:  nil,
		},

		// We consider these bad titles.
		{
			title: "bad.",
			want: []string{
				"title: no package found",
				"title: no lowercase word after a first colon",
				"title: ends with period",
			},
		},
		{
			title: "bad",
			want: []string{
				"title: no package found",
				"title: no lowercase word after a first colon",
			},
		},
		{
			title: "fmt: bad.",
			want: []string{
				"title: ends with period",
			},
		},
		{
			title: "Fmt: bad",
			want: []string{
				"title: no package found",
			},
		},
		{
			title: "fmt: Bad",
			want: []string{
				"title: no lowercase word after a first colon",
			},
		},
		{
			title: "fmt:  bad",
			want: []string{
				"title: no colon then single space after package",
			},
		},
		{
			title: "fmt:  Bad",
			want: []string{
				"title: no colon then single space after package",
				"title: no lowercase word after a first colon",
			},
		},
		{
			title: "fmt:bad",
			want: []string{
				"title: no colon then single space after package",
			},
		},
		{
			title: "fmt : bad",
			want: []string{
				"title: no package found",
			},
		},
		{
			title: ": bad",
			want: []string{
				"title: no package found",
			},
		},
		{
			title: " : bad",
			want: []string{
				"title: no package found",
			},
		},
		{
			title: "go/types types2: bad",
			want: []string{
				"title: no package found",
			},
		},
		{
			title: "a sentence, with a comma and colon: bad",
			want: []string{
				"title: no package found",
			},
		},
		{
			title: "a sentence with a colon: and a wrongly placed package fmt: bad",
			want: []string{
				"title: no package found",
			},
		},
		{
			title: "",
			want: []string{
				"title: no package found",
				"title: no lowercase word after a first colon",
			},
		},

		// We allow these titles (in interests of simplicity or leniency).
		// TODO: are some of these considered an alternative good style?
		{
			title: "go/types,types2: we allow",
			want:  nil,
		},
		{
			title: "cmd/{compile,link}: we allow",
			want:  nil,
		},
		{
			title: "cmd/{compile, link}: we allow",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run("title "+tt.title, func(t *testing.T) {
			commit := commitMessage(tt.title, goodCommitBody, goodCommitFooters)
			repo := "go"
			if tt.repo != "" {
				repo = tt.repo
			}
			change, err := ParseCommitMessage(repo, commit)
			if err != nil {
				t.Fatalf("ParseCommitMessage failed: %v", err)
			}
			results := Check(change)

			var got []string
			for _, r := range results {
				got = append(got, r.Name)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("checkRules() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBodyRules(t *testing.T) {
	tests := []struct {
		name  string
		title string // if empty string, we use goodCommitTitle
		repo  string // if empty string, treated as "go" repo
		body  string
		want  []string
	}{
		// We consider these good bodies.
		{
			name: "good",
			body: goodCommitBody,
			want: nil,
		},
		{
			name: "good bug format for go repo",
			repo: "go",
			body: "This is This is body text.\n\nFixes #1234",
			want: nil,
		},
		{
			name: "good bug format for tools repo",
			repo: "tools",
			body: "This is This is body text.\n\nFixes golang/go#1234",
			want: nil,
		},
		{
			name: "good bug format for vscode-go",
			repo: "vscode-go",
			body: "This is This is body text.\n\nFixes golang/vscode-go#1234",
			want: nil,
		},
		{
			name: "good bug format for unknown repo",
			repo: "some-future-repo",
			body: "This is This is body text.\n\nFixes golang/go#1234",
			want: nil,
		},
		{
			name: "allowed long lines for benchstat output",
			body: "Encode/format=json-48   1.718µ ± 1%   1.423µ ± 1%  -17.20% (p=0.000 n=10)" +
				strings.Repeat("Hello. ", 100) + "\n" + goodCommitBody,
			want: nil,
		},
		{
			name:  "a trival fix",
			title: "fmt: fix spelling mistakes",
			body:  "",
			want:  nil, // we don't flag short body or missing bug reference because "spelling" is in title
		},

		// Now we consider some bad bodies.
		// First, some basic mistakes.
		{
			name: "too short",
			body: "Short body",
			want: []string{
				"body: short",
				"body: no bug reference candidate found",
			},
		},
		{
			name: "missing body",
			body: "",
			want: []string{
				"body: short",
				"body: no bug reference candidate found",
			},
		},
		{
			name: "not word wrapped",
			body: strings.Repeat("Hello. ", 100) + "\n" + goodCommitBody,
			want: []string{
				"body: long lines",
			},
		},
		{
			name: "not a sentence",
			body: "This is missing a period",
			want: []string{
				"body: no sentence candidates found",
				"body: no bug reference candidate found",
			},
		},
		{
			name: "Signed-off-by",
			body: "Signed-off-by: bad\n\n" + goodCommitBody,
			want: []string{
				"body: contains Signed-off-by",
			},
		},
		{
			name: "PR instructions",
			body: "Delete these instructions once you have read and applied them.\n",
			want: []string{
				"body: still contains PR instructions",
				"body: no bug reference candidate found",
			},
		},

		// Next, mistakes in the repo-specific format or location of bug references.
		{
			name: "bad bug format for go repo",
			repo: "go",
			body: "This is body text.\n\nFixes golang/go#1234",
			want: []string{
				"body: bug format looks incorrect",
			},
		},
		{
			name: "bad bug format for tools repo",
			repo: "tools",
			body: "This is body text.\n\nFixes #1234",
			want: []string{
				"body: bug format looks incorrect",
			},
		},
		{
			name: "bad bug format for vscode-go",
			repo: "vscode-go",
			body: "This is body text.\n\nFixes #1234",
			want: []string{
				"body: bug format looks incorrect",
			},
		},
		{
			name: "bad bug format for unknown repo",
			repo: "some-future-repo",
			body: "This is body text.\n\nFixes #1234",
			want: []string{
				"body: bug format looks incorrect",
			},
		},
		{
			name: "bad bug location",
			body: "This is body text.\nFixes #1234\nAnd a final line we should not have.",
			want: []string{
				"body: no bug reference candidate at end",
			},
		},

		// We next have some good bodies that are markdown-ish,
		// but we allow them.
		{
			name:  "allowed markdown: mention regex in title",
			title: "regexp: fix something",
			body:  "Example `.*`.\n" + goodCommitBody,
			want:  nil,
		},
		{
			name: "allowed markdown: mention regex in body",
			body: "A regex `.*`.\n" + goodCommitBody,
			want: nil,
		},
		{
			name: "allowed markdown: mention markdown",
			body: "A markdown bug `and this is ok`.\n" + goodCommitBody,
			want: nil,
		},
		{
			name: "allowed markdown: might be go code",
			body: " s := `raw`\n" + goodCommitBody,
			want: nil,
		},

		// Examples of using markdown that we flag.
		{
			name: "markdown backticks",
			body: "A variable `foo`.\n" + goodCommitBody,
			want: []string{
				"body: might use markdown",
			},
		},
		{
			name: "markdown block quote",
			body: "Some code:\n```\nx := y\n```\n" + goodCommitBody,
			want: []string{
				"body: might use markdown",
			},
		},
		{
			name: "markdown link",
			body: "[click here](https://example.com)\n" + goodCommitBody,
			want: []string{
				"body: might use markdown",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title := goodCommitTitle
			if tt.title != "" {
				title = tt.title
			}
			commit := commitMessage(title, tt.body, goodCommitFooters)
			repo := "go"
			if tt.repo != "" {
				repo = tt.repo
			}
			change, err := ParseCommitMessage(repo, commit)
			if err != nil {
				t.Fatalf("ParseCommitMessage failed: %v", err)
			}
			results := Check(change)

			var got []string
			for _, r := range results {
				got = append(got, r.Name)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("checkRules() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseCommitMessage(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		text    string
		want    Change
		wantErr bool
	}{
		// Bad examples.
		{
			name:    "not enough lines",
			text:    "title",
			want:    Change{},
			wantErr: true,
		},
		{
			name:    "second line not blank",
			text:    "title\nbad line\nBody 1",
			want:    Change{},
			wantErr: true,
		},
		{
			name:    "no footer",
			text:    "title\n\nBody 1\n",
			want:    Change{},
			wantErr: true,
		},
		{
			name:    "no footer and no body",
			text:    "title\n\n\n",
			want:    Change{},
			wantErr: true,
		},

		// Good examples.
		{
			name: "good",
			text: "title\n\nBody 1\n\nFooter: 1\n",
			want: Change{
				Title: "title",
				Body:  "Body 1",
			},
			wantErr: false,
		},
		{
			name: "good with two body lines",
			text: "title\n\nBody 1\nBody 2\n\nFooter: 1\n",
			want: Change{
				Title: "title",
				Body:  "Body 1\nBody 2",
			},
			wantErr: false,
		},
		{
			name: "good with empty body",
			text: "title\n\nFooter: 1\n",
			want: Change{
				Title: "title",
				Body:  "",
			},
			wantErr: false,
		},
		{
			name: "good with extra blank lines after footer",
			text: "title\n\nBody 1\n\nFooter: 1\n\n\n",
			want: Change{
				Title: "title",
				Body:  "Body 1",
			},
			wantErr: false,
		},
		{
			name: "good with body line that looks like footer",
			text: "title\n\nBody 1\nLink: example.com\n\nFooter: 1\n\n\n",
			want: Change{
				Title: "title",
				Body:  "Body 1\nLink: example.com",
			},
			wantErr: false,
		},
		{
			name: "allowed cherry pick in footer", // Example from CL 346093.
			text: "title\n\nBody 1\n\nFooter: 1\n(cherry picked from commit ebd07b13caf35114b32e7d6783b27902af4829ce)\n",
			want: Change{
				Title: "title",
				Body:  "Body 1",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCommitMessage(tt.repo, tt.text)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCommitMessage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("checkRules() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// commitMessage helps us create valid commit messages while testing.
func commitMessage(title, body, footers string) string {
	return title + "\n\n" + body + "\n\n" + footers
}

// Some auxiliary testing variables available for use when creating commit messages.
var (
	goodCommitTitle   = "pkg: a title that does not trigger any rules"
	goodCommitBody    = "A commit message body that does not trigger any rules.\n\nFixes #1234"
	goodCommitFooters = `Change-Id: I1d8d10b142358983194ef2c389de4d9862d4ce97
GitHub-Last-Rev: 6d27e1471ee5dac0323a10b46e6e64e647068ecf
GitHub-Pull-Request: golang/build#69`
)
