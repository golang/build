// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-github/v48/github"
	"golang.org/x/build/internal/workflow"
)

func TestLatestVersion(t *testing.T) {
	testcases := []struct {
		name           string
		input          []string
		filters        []func(releaseVersion, string) bool
		wantRelease    releaseVersion
		wantPrerelease string
	}{
		{
			name:           "choose the latest version v2.1.0",
			input:          []string{"v1.0.0", "v2.0.0", "v2.1.0"},
			wantRelease:    releaseVersion{Major: 2, Minor: 1, Patch: 0},
			wantPrerelease: "",
		},
		{
			name:           "choose the latest version v2.2.0-pre.1",
			input:          []string{"v1.0.0", "v2.0.0", "v2.1.0", "v2.2.0-pre.1"},
			wantRelease:    releaseVersion{Major: 2, Minor: 2, Patch: 0},
			wantPrerelease: "pre.1",
		},
		{
			name:           "choose the latest pre-release version v2.2.0-pre.1",
			input:          []string{"v1.0.0", "v2.0.0", "v2.1.0", "v2.2.0-pre.1", "v2.3.0"},
			filters:        []func(releaseVersion, string) bool{isPrereleaseVersion},
			wantRelease:    releaseVersion{Major: 2, Minor: 2, Patch: 0},
			wantPrerelease: "pre.1",
		},
		{
			name:        "choose the latest release version v2.1.0",
			input:       []string{"v1.0.0", "v2.0.0", "v2.1.0", "v2.2.0-pre.1"},
			filters:     []func(releaseVersion, string) bool{isReleaseVersion},
			wantRelease: releaseVersion{Major: 2, Minor: 1, Patch: 0},
		},
		{
			name:           "choose the latest version among v2.2.0",
			input:          []string{"v1.0.0", "v2.0.0", "v2.1.0", "v2.2.0-pre.3", "v2.2.0-pre.2", "v2.2.0-pre.1", "v2.3.0"},
			filters:        []func(releaseVersion, string) bool{isSameReleaseVersion(releaseVersion{Major: 2, Minor: 2, Patch: 0})},
			wantRelease:    releaseVersion{Major: 2, Minor: 2, Patch: 0},
			wantPrerelease: "pre.3",
		},
		{
			name:        "release version is consider newer than prerelease version",
			input:       []string{"v1.0.0", "v2.0.0", "v2.1.0", "v2.2.0", "v2.2.0-pre.2", "v2.2.0-pre.3", "v2.2.0-pre.1", "v2.3.0"},
			filters:     []func(releaseVersion, string) bool{isSameReleaseVersion(releaseVersion{Major: 2, Minor: 2, Patch: 0})},
			wantRelease: releaseVersion{Major: 2, Minor: 2, Patch: 0},
		},
		{
			name:           "choose the latest pre-release version among v2.2.0",
			input:          []string{"v1.0.0", "v2.0.0", "v2.1.0", "v2.2.0", "v2.2.0-pre.2", "v2.2.0-pre.3", "v2.2.0-pre.1", "v2.3.0"},
			filters:        []func(releaseVersion, string) bool{isPrereleaseVersion, isSameReleaseVersion(releaseVersion{Major: 2, Minor: 2, Patch: 0})},
			wantRelease:    releaseVersion{Major: 2, Minor: 2, Patch: 0},
			wantPrerelease: "pre.3",
		},
		{
			name:           "choose the latest pre-release version matching pattern among v2.2.0",
			input:          []string{"v2.2.0-pre.2", "v2.2.0-pre.3"},
			filters:        []func(releaseVersion, string) bool{isPrereleaseMatchRegex(`^pre\.\d+$`), isSameReleaseVersion(releaseVersion{Major: 2, Minor: 2, Patch: 0})},
			wantRelease:    releaseVersion{Major: 2, Minor: 2, Patch: 0},
			wantPrerelease: "pre.3",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotRelease, gotPrerelease := latestVersion(tc.input, tc.filters...)
			if gotRelease != tc.wantRelease {
				t.Errorf("latestVersion() = %v, want %v", gotRelease, tc.wantRelease)
			}
			if gotPrerelease != tc.wantPrerelease {
				t.Errorf("latestVersion() = %v, want %v", gotPrerelease, tc.wantPrerelease)
			}
		})
	}
}

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

			release, _, ok := parseVersion(tc.version)
			if !ok {
				t.Fatalf("parseVersion(%q) failed", tc.version)
			}
			issueNumber, err := tasks.createReleaseMilestoneAndIssue(&workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}, release)
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

