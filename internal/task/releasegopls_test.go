// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v48/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

func TestPossibleGoplsVersions(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want []string
	}{
		{
			name: "any one version tag should have three possible next versions",
			tags: []string{"gopls/v1.2.3"},
			want: []string{"v1.2.4", "v1.3.0", "v2.0.0"},
		},
		{
			name: "1.2.0 should be skipped because 1.2.3 already exist",
			tags: []string{"gopls/v1.2.3", "gopls/v1.1.0"},
			want: []string{"v1.1.1", "v1.2.4", "v1.3.0", "v2.0.0"},
		},
		{
			name: "2.0.0 should be skipped because 2.1.3 already exist",
			tags: []string{"gopls/v1.2.3", "gopls/v2.1.3"},
			want: []string{"v1.2.4", "v1.3.0", "v2.1.4", "v2.2.0", "v3.0.0"},
		},
		{
			name: "1.2.0 is still consider valid version because there is no 1.2.X",
			tags: []string{"gopls/v1.1.3", "gopls/v1.3.2", "gopls/v2.1.2"},
			want: []string{"v1.1.4", "v1.2.0", "v1.3.3", "v1.4.0", "v2.1.3", "v2.2.0", "v3.0.0"},
		},
		{
			name: "2.0.0 is still consider valid version because there is no 2.X.X",
			tags: []string{"gopls/v1.2.3", "gopls/v3.1.2"},
			want: []string{"v1.2.4", "v1.3.0", "v2.0.0", "v3.1.3", "v3.2.0", "v4.0.0"},
		},
		{
			name: "pre-release version tag should not have any effect on the next version",
			tags: []string{"gopls/v0.16.1-pre.1", "gopls/v0.16.1-pre.2", "gopls/v0.16.0"},
			want: []string{"v0.16.1", "v0.17.0", "v1.0.0"},
		},
		{
			name: "other unrelated tag should not have any effect on the next version",
			tags: []string{"v0.9.2", "v0.9.3", "v0.23.0", "gopls/v0.16.0"},
			want: []string{"v0.16.1", "v0.17.0", "v1.0.0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			commit := tools.Commit(map[string]string{
				"go.mod": "module golang.org/x/tools\n",
				"go.sum": "\n",
			})

			for _, tag := range tc.tags {
				tools.Tag(tag, commit)
			}

			gerrit := NewFakeGerrit(t, tools)

			tasks := &PrereleaseGoplsTasks{
				Gerrit: gerrit,
			}

			got, err := tasks.possibleGoplsVersions(&workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}})
			if err != nil {
				t.Fatalf("possibleGoplsVersions() should not return error, but return %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("possibleGoplsVersions() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCreateBranchIfMinor(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name           string
		version        string
		existingBranch string
		wantErr        bool
		wantBranch     string
	}{
		{
			name:       "should create a release branch for a minor release",
			version:    "v1.2.0",
			wantErr:    false,
			wantBranch: "gopls-release-branch.1.2",
		},
		{
			name:           "should return nil if the release branch already exist for a minor release",
			version:        "v1.2.0",
			existingBranch: "gopls-release-branch.1.2",
			wantErr:        false,
		},
		{
			name:           "should not create a release branch for a patch release",
			version:        "v1.2.4",
			existingBranch: "gopls-release-branch.1.2",
			wantErr:        false,
			wantBranch:     "",
		},
		{
			name:       "should throw error for patch release if release branch is missing",
			version:    "v1.3.1",
			wantErr:    true,
			wantBranch: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			_ = tools.Commit(map[string]string{
				"go.mod": "module golang.org/x/tools\n",
				"go.sum": "\n",
			})
			_ = tools.Commit(map[string]string{
				"README.md": "THIS IS READ ME.",
			})

			gerritClient := NewFakeGerrit(t, tools)

			masterHead, err := gerritClient.ReadBranchHead(ctx, "tools", "master")
			if err != nil {
				t.Fatalf("ReadBranchHead should be able to get revision of master branch's head: %v", err)
			}

			if tc.existingBranch != "" {
				if _, err := gerritClient.CreateBranch(ctx, "tools", tc.existingBranch, gerrit.BranchInput{Revision: masterHead}); err != nil {
					t.Fatalf("failed to create the branch %q: %v", tc.existingBranch, err)
				}
			}

			tasks := &PrereleaseGoplsTasks{
				Gerrit: gerritClient,
			}

			semv, _ := parseSemver(tc.version)
			err = tasks.createBranchIfMinor(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, semv)

			if tc.wantErr && err == nil {
				t.Errorf("createBranchIfMinor() should return error but return nil")
			} else if !tc.wantErr && err != nil {
				t.Errorf("createBranchIfMinor() should return nil but return err: %v", err)
			}

			// Created branch should have same revision as master branch's head.
			if tc.wantBranch != "" {
				gotRevision, err := gerritClient.ReadBranchHead(ctx, "tools", tc.wantBranch)
				if err != nil {
					t.Errorf("ReadBranchHead should be able to get revision of %s branch's head: %v", tc.wantBranch, err)
				}
				if masterHead != gotRevision {
					t.Errorf("createBranchIfMinor() = %q, want %q", gotRevision, masterHead)
				}
			}
		})
	}
}

