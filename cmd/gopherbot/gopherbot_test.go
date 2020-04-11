// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-github/github"
	"golang.org/x/build/devapp/owners"
	"golang.org/x/build/maintner"
)

func TestLabelCommandsFromComments(t *testing.T) {
	created := time.Now()
	testCases := []struct {
		desc string
		body string
		cmds []labelCommand
	}{
		{
			"basic add/remove",
			"We should fix this issue, but we need help\n\n@gopherbot please add help wanted, needsfix and remove needsinvestigation",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
				{action: "add", label: "needsfix", created: created},
				{action: "remove", label: "needsinvestigation", created: created},
			},
		},
		{
			"no please",
			"@gopherbot add NeedsFix",
			[]labelCommand{
				{action: "add", label: "needsfix", created: created},
			},
		},
		{
			"with comma",
			"@gopherbot, NeedsFix",
			[]labelCommand{
				{action: "add", label: "needsfix", created: created},
			},
		},
		{
			"with semicolons",
			"@gopherbot NeedsFix;help wanted; remove needsinvestigation",
			[]labelCommand{
				{action: "add", label: "needsfix", created: created},
				{action: "add", label: "help wanted", created: created},
				{action: "remove", label: "needsinvestigation", created: created},
			},
		},
		{
			"case insensitive",
			"@gopherbot please add HelP WanteD",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
			},
		},
		{
			"fun input",
			"@gopherbot please add help wanted,;needsfix;",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
				{action: "add", label: "needsfix", created: created},
			},
		},
		{
			"with hyphen",
			"@gopherbot please add label OS-macOS",
			[]labelCommand{
				{action: "add", label: "os-macos", created: created},
			},
		},
		{
			"unlabel keyword",
			"@gopherbot please unlabel needsinvestigation, NeedsDecision",
			[]labelCommand{
				{action: "remove", label: "needsinvestigation", created: created},
				{action: "remove", label: "needsdecision", created: created},
			},
		},
		{
			"with label[s] keyword",
			"@gopherbot please add label help wanted and remove labels needsinvestigation, NeedsDecision",
			[]labelCommand{
				{action: "add", label: "help wanted", created: created},
				{action: "remove", label: "needsinvestigation", created: created},
				{action: "remove", label: "needsdecision", created: created},
			},
		},
		{
			"no label commands",
			"The cake was a lie",
			nil,
		},
	}
	for _, tc := range testCases {
		cmds := labelCommandsFromBody(tc.body, created)
		if diff := cmp.Diff(cmds, tc.cmds, cmp.AllowUnexported(labelCommand{})); diff != "" {
			t.Errorf("%s: commands differ: (-got +want)\n%s", tc.desc, diff)
		}
	}
}

func TestLabelMutations(t *testing.T) {
	testCases := []struct {
		desc   string
		cmds   []labelCommand
		add    []string
		remove []string
	}{
		{
			"basic",
			[]labelCommand{
				{action: "add", label: "foo"},
				{action: "remove", label: "baz"},
			},
			[]string{"foo"},
			[]string{"baz"},
		},
		{
			"add/remove of same label",
			[]labelCommand{
				{action: "add", label: "foo"},
				{action: "remove", label: "foo"},
				{action: "remove", label: "bar"},
				{action: "add", label: "bar"},
			},
			nil,
			nil,
		},
		{
			"deduplication of labels",
			[]labelCommand{
				{action: "add", label: "foo"},
				{action: "add", label: "foo"},
				{action: "remove", label: "bar"},
				{action: "remove", label: "bar"},
			},
			[]string{"foo"},
			[]string{"bar"},
		},
		{
			"forbidden actions",
			[]labelCommand{
				{action: "add", label: "Proposal-Accepted"},
				{action: "add", label: "CherryPickApproved"},
				{action: "add", label: "cla: yes"},
				{action: "remove", label: "Security"},
			},
			nil,
			nil,
		},
		{
			"can add Security",
			[]labelCommand{
				{action: "add", label: "Security"},
			},
			[]string{"Security"},
			nil,
		},
	}
	for _, tc := range testCases {
		add, remove := mutationsFromCommands(tc.cmds)
		if diff := cmp.Diff(add, tc.add); diff != "" {
			t.Errorf("%s: label additions differ: (-got, +want)\n%s", tc.desc, diff)
		}
		if diff := cmp.Diff(remove, tc.remove); diff != "" {
			t.Errorf("%s: label removals differ: (-got, +want)\n%s", tc.desc, diff)
		}
	}
}

type fakeIssuesService struct {
	labels map[int][]string
}

func (f *fakeIssuesService) ListLabelsByIssue(ctx context.Context, owner string, repo string, number int, opt *github.ListOptions) ([]*github.Label, *github.Response, error) {
	var labels []*github.Label
	if ls, ok := f.labels[number]; ok {
		for _, l := range ls {
			name := l
			labels = append(labels, &github.Label{Name: &name})
		}
	}
	return labels, nil, nil
}