func TestCreateReleaseBranch(t *testing.T) {
	ctx := context.Background()
	testcases := []struct {
		name           string
		version        string
		existingBranch bool
		wantErr        bool
	}{
		{
			name:           "nil if the release branch does not exist for first rc in a minor release",
			version:        "v0.44.0-rc.1",
			existingBranch: false,
			wantErr:        false,
		},
		{
			name:           "nil if the release branch already exist for non-initial rc in a minor release",
			version:        "v0.44.0-rc.4",
			existingBranch: true,
			wantErr:        false,
		},
		{
			name:           "fail if the release branch does not exist for non-initial rc in a minor release",
			version:        "v0.44.0-rc.4",
			existingBranch: false,
			wantErr:        true,
		},
		{
			name:           "nil if the release branch already exist for a patch version",
			version:        "v0.44.3-rc.3",
			existingBranch: true,
			wantErr:        false,
		},
		{
			name:           "fail if the release branch does not exist for a patch version",
			version:        "v0.44.3-rc.3",
			existingBranch: false,
			wantErr:        true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			release, prerelease, ok := parseVersion(tc.version)
			if !ok {
				t.Fatalf("failed to parse the want version: %q", tc.version)
			}

			vscodego := NewFakeRepo(t, "vscode-go")
			commit := vscodego.Commit(map[string]string{
				"go.mod": "module github.com/golang/vscode-go\n",
				"go.sum": "\n",
			})
			if tc.existingBranch {
				vscodego.Branch(fmt.Sprintf("release-v%v.%v", release.Major, release.Minor), commit)
			}

			gerrit := NewFakeGerrit(t, vscodego)
			tasks := &ReleaseVSCodeGoTasks{
				Gerrit: gerrit,
			}

			err := tasks.createReleaseBranch(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, release, prerelease)
			if tc.wantErr && err == nil {
				t.Errorf("createReleaseBranch(%q) should return error but return nil", tc.version)
			} else if !tc.wantErr && err != nil {
				t.Errorf("createReleaseBranch(%q) should return nil but return err: %v", tc.version, err)
			}

			if !tc.wantErr {
				if _, err := gerrit.ReadBranchHead(ctx, "vscode-go", fmt.Sprintf("release-v%v.%v", release.Major, release.Minor)); err != nil {
					t.Errorf("createReleaseBranch(%q) should ensure the release branch creation: %v", tc.version, err)
				}
			}
		})
	}
}