func TestUpdateCodeReviewConfig(t *testing.T) {
	ctx := context.Background()
	testcases := []struct {
		name       string
		version    string
		config     string
		wantCommit bool
		wantConfig string
	}{
		{
			name:       "should update the codereview.cfg with version 1.2 for input minor release 1.2.0",
			version:    "v1.2.0",
			config:     "foo",
			wantCommit: true,
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.1.2
parent-branch: master
`,
		},
		{
			name:       "should update the codereview.cfg with version 1.2 for input patch release 1.2.3",
			version:    "v1.2.3",
			config:     "foo",
			wantCommit: true,
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.1.2
parent-branch: master
`,
		},
		{
			name:    "no need to update the config for a minor release 1.3.0",
			version: "v1.3.0",
			config: `issuerepo: golang/go
branch: gopls-release-branch.1.3
parent-branch: master
`,
			wantCommit: false,
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.1.3
parent-branch: master
`,
		},
		{
			name:    "no need to update the config for a patch release 1.3.3",
			version: "v1.3.3",
			config: `issuerepo: golang/go
branch: gopls-release-branch.1.3
parent-branch: master
`,
			wantCommit: false,
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.1.3
parent-branch: master
`,
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			_ = tools.Commit(map[string]string{
				"go.mod": "module golang.org/x/tools\n",
				"go.sum": "\n",
			})
			_ = tools.Commit(map[string]string{
				"codereview.cfg": tc.config,
			})

			gerritClient := NewFakeGerrit(t, tools)

			headMaster, err := gerritClient.ReadBranchHead(ctx, "tools", "master")
			if err != nil {
				t.Fatalf("ReadBranchHead should be able to get revision of master branch's head: %v", err)
			}

			configMaster, err := gerritClient.ReadFile(ctx, "tools", headMaster, "codereview.cfg")
			if err != nil {
				t.Fatalf("ReadFile should be able to read the codereview.cfg file from master branch head: %v", err)
			}

			semv, _ := parseSemver(tc.version)
			releaseBranch := goplsReleaseBranchName(semv)
			if _, err := gerritClient.CreateBranch(ctx, "tools", releaseBranch, gerrit.BranchInput{Revision: headMaster}); err != nil {
				t.Fatalf("failed to create the branch %q: %v", releaseBranch, err)
			}

			headRelease, err := gerritClient.ReadBranchHead(ctx, "tools", releaseBranch)
			if err != nil {
				t.Fatalf("ReadBranchHead should be able to get revision of release branch's head: %v", err)
			}

			tasks := &PrereleaseGoplsTasks{
				Gerrit:     gerritClient,
				CloudBuild: NewFakeCloudBuild(t, gerritClient, "", nil, fakeGo),
			}

			_, err = tasks.updateCodeReviewConfig(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, semv, nil, 0)
			if err != nil {
				t.Fatalf("updateCodeReviewConfig() returns error: %v", err)
			}

			// master branch's head commit should not change.
			headMasterAfter, err := gerritClient.ReadBranchHead(ctx, "tools", "master")
			if err != nil {
				t.Fatalf("ReadBranchHead() should be able to get revision of master branch's head: %v", err)
			}
			if headMasterAfter != headMaster {
				t.Errorf("updateCodeReviewConfig() should not change master branch's head, got = %s want = %s", headMasterAfter, headMaster)
			}

			// master branch's head codereview.cfg content should not change.
			configMasterAfter, err := gerritClient.ReadFile(ctx, "tools", headMasterAfter, "codereview.cfg")
			if err != nil {
				t.Fatalf("ReadFile() should be able to read the codereview.cfg file from master branch head: %v", err)
			}
			if diff := cmp.Diff(configMaster, configMasterAfter); diff != "" {
				t.Errorf("updateCodeReviewConfig() should not change codereview.cfg content in master branch (-want +got) \n %s", diff)
			}

			// verify the release branch commit have the expected behavior.
			headReleaseAfter, err := gerritClient.ReadBranchHead(ctx, "tools", releaseBranch)
			if err != nil {
				t.Fatalf("ReadBranchHead() should be able to get revision of master branch's head: %v", err)
			}
			if tc.wantCommit && headReleaseAfter == headRelease {
				t.Errorf("updateCodeReviewConfig() should have one commit to release branch, head of branch got = %s want = %s", headRelease, headReleaseAfter)
			} else if !tc.wantCommit && headReleaseAfter != headRelease {
				t.Errorf("updateCodeReviewConfig() should have not change release branch's head, got = %s want = %s", headRelease, headReleaseAfter)
			}

			// verify the release branch configreview.cfg have the expected content.
			configReleaseAfter, err := gerritClient.ReadFile(ctx, "tools", headReleaseAfter, "codereview.cfg")
			if err != nil {
				t.Fatalf("ReadFile() should be able to read the codereview.cfg file from release branch head: %v", err)
			}
			if diff := cmp.Diff(tc.wantConfig, string(configReleaseAfter)); diff != "" {
				t.Errorf("codereview.cfg mismatch (-want +got) \n %s", diff)
			}
		})
	}
}

