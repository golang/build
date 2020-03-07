// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"net/url"
	"os/exec"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/github"
	"golang.org/x/build/maintner"
	"golang.org/x/build/repos"
)

func newPullRequest(title, body string) *github.PullRequest {
	return &github.PullRequest{
		Title:  github.String(title),
		Body:   github.String(body),
		Number: github.Int(42),
		Head:   &github.PullRequestBranch{SHA: github.String("deadbeef")},
		Base: &github.PullRequestBranch{
			Repo: &github.Repository{
				Owner: &github.User{
					Login: github.String("golang"),
				},
				Name: github.String("go"),
			},
		},
	}
}

func newMaintnerCL() *maintner.GerritCL {
	return &maintner.GerritCL{
		Commit: &maintner.GitCommit{
			Msg: `cmd/gerritbot: previous commit messsage

Hello there

Change-Id: If751ce3ffa3a4d5e00a3138211383d12cb6b23fc
`,
		},
	}
}

func TestCommitMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("skipping; 'git' not in PATH")
	}
	testCases := []struct {
		desc     string
		pr       *github.PullRequest
		cl       *maintner.GerritCL
		expected string
	}{
		{
			"simple",
			newPullRequest("cmd/gerritbot: title of change", "Body text"),
			nil,
			`cmd/gerritbot: title of change

Body text

Change-Id: I8ef4fc7aa2b40846583a9cbf175d75d023b5564e
GitHub-Last-Rev: deadbeef
GitHub-Pull-Request: golang/go#42
`,
		},
		{
			"change with Change-Id",
			newPullRequest("x/build/cmd/gerritbot: change with change ID", "Body text"),
			newMaintnerCL(),
			`cmd/gerritbot: change with change ID

Body text

Change-Id: If751ce3ffa3a4d5e00a3138211383d12cb6b23fc
GitHub-Last-Rev: deadbeef
GitHub-Pull-Request: golang/go#42
`,
		},
		{
			"Change-Id in body text",
			newPullRequest("cmd/gerritbot: change with change ID in body text",
				"Change-Id: I30e0a6ec666a06eae3e8444490d96fabcab3333e"),
			nil,
			`cmd/gerritbot: change with change ID in body text

Change-Id: I30e0a6ec666a06eae3e8444490d96fabcab3333e
GitHub-Last-Rev: deadbeef
GitHub-Pull-Request: golang/go#42
`,
		},
		{
			"Change-Id in body text with an existing CL",
			newPullRequest("cmd/gerritbot: change with change ID in body text and an existing CL",
				"Change-Id: I30e0a6ec666a06eae3e8444490d96fabcab3333e"),
			newMaintnerCL(),
			`cmd/gerritbot: change with change ID in body text and an existing CL

Change-Id: I30e0a6ec666a06eae3e8444490d96fabcab3333e

Change-Id: If751ce3ffa3a4d5e00a3138211383d12cb6b23fc
GitHub-Last-Rev: deadbeef
GitHub-Pull-Request: golang/go#42
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			msg, err := commitMessage(tc.pr, tc.cl)
			if err != nil {
				t.Fatalf("got unexpected error from commitMessage: %v", err)
			}
			if diff := cmp.Diff(msg, tc.expected); diff != "" {
				t.Errorf("got unexpected commit message (-got +want)\n%s", diff)
			}
		})
	}
}

// Test that gerritChangeRE matches the URL to the Change within
// the git output from Gerrit after successfully creating a new CL.
// Whenever Gerrit changes the Change URL format in its output,
// we need to update gerritChangeRE and this test accordingly.
//
// See https://golang.org/issue/27561.
func TestFindChangeURL(t *testing.T) {
	for _, tc := range [...]struct {
		name string
		in   string // Output from git (and input to the regexp).
		want string
	}{
		{
			name: "verbatim", // Verbatim git output from Gerrit, extracted from production logs on 2018/09/07.
			in:   "remote: \rremote: Processing changes: new: 1 (\\)\rremote: Processing changes: new: 1 (|)\rremote: Processing changes: refs: 1, new: 1 (|)\rremote: Processing changes: refs: 1, new: 1 (|)        \rremote: Processing changes: refs: 1, new: 1, done            \nremote: \nremote: SUCCESS        \nremote: \nremote: New Changes:        \nremote:   https://go-review.googlesource.com/c/dl/+/134117 remove blank line from codereview.cfg        \nTo https://go.googlesource.com/dl\n * [new branch]      HEAD -> refs/for/master",
			want: "https://go-review.googlesource.com/c/dl/+/134117",
		},
		{
			name: "repo-with-dash", // A Gerrit repository with a dash in its name (shortened git output).
			in:   "remote: \rremote: Processing changes: (\\) [...] https://go-review.googlesource.com/c/vscode-go/+/222417 [...]",
			want: "https://go-review.googlesource.com/c/vscode-go/+/222417",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := gerritChangeRE.FindString(tc.in)
			if got != tc.want {
				t.Errorf("could not find change URL in command output: %q\n\ngot %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFindChangeURLInAllRepos tests that gerritChangeRE
// works for names of all Gerrit repositories.
//
// See https://golang.org/issue/37725.
func TestFindChangeURLInAllRepos(t *testing.T) {
	for proj := range repos.ByGerritProject {
		u := (&url.URL{
			Scheme: "https",
			Host:   "go-review.googlesource.com",
			Path:   "/c/" + proj + "/+/1337",
		}).String()
		if gerritChangeRE.FindString("... "+u+" ...") != u {
			t.Errorf("gerritChangeRE regexp didn't work for Gerrit repository named %q, does the regexp need to be adjusted to match some additional characters?", proj)
		}
	}
}
