// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"testing"

	"github.com/google/go-github/v48/github"
	"golang.org/x/build/internal/workflow"
)

func TestCreateReleaseMilestoneAndIssue(t *testing.T) {
	testcases := []struct {
		name          string
		version       string
		fakeGithub    FakeGitHub
		wantIssue     int
		wantMilestone int
	}{
		{
			name:          "flow should create a milestone and create an issue under the milestone",
			version:       "v0.45.0-rc.1",
			fakeGithub:    FakeGitHub{}, // no issues and no milestones.
			wantIssue:     1,
			wantMilestone: 1,
		},
		{
			name:    "flow should create an issue under the existing milestone",
			version: "v0.48.0-rc.1",
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{999: "v0.48.0", 998: "v0.46.0"},
			},
			wantIssue:     1,
			wantMilestone: 999,
		},
		{
			name:    "flow should reuse the existing release issue",
			version: "v0.48.0-rc.1",
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{999: "v0.48.0", 998: "Release v0.46.0"},
				Issues:     map[int]*github.Issue{1000: {Number: github.Int(1000), Title: github.String("Release v0.48.0"), Milestone: &github.Milestone{ID: github.Int64(999)}}},
			},
			wantIssue:     1000,
			wantMilestone: 999,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &ReleaseVSCodeGoTasks{
				GitHub: &tc.fakeGithub,
			}

			semv, ok := parseSemver(tc.version)
			if !ok {
				t.Fatalf("parseSemver(%q) should success", tc.version)
			}
			issueNumber, err := tasks.createReleaseMilestoneAndIssue(&workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}, semv)
			if err != nil {
				t.Fatal(err)
			}

			issue, ok := tc.fakeGithub.Issues[issueNumber]
			if !ok {
				t.Errorf("release issue with number %v does not exist", issueNumber)
			}

			if *issue.Number != tc.wantIssue {
				t.Errorf("createReleaseMilestoneAndIssue() create an issue with number %v, but should create issue with number %v", issue.Number, tc.wantIssue)
			}

			if int(*issue.Milestone.ID) != tc.wantMilestone {
				t.Errorf("release issue is created under milestone %v should under milestone %v", *issue.Milestone.ID, tc.wantMilestone)
			}
		})
	}
}

func TestNextPrereleaseVersion(t *testing.T) {
	tests := []struct {
		name         string
		existingTags []string
		versionRule  string
		wantVersion  string
	}{
		{
			name:         "v0.44.0 have not released, have no release candidate",
			existingTags: []string{"v0.44.0", "v0.43.0", "v0.42.0"},
			versionRule:  "next minor",
			wantVersion:  "v0.46.0-rc.1",
		},
		{
			name:         "v0.44.0 have not released but already have two release candidate",
			existingTags: []string{"v0.44.0-rc.1", "v0.44.0-rc.2", "v0.43.0", "v0.42.0"},
			versionRule:  "next minor",
			wantVersion:  "v0.44.0-rc.3",
		},
		{
			name:         "v0.44.3 have not released, have no release candidate",
			existingTags: []string{"v0.44.2-rc.1", "v0.44.2", "v0.44.1", "v0.44.1-rc.1"},
			versionRule:  "next patch",
			wantVersion:  "v0.44.3-rc.1",
		},
		{
			name:         "v0.44.3 have not released but already have one release candidate",
			existingTags: []string{"v0.44.3-rc.1", "v0.44.2", "v0.44.2-rc.1", "v0.44.1", "v0.44.1-rc.1"},
			versionRule:  "next patch",
			wantVersion:  "v0.44.3-rc.2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vscodego := NewFakeRepo(t, "vscode-go")
			commit := vscodego.Commit(map[string]string{
				"go.mod": "module github.com/golang/vscode-go\n",
				"go.sum": "\n",
			})

			for _, tag := range tc.existingTags {
				vscodego.Tag(tag, commit)
			}

			gerrit := NewFakeGerrit(t, vscodego)

			tasks := &ReleaseVSCodeGoTasks{
				Gerrit: gerrit,
			}

			got, err := tasks.nextPrereleaseVersion(&workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}, tc.versionRule)
			if err != nil {
				t.Fatal(err)
			}

			want, ok := parseSemver(tc.wantVersion)
			if !ok {
				t.Fatalf("failed to parse the want version: %q", tc.wantVersion)
			}

			if want != got {
				t.Errorf("nextPrereleaseVersion(%q) = %v but want %v", tc.versionRule, got, want)
			}
		})
	}
}

func TestVSCodeGoActiveReleaseBranch(t *testing.T) {
	testcases := []struct {
		name             string
		existingBranches []string
		want             string
	}{
		{
			name:             "choose the largest release branch",
			existingBranches: []string{"release-v0.42", "release-v0.44", "release-v0.46"},
			want:             "release-v0.46",
		},
		{
			name:             "ignore any insider version release branch (should never exist)",
			existingBranches: []string{"release-v0.42", "release-v0.44", "release-v0.46", "release-v0.47"},
			want:             "release-v0.46",
		},
		{
			name:             "ignore any branch with wrong formatting",
			existingBranches: []string{"release-v0.42", "release-v0.44", "release-v0.46", "v0.48", "release-0.48"},
			want:             "release-v0.46",
		},
		{
			name:             "fall back to branch release",
			existingBranches: []string{"foo", "bar"},
			want:             "release",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			vscodego := NewFakeRepo(t, "vscode-go")
			commit := vscodego.Commit(map[string]string{
				"go.mod": "module github.com/golang/vscode-go\n",
				"go.sum": "\n",
			})

			for _, branch := range tc.existingBranches {
				vscodego.Branch(branch, commit)
			}

			gerrit := NewFakeGerrit(t, vscodego)
			ctx := &workflow.TaskContext{
				Context: context.Background(),
				Logger:  &testLogger{t, ""},
			}
			got, err := vsCodeGoActiveReleaseBranch(ctx, gerrit)
			if err != nil {
				t.Fatal(err)
			}

			if tc.want != got {
				t.Errorf("vsCodeGoActiveReleaseBranch() = %q, want %q", got, tc.want)
			}

		})
	}
}