func TestDetermineReleaseAndNextPrereleaseVersion(t *testing.T) {
	ctx := workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}
	tests := []struct {
		name           string
		existingTags   []string
		versionRule    string
		wantRelease    releaseVersion
		wantPrerelease string
	}{
		{
			name:           "v0.44.0 have not released, have no release candidate",
			existingTags:   []string{"v0.44.0", "v0.43.0", "v0.42.0"},
			versionRule:    "next minor",
			wantRelease:    releaseVersion{Major: 0, Minor: 46, Patch: 0},
			wantPrerelease: "rc.1",
		},
		{
			name:           "v0.44.0 have not released but already have two release candidate",
			existingTags:   []string{"v0.44.0-rc.1", "v0.44.0-rc.2", "v0.43.0", "v0.42.0"},
			versionRule:    "next minor",
			wantRelease:    releaseVersion{Major: 0, Minor: 44, Patch: 0},
			wantPrerelease: "rc.3",
		},
		{
			name:           "v0.44.3 have not released, have no release candidate",
			existingTags:   []string{"v0.44.2-rc.1", "v0.44.2", "v0.44.1", "v0.44.1-rc.1"},
			versionRule:    "next patch",
			wantRelease:    releaseVersion{Major: 0, Minor: 44, Patch: 3},
			wantPrerelease: "rc.1",
		},
		{
			name:           "v0.44.3 have not released but already have one release candidate",
			existingTags:   []string{"v0.44.3-rc.1", "v0.44.2", "v0.44.2-rc.1", "v0.44.1", "v0.44.1-rc.1"},
			versionRule:    "next patch",
			wantRelease:    releaseVersion{Major: 0, Minor: 44, Patch: 3},
			wantPrerelease: "rc.2",
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

			gotRelease, err := tasks.determineReleaseVersion(&ctx, tc.versionRule)
			if err != nil || gotRelease != tc.wantRelease {
				t.Errorf("determineReleaseVersion(%q) = (%v, %v), want (%v, nil)", tc.versionRule, gotRelease, err, tc.wantRelease)
			}

			gotPrerelease, err := tasks.nextPrereleaseVersion(&ctx, gotRelease)
			if err != nil || tc.wantPrerelease != gotPrerelease {
				t.Errorf("nextPrerelease(%v) = (%s, %v) but want (%s, nil)", gotRelease, gotPrerelease, err, tc.wantPrerelease)
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
			got, err := vscodeGoActiveReleaseBranch(ctx, gerrit)
			if err != nil {
				t.Fatal(err)
			}

			if tc.want != got {
				t.Errorf("vscodeGoActiveReleaseBranch() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVerifyTestResults(t *testing.T) {
	mustHaveShell(t)
	fakeScriptFmt := `#!/bin/bash -exu

case "$1" in
"testlocal")
	echo "the testlocal return %v"
	exit %v
	;;
*)
	echo unexpected command $@
	exit 1
	;;
esac
`
	testcases := []struct {
		name    string
		rc      int
		wantErr bool
	}{
		{
			name:    "test failed, return error",
			rc:      1,
			wantErr: true,
		},
		{
			name:    "test passed, return nil",
			rc:      0,
			wantErr: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			vscodego := NewFakeRepo(t, "vscode-go")
			commit := vscodego.Commit(map[string]string{
				"go.mod":         "module github.com/golang/vscode-go\n",
				"go.sum":         "\n",
				"build/all.bash": fmt.Sprintf(fakeScriptFmt, tc.rc, tc.rc),
			})
			// Overwrite the script to empty to make sure vscode-go flow will checkout
			// the specific commit.
			_ = vscodego.Commit(map[string]string{
				"build/all.bash": "",
			})

			gerrit := NewFakeGerrit(t, vscodego)
			ctx := &workflow.TaskContext{
				Context: context.Background(),
				Logger:  &testLogger{t, ""},
			}

			tasks := &ReleaseVSCodeGoTasks{
				Gerrit:     gerrit,
				CloudBuild: NewFakeCloudBuild(t, gerrit, "vscode-go", nil, ""),
			}

			err := tasks.verifyTestResults(ctx, commit)
			if tc.wantErr && err == nil {
				t.Errorf("verifyTestResult() should return error but return nil")
			} else if !tc.wantErr && err != nil {
				t.Errorf("verifyTestResult() should return nil but return err: %v", err)
			}
		})
	}
}

func TestDetermineInsiderVersion(t *testing.T) {
	testcases := []struct {
		name         string
		existingTags []string
		want         releaseVersion
	}{
		{
			name:         "pick v0.45.0 because there is no other v0.45.X",
			existingTags: []string{"v0.44.0", "v0.44.1", "v0.44.2"},
			want:         releaseVersion{Major: 0, Minor: 45, Patch: 0},
		},
		{
			name:         "pick v0.45.3 because there is v0.45.2",
			existingTags: []string{"v0.44.0", "v0.44.1", "v0.44.2", "v0.45.2"},
			want:         releaseVersion{Major: 0, Minor: 45, Patch: 3},
		},
		{
			name:         "pick v0.47.4 because there is v0.47.3",
			existingTags: []string{"v0.44.0", "v0.45.2", "v0.46.0", "v0.47.3"},
			want:         releaseVersion{Major: 0, Minor: 47, Patch: 4},
		},
	}

	for _, tc := range testcases {
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
			ctx := &workflow.TaskContext{
				Context: context.Background(),
				Logger:  &testLogger{t, ""},
			}

			tasks := &ReleaseVSCodeGoTasks{
				Gerrit: gerrit,
			}

			got, err := tasks.determineInsiderVersion(ctx)
			if err != nil || got != tc.want {
				t.Errorf("determineInsiderVersion() = (%v, %v), want (%v, nil)", got, err, tc.want)
			}
		})
	}
}