func (f *fakeIssuesService) AddLabelsToIssue(ctx context.Context, owner string, repo string, number int, labels []string) ([]*github.Label, *github.Response, error) {
	if f.labels == nil {
		f.labels = map[int][]string{number: labels}
		return nil, nil, nil
	}
	ls, ok := f.labels[number]
	if !ok {
		f.labels[number] = labels
		return nil, nil, nil
	}
	for _, label := range labels {
		var found bool
		for _, l := range ls {
			if l == label {
				found = true
			}
		}
		if found {
			continue
		}
		f.labels[number] = append(f.labels[number], label)
	}
	return nil, nil, nil
}

func (f *fakeIssuesService) RemoveLabelForIssue(ctx context.Context, owner string, repo string, number int, label string) (*github.Response, error) {
	if ls, ok := f.labels[number]; ok {
		for i, l := range ls {
			if l == label {
				f.labels[number] = append(f.labels[number][:i], f.labels[number][i+1:]...)
				return nil, nil
			}
		}
	}
	// The GitHub API returns a NotFound error if the label did not exist.
	return nil, &github.ErrorResponse{
		Response: &http.Response{
			Status:     http.StatusText(http.StatusNotFound),
			StatusCode: http.StatusNotFound,
		},
	}
}

func TestAddLabels(t *testing.T) {
	testCases := []struct {
		desc   string
		gi     *maintner.GitHubIssue
		labels []string
		added  []string
	}{
		{
			"basic add",
			&maintner.GitHubIssue{},
			[]string{"foo"},
			[]string{"foo"},
		},
		{
			"some labels already present in maintner",
			&maintner.GitHubIssue{
				Labels: map[int64]*maintner.GitHubLabel{
					0: {Name: "NeedsDecision"},
				},
			},
			[]string{"foo", "NeedsDecision"},
			[]string{"foo"},
		},
		{
			"all labels already present in maintner",
			&maintner.GitHubIssue{
				Labels: map[int64]*maintner.GitHubLabel{
					0: {Name: "NeedsDecision"},
				},
			},
			[]string{"NeedsDecision"},
			nil,
		},
	}

	b := &gopherbot{}
	for _, tc := range testCases {
		// Clear any previous state from fake addLabelsToIssue since some test cases may skip calls to it.
		fis := &fakeIssuesService{}
		b.is = fis

		if err := b.addLabels(context.Background(), maintner.GitHubRepoID{
			Owner: "golang",
			Repo:  "go",
		}, tc.gi, tc.labels); err != nil {
			t.Errorf("%s: b.addLabels got unexpected error: %v", tc.desc, err)
			continue
		}
		if diff := cmp.Diff(fis.labels[int(tc.gi.ID)], tc.added); diff != "" {
			t.Errorf("%s: labels added differ: (-got, +want)\n%s", tc.desc, diff)
		}
	}
}

func TestRemoveLabels(t *testing.T) {
	testCases := []struct {
		desc     string
		gi       *maintner.GitHubIssue
		ghLabels []string
		toRemove []string
		want     []string
	}{
		{
			"basic remove",
			&maintner.GitHubIssue{
				Number: 123,
				Labels: map[int64]*maintner.GitHubLabel{
					0: {Name: "NeedsFix"},
					1: {Name: "help wanted"},
				},
			},
			[]string{"NeedsFix", "help wanted"},
			[]string{"NeedsFix"},
			[]string{"help wanted"},
		},
		{
			"label not present in maintner",
			&maintner.GitHubIssue{},
			[]string{"NeedsFix"},
			[]string{"NeedsFix"},
			[]string{"NeedsFix"},
		},
		{
			"label not present in GitHub",
			&maintner.GitHubIssue{
				Labels: map[int64]*maintner.GitHubLabel{
					0: {Name: "foo"},
				},
			},
			[]string{"NeedsFix"},
			[]string{"foo"},
			[]string{"NeedsFix"},
		},
	}

	b := &gopherbot{}
	for _, tc := range testCases {
		// Clear any previous state from fakeIssuesService since some test cases may skip calls to it.
		fis := &fakeIssuesService{map[int][]string{
			int(tc.gi.Number): tc.ghLabels,
		}}
		b.is = fis

		if err := b.removeLabels(context.Background(), maintner.GitHubRepoID{
			Owner: "golang",
			Repo:  "go",
		}, tc.gi, tc.toRemove); err != nil {
			t.Errorf("%s: b.addLabels got unexpected error: %v", tc.desc, err)
			continue
		}
		if diff := cmp.Diff(fis.labels[int(tc.gi.Number)], tc.want); diff != "" {
			t.Errorf("%s: labels differ: (-got, +want)\n%s", tc.desc, diff)
		}
	}
}