func TestNextPrerelease(t *testing.T) {
	ctx := context.Background()
	testcases := []struct {
		name    string
		tags    []string
		version string
		want    string
	}{
		{
			name:    "next pre-release is 2",
			tags:    []string{"gopls/v0.16.0-pre.0", "gopls/v0.16.0-pre.1"},
			version: "v0.16.0",
			want:    "pre.2",
		},
		{
			name:    "next pre-release is 2 regardless of other minor or patch version",
			tags:    []string{"gopls/v0.16.0-pre.0", "gopls/v0.16.0-pre.1", "gopls/v0.16.1-pre.1", "gopls/v0.2.0-pre.3"},
			version: "v0.16.0",
			want:    "pre.2",
		},
		{
			name:    "next pre-release is 2 regardless of non-int prerelease version",
			tags:    []string{"gopls/v0.16.0-pre.0", "gopls/v0.16.0-pre.1", "gopls/v0.16.0-pre.foo", "gopls/v0.16.0-pre.bar"},
			version: "v0.16.0",
			want:    "pre.2",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			commit := tools.Commit(map[string]string{
				"go.mod": "module golang.org/x/tools\n",
				"go.sum": "\n",
			})

			for _, tag := range tc.tags {
				tools.Tag(tag, commit)
			}

			gerrit := NewFakeGerrit(t, tools)

			tasks := &PrereleaseGoplsTasks{
				Gerrit: gerrit,
			}

			semv, ok := parseSemver(tc.version)
			if !ok {
				t.Fatalf("parseSemver(%q) should success", tc.version)
			}
			got, err := tasks.nextPrerelease(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, semv)
			if err != nil {
				t.Fatalf("nextPrerelease(%q) should not return error: %v", tc.version, err)
			}

			if tc.want != got {
				t.Errorf("nextPrerelease(%q) = %v want %v", tc.version, got, tc.want)
			}
		})
	}
}

func TestCreateReleaseIssue(t *testing.T) {
	ctx := context.Background()
	testcases := []struct {
		name       string
		version    string
		fakeGithub FakeGitHub
		wantErr    bool
		wantIssue  int64
	}{
		{
			name:      "milestone does not exist",
			version:   "v0.16.2",
			wantErr:   true,
			wantIssue: -1,
		},
		{
			name:    "irrelevant milestone exist",
			version: "v0.16.2",
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.1"},
			},
			wantErr:   true,
			wantIssue: -1,
		},
		{
			name:    "milestone exist, issue is missing, workflow should create this issue",
			version: "v0.16.2",
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.2"},
			},
			wantErr:   false,
			wantIssue: 1,
		},
		{
			name:    "milestone exist, issue exist, workflow should reuse the issue",
			version: "v0.16.2",
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.2"},
				Issues:     map[int]*github.Issue{2: {Number: pointTo(2), Title: pointTo("x/tools/gopls: release version v0.16.2"), Milestone: &github.Milestone{ID: pointTo(int64(1))}}},
			},
			wantErr:   false,
			wantIssue: 2,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &PrereleaseGoplsTasks{
				Github: &tc.fakeGithub,
			}

			semv, ok := parseSemver(tc.version)
			if !ok {
				t.Fatalf("parseSemver(%q) should success", tc.version)
			}
			gotIssue, err := tasks.createReleaseIssue(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, semv)

			if tc.wantErr && err == nil {
				t.Errorf("createReleaseIssue(%s) should return error but return nil", tc.version)
			} else if !tc.wantErr && err != nil {
				t.Errorf("createReleaseIssue(%s) should return nil but return err: %v", tc.version, err)
			}

			if tc.wantIssue != gotIssue {
				t.Errorf("createReleaseIssue(%s) = %v, want %v", tc.version, gotIssue, tc.wantIssue)
			}
		})
	}
}