func TestHumanReviewersInMetas(t *testing.T) {
	testCases := []struct {
		commitMsg string
		hasHuman  bool
		atLeast   int
	}{
		{`Patch-set: 6
Reviewer: Andrew Bonventre <22285@62eb7196-b449-3ce5-99f1-c037f21e1705>
`,
			true,
			1,
		},
		{`Patch-set: 6
CC: Andrew Bonventre <22285@62eb7196-b449-3ce5-99f1-c037f21e1705>
`,
			true,
			1,
		},
		{`Patch-set: 6
Reviewer: Gobot Gobot <5976@62eb7196-b449-3ce5-99f1-c037f21e1705>
`,
			false,
			1,
		},
		{`Patch-set: 6
Reviewer: Gobot Gobot <5976@62eb7196-b449-3ce5-99f1-c037f21e1705>
CC: Andrew Bonventre <22285@62eb7196-b449-3ce5-99f1-c037f21e1705>
`,
			true,
			1,
		},
		{`Patch-set: 6
Reviewer: Gobot Gobot <5976@62eb7196-b449-3ce5-99f1-c037f21e1705>
Reviewer: Andrew Bonventre <22285@62eb7196-b449-3ce5-99f1-c037f21e1705>
`,
			true,
			1,
		},
		{`Patch-set: 6
Reviewer: Gobot Gobot <5976@62eb7196-b449-3ce5-99f1-c037f21e1705>
Reviewer: Andrew Bonventre <22285@62eb7196-b449-3ce5-99f1-c037f21e1705>
		`,
			false,
			2,
		},
		{`Patch-set: 6
Reviewer: Gobot Gobot <5976@62eb7196-b449-3ce5-99f1-c037f21e1705>
Reviewer: Andrew Bonventre <22285@62eb7196-b449-3ce5-99f1-c037f21e1705>
Reviewer: Rebecca Stambler <16140@62eb7196-b449-3ce5-99f1-c037f21e1705>
				`,
			true,
			2,
		},
	}

	for _, tc := range testCases {
		metas := []*maintner.GerritMeta{
			{Commit: &maintner.GitCommit{Msg: tc.commitMsg}},
		}
		if got, want := humanReviewersInMetas(metas, tc.atLeast), tc.hasHuman; got != want {
			t.Errorf("Unexpected result for meta commit message: got %v; want %v for\n%s", got, want, tc.commitMsg)
		}
	}
}

func TestMergeOwnersEntries(t *testing.T) {
	var (
		andybons = owners.Owner{GitHubUsername: "andybons", GerritEmail: "andybons@golang.org"}
		bradfitz = owners.Owner{GitHubUsername: "bradfitz", GerritEmail: "bradfitz@golang.org"}
		filippo  = owners.Owner{GitHubUsername: "filippo", GerritEmail: "filippo@golang.org"}
	)
	testCases := []struct {
		desc        string
		entries     []*owners.Entry
		authorEmail string
		result      *owners.Entry
	}{
		{
			"no entries",
			nil,
			"",
			&owners.Entry{},
		},
		{
			"primary merge",
			[]*owners.Entry{
				{Primary: []owners.Owner{andybons}},
				{Primary: []owners.Owner{bradfitz}},
			},
			"",
			&owners.Entry{
				Primary: []owners.Owner{andybons, bradfitz},
			},
		},
		{
			"secondary merge",
			[]*owners.Entry{
				{Secondary: []owners.Owner{andybons}},
				{Secondary: []owners.Owner{filippo}},
			},
			"",
			&owners.Entry{
				Secondary: []owners.Owner{andybons, filippo},
			},
		},
		{
			"promote from secondary to primary",
			[]*owners.Entry{
				{Primary: []owners.Owner{andybons, filippo}},
				{Secondary: []owners.Owner{filippo}},
			},
			"",
			&owners.Entry{
				Primary: []owners.Owner{andybons, filippo},
			},
		},
		{
			"primary filter",
			[]*owners.Entry{
				{Primary: []owners.Owner{filippo, andybons}},
			},
			filippo.GerritEmail,
			&owners.Entry{
				Primary: []owners.Owner{andybons},
			},
		},
		{
			"secondary filter",
			[]*owners.Entry{
				{Secondary: []owners.Owner{filippo, andybons}},
			},
			filippo.GerritEmail,
			&owners.Entry{
				Secondary: []owners.Owner{andybons},
			},
		},
	}
	cmpFn := func(a, b owners.Owner) bool {
		return a.GitHubUsername < b.GitHubUsername
	}
	for _, tc := range testCases {
		got := mergeOwnersEntries(tc.entries, tc.authorEmail)
		if diff := cmp.Diff(got, tc.result, cmpopts.SortSlices(cmpFn)); diff != "" {
			t.Errorf("%s: final entry results differ: (-got, +want)\n%s", tc.desc, diff)
		}
	}
}
