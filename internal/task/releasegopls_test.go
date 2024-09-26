// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v48/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

func TestInterpretNextRelease(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		bump string
		want releaseVersion
	}{
		{
			name: "next minor version of v0.0.0 is v0.1.0",
			tags: []string{"gopls/v0.0.0"},
			bump: "next minor",
			want: releaseVersion{Major: 0, Minor: 1, Patch: 0},
		},
		{
			name: "pre-release versions should be ignored",
			tags: []string{"gopls/v0.0.0", "gopls/v0.1.0-pre.1", "gopls/v0.1.0-pre.2"},
			bump: "next minor",
			want: releaseVersion{Major: 0, Minor: 1, Patch: 0},
		},
		{
			name: "next patch version of v0.2.2 is v0.2.3",
			tags: []string{"gopls/0.1.1", "gopls/0.2.0", "gopls/0.2.1", "gopls/v0.2.2"},
			bump: "next patch",
			want: releaseVersion{Major: 0, Minor: 2, Patch: 3},
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

			tasks := &ReleaseGoplsTasks{
				Gerrit: gerrit,
			}

			got, err := tasks.interpretNextRelease(&workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}, tc.bump)
			if err != nil {
				t.Fatalf("interpretNextRelease(%q) should not return error, but return %v", tc.bump, err)
			}
			if tc.want != got {
				t.Errorf("interpretNextRelease(%q) = %v, want %v", tc.bump, tc.want, got)
			}
		})
	}
}

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
			name: "v1.2.3 and v1.1.0 share the same next major version",
			tags: []string{"gopls/v1.2.3", "gopls/v1.1.0"},
			want: []string{"v1.1.1", "v1.2.0", "v1.2.4", "v1.3.0", "v2.0.0"},
		},
		{
			name: "two versions without any duplicate next version should have 6 possible versions",
			tags: []string{"gopls/v1.1.3", "gopls/v2.1.2"},
			want: []string{"v1.1.4", "v1.2.0", "v2.0.0", "v2.1.3", "v2.2.0", "v3.0.0"},
		},
		{
			name: "v1.1.0 and v1.1.0 share the same next major and next minor",
			tags: []string{"gopls/v1.1.2", "gopls/v1.1.0"},
			want: []string{"v1.1.1", "v1.1.3", "v1.2.0", "v2.0.0"},
		},
		{
			name: "v1.1.0 next patch v1.1.1 already exist",
			tags: []string{"gopls/v1.1.1", "gopls/v1.1.0"},
			want: []string{"v1.1.2", "v1.2.0", "v2.0.0"},
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

			tasks := &ReleaseGoplsTasks{
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

			tasks := &ReleaseGoplsTasks{
				Gerrit: gerritClient,
			}

			release, _, _ := parseVersion(tc.version)
			err = tasks.createBranchIfMinor(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, release)

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

			release, _, _ := parseVersion(tc.version)
			releaseBranch := goplsReleaseBranchName(release)
			if _, err := gerritClient.CreateBranch(ctx, "tools", releaseBranch, gerrit.BranchInput{Revision: headMaster}); err != nil {
				t.Fatalf("failed to create the branch %q: %v", releaseBranch, err)
			}

			headRelease, err := gerritClient.ReadBranchHead(ctx, "tools", releaseBranch)
			if err != nil {
				t.Fatalf("ReadBranchHead should be able to get revision of release branch's head: %v", err)
			}

			tasks := &ReleaseGoplsTasks{
				Gerrit:     gerritClient,
				CloudBuild: NewFakeCloudBuild(t, gerritClient, "", nil),
			}

			_, err = tasks.updateCodeReviewConfig(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, release, nil, 0)
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

			tasks := &ReleaseGoplsTasks{
				Gerrit: gerrit,
			}

			release, _, ok := parseVersion(tc.version)
			if !ok {
				t.Fatalf("parseVersion(%q) failed", tc.version)
			}
			got, err := tasks.nextPrereleaseVersion(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, release)
			if err != nil || tc.want != got {
				t.Errorf("nextPrereleaseVersion(%q) = (%v, %v) but want (%v, nil)", tc.version, got, err, tc.want)
			}
		})
	}
}

func TestFindOrCreateReleaseIssue(t *testing.T) {
	ctx := context.Background()
	testcases := []struct {
		name       string
		version    string
		create     bool
		fakeGithub FakeGitHub
		wantErr    bool
		wantIssue  int64
	}{
		{
			name:      "milestone does not exist",
			version:   "v0.16.2",
			create:    true,
			wantErr:   true,
			wantIssue: 0,
		},
		{
			name:    "irrelevant milestone exist",
			version: "v0.16.2",
			create:  true,
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.1"},
			},
			wantErr:   true,
			wantIssue: 0,
		},
		{
			name:    "milestone exist, issue is missing, create true, workflow should create this issue",
			version: "v0.16.2",
			create:  true,
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.2"},
			},
			wantErr:   false,
			wantIssue: 1,
		},
		{
			name:    "milestone exist, issue is missing, create false, workflow error out",
			version: "v0.16.2",
			create:  false,
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.2"},
			},
			wantErr:   true,
			wantIssue: 0,
		},
		{
			name:    "milestone exist, issue exist, create true, workflow should reuse the issue",
			version: "v0.16.2",
			create:  true,
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.2"},
				Issues:     map[int]*github.Issue{2: {Number: github.Int(2), Title: github.String("x/tools/gopls: release version v0.16.2"), Milestone: &github.Milestone{ID: github.Int64(1)}}},
			},
			wantErr:   false,
			wantIssue: 2,
		},
		{
			name:    "milestone exist, issue exist, create false, workflow should reuse the issue",
			version: "v0.16.2",
			create:  false,
			fakeGithub: FakeGitHub{
				Milestones: map[int]string{1: "gopls/v0.16.2"},
				Issues:     map[int]*github.Issue{2: {Number: github.Int(2), Title: github.String("x/tools/gopls: release version v0.16.2"), Milestone: &github.Milestone{ID: github.Int64(1)}}},
			},
			wantErr:   false,
			wantIssue: 2,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &ReleaseGoplsTasks{
				Github: &tc.fakeGithub,
			}

			release, _, ok := parseVersion(tc.version)
			if !ok {
				t.Fatalf("parseVersion(%q) failed", tc.version)
			}
			gotIssue, err := tasks.findOrCreateGitHubIssue(&workflow.TaskContext{Context: ctx, Logger: &testLogger{t, ""}}, release, tc.create)

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

func TestGoplsPrereleaseFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping an end-to-end workflow test in short mode")
	}
	mustHaveShell(t)

	testcases := []struct {
		name string
		// The fields below are the prepared states before running the gopls
		// pre-release flow.
		// commitTags specifies a sequence of (possibly) tagged commits.
		// For each entry, a new commit is created, and if the entry is
		// non empty that commit is tagged with the entry value.
		commitTags []string
		// If set, create the release branch before starting the workflow.
		createBranch bool
		config       string
		release      releaseVersion
		// fields below are the desired states.
		wantVersion string
		wantConfig  string
		wantCommits int
	}{
		{
			name:         "update all three file through two commits",
			commitTags:   []string{"gopls/v0.0.0"},
			createBranch: true,
			config:       " ",
			release:      releaseVersion{Major: 0, Minor: 1, Patch: 0},
			wantVersion:  "v0.1.0-pre.1",
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.0.1
parent-branch: master
`,
			wantCommits: 2,
		},
		{
			name:         "codereview.cfg already have expected content, update go.mod and go.sum with one commit",
			commitTags:   []string{"gopls/v0.0.0"},
			createBranch: true,
			config: `issuerepo: golang/go
branch: gopls-release-branch.0.1
parent-branch: master
`,
			release:     releaseVersion{Major: 0, Minor: 1, Patch: 0},
			wantVersion: "v0.1.0-pre.1",
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.0.1
parent-branch: master
`,
			wantCommits: 1,
		},
		{
			name:         "create the branch for minor version",
			commitTags:   []string{"gopls/v0.11.0"},
			createBranch: false,
			config:       ` `,
			release:      releaseVersion{Major: 0, Minor: 12, Patch: 0},
			wantVersion:  "v0.12.0-pre.1",
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.0.12
parent-branch: master
`,
			wantCommits: 2,
		},
		{
			name:         "workflow should increment the pre-release number to 4",
			commitTags:   []string{"gopls/v0.8.2", "gopls/v0.8.3-pre.1", "gopls/v0.8.3-pre.2", "gopls/v0.8.3-pre.3"},
			createBranch: true,
			config:       " ",
			release:      releaseVersion{Major: 0, Minor: 8, Patch: 3},
			wantVersion:  "v0.8.3-pre.4",
			wantConfig: `issuerepo: golang/go
branch: gopls-release-branch.0.8
parent-branch: master
`,
			wantCommits: 2,
		},
	}

	for _, tc := range testcases {
		runTestWithInput := func(input map[string]any) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			vscodego := NewFakeRepo(t, "vscode-go")
			initial := vscodego.Commit(map[string]string{"extension/src/goToolsInformation.ts": "foo"})
			vscodego.Branch("release-v0.44", initial)

			tools := NewFakeRepo(t, "tools")
			beforeHead := tools.Commit(map[string]string{
				"gopls/go.mod":   "module golang.org/x/tools\n",
				"gopls/go.sum":   "\n",
				"codereview.cfg": tc.config,
			})
			// Create the release branch and make a few commits to the master branch.
			// Var beforeHead is used to track the commit of release branch's head
			// before trigger the gopls pre-release run. If we do not need to create a
			// release branch, beforeHead will point to the initial commit in the
			// master branch.
			if len(tc.commitTags) != 0 {
				for i, tag := range tc.commitTags {
					commit := tools.CommitOnBranch("master", map[string]string{
						"README.md": fmt.Sprintf("THIS IS READ ME FOR %v.", i),
					})
					beforeHead = commit
					if tag != "" {
						tools.Tag(tag, commit)
					}
				}
			}

			if tc.createBranch {
				tools.Branch(goplsReleaseBranchName(tc.release), beforeHead)
			}

			gerrit := NewFakeGerrit(t, tools, vscodego)

			// fakeGo handles multiple arguments in gopls pre-release flow.
			// - go get will write fake go.sum and go.mod to simulate pining the
			// x/tools dependency.
			// - go install will write a fake script in bin/gopls and grant execute
			// permission to it to simulate gopls installation.
			// - go env will return the current dir so gopls will point to the fake
			// script that is written by go install.
			// - go run will write "bar" content to file in vscode-go project
			// containing gopls versions.
			// - go mod will exit without any error.
			var fakeGo = fmt.Sprintf(`#!/bin/bash -exu

case "$1" in
"get")
	echo -n "test go sum" > go.sum
	echo -n "test go mod" > go.mod
	;;
"install")
	mkdir bin
	# write following content to bin/gopls
	# make sure the gopls version and gopls references have return code 0.
	cat <<EOF > bin/gopls
#!/bin/bash -exu

case "\$1" in
"version")
	echo %q
	;;
"references")
	exit 0
	;;
*)
	echo unexpected command "\$@"
	exit 1
	;;
esac
EOF

	# Make the bin/gopls script executable
	chmod +x bin/gopls
	;;
"env")
	echo "."
	;;
"mod")
	exit 0
	;;
"run")
	echo -n "bar" > extension/src/goToolsInformation.ts
	exit 0
	;;
*)
	echo unexpected command $@
	exit 1
	;;
esac`, tc.wantVersion)

			var gotSubject string // subject of the announcement email that was sent

			tasks := &ReleaseGoplsTasks{
				Gerrit:     gerrit,
				CloudBuild: NewFakeCloudBuild(t, gerrit, "", nil, FakeBinary{Name: "go", Implementation: fakeGo}),
				Github: &FakeGitHub{
					Milestones: map[int]string{
						1: fmt.Sprintf("gopls/%s", tc.release.String()),
					},
				},
				SendMail: func(h MailHeader, c MailContent) error {
					gotSubject = c.Subject
					return nil
				},
				ApproveAction: func(tc *workflow.TaskContext) error { return nil },
			}

			wd := tasks.NewPrereleaseDefinition()
			w, err := workflow.Start(wd, input)
			if err != nil {
				t.Fatal(err)
			}

			outputs, err := w.Run(ctx, &verboseListener{t: t})
			if err != nil {
				t.Fatal(err)
			}

			// Verify that workflow will create the release branch for minor releases.
			// The release branch is created before the flow run for patch releases.
			afterHead, err := gerrit.ReadBranchHead(ctx, "tools", goplsReleaseBranchName(tc.release))
			if err != nil {
				t.Error(err)
			}

			// Verify that workflow return the expected pre-release version.
			if got := outputs["version"]; got != tc.wantVersion {
				t.Errorf("Output: got \"version\" %q, want %q", got, tc.wantVersion)
			}

			// Verify the content of following files are expected.
			contentChecks := []struct {
				repo   string
				branch string
				path   string
				want   string
			}{
				{
					repo:   "tools",
					branch: goplsReleaseBranchName(tc.release),
					path:   "codereview.cfg",
					want:   tc.wantConfig,
				},
				{
					repo:   "tools",
					branch: goplsReleaseBranchName(tc.release),
					path:   "gopls/go.sum",
					want:   "test go sum",
				},
				{
					repo:   "tools",
					branch: goplsReleaseBranchName(tc.release),
					path:   "gopls/go.mod",
					want:   "test go mod",
				},
				{
					repo:   "vscode-go",
					branch: "master",
					path:   "extension/src/goToolsInformation.ts",
					want:   "bar",
				},
				{
					repo:   "vscode-go",
					branch: "release-v0.44",
					path:   "extension/src/goToolsInformation.ts",
					want:   "foo",
				},
			}
			for _, check := range contentChecks {
				commit, err := gerrit.ReadBranchHead(ctx, check.repo, check.branch)
				if err != nil {
					t.Fatal(err)
				}
				got, err := gerrit.ReadFile(ctx, check.repo, commit, check.path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != check.want {
					t.Errorf("Content of %q = %q, want %q", check.path, got, check.want)
				}
			}

			// Verify the commits merged to release branch after the flow execution.
			beforeIndex, afterIndex := 0, 0
			for i, commit := range tools.History() {
				if commit == afterHead {
					afterIndex = i
				}
				if commit == beforeHead {
					beforeIndex = i
				}
			}

			if committed := beforeIndex - afterIndex; committed != tc.wantCommits {
				t.Errorf("%v commits merged to release branch after the pre-release flow executed, but want %v commits", committed, tc.wantCommits)
			}

			// Verify the pre-release tag is created and it's pointing to the head of
			// the release branch.
			info, err := gerrit.GetTag(ctx, "tools", fmt.Sprintf("gopls/%s", tc.wantVersion))
			if err != nil {
				t.Fatal(err)
			}
			if info.Revision != afterHead {
				t.Errorf("the pre-release tag points to commit %s, should point to the head commit of release branch %s", info.Revision, afterHead)
			}
			if wantSubject := "Gopls " + tc.wantVersion + " is released"; gotSubject != wantSubject {
				// The full email content is checked by TestAnnouncementMail.
				t.Errorf("NewPrereleaseDefinition().Run(): got email subject %q, want %q", gotSubject, wantSubject)
			}
		}

		t.Run("manual input version: "+tc.name, func(t *testing.T) {
			runTestWithInput(map[string]any{
				reviewersParam.Name:           []string(nil),
				"explicit version (optional)": tc.release.String(),
				"next version":                "use explicit version",
			})
		})
		versionBump := "next patch"
		if tc.release.Patch == 0 {
			versionBump = "next minor"
		}
		t.Run("interpret version "+versionBump+" : "+tc.name, func(t *testing.T) {
			runTestWithInput(map[string]any{
				reviewersParam.Name:           []string(nil),
				"explicit version (optional)": "",
				"next version":                versionBump,
			})
		})
	}
}

func TestTagRelease(t *testing.T) {
	ctx := context.Background()
	testcases := []struct {
		name       string
		tags       []string
		release    releaseVersion
		prerelease string
		wantErr    bool
	}{
		{
			name: "should add the release tag v0.1.0 to the commit with tag v0.1.0-pre.2",
			tags: []string{
				"gopls/v0.1.0-pre.1",
				"gopls/v0.1.0-pre.2",
			},
			release:    releaseVersion{Major: 0, Minor: 1, Patch: 0},
			prerelease: "pre.2",
			wantErr:    false,
		},
		{
			name: "should add the release tag v0.12.0 to the commit with tag v0.12.0-pre.1",
			tags: []string{
				"gopls/v0.12.0-pre.1",
				"gopls/v0.12.0-pre.2",
			},
			release:    releaseVersion{Major: 0, Minor: 12, Patch: 0},
			prerelease: "pre.1",
			wantErr:    false,
		},
		{
			name: "should error if the pre-release tag does not exist",
			tags: []string{
				"gopls/v0.12.0-pre.1",
				"gopls/v0.12.0-pre.2",
			},
			release:    releaseVersion{Major: 0, Minor: 12, Patch: 0},
			prerelease: "pre.3",
			wantErr:    true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			_ = tools.Commit(map[string]string{
				"go.mod": "module golang.org/x/tools\n",
				"go.sum": "\n",
			})

			for i, tag := range tc.tags {
				commit := tools.Commit(map[string]string{
					"README.md": fmt.Sprintf("THIS IS READ ME FOR %v.", i),
				})
				tools.Tag(tag, commit)
			}

			tasks := &ReleaseGoplsTasks{
				Gerrit: NewFakeGerrit(t, tools),
			}

			err := tasks.tagRelease(&workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}, tc.release, tc.prerelease)

			if tc.wantErr && err == nil {
				t.Errorf("tagRelease(%q) should return error but return nil", tc.release)
			} else if !tc.wantErr && err != nil {
				t.Errorf("tagRelease(%q) should return nil but return err: %v", tc.release, err)
			}

			if !tc.wantErr {
				releaseTag := fmt.Sprintf("gopls/%s", tc.release)
				release, err := tasks.Gerrit.GetTag(ctx, "tools", releaseTag)
				if err != nil {
					t.Errorf("release tag %q should be added after tagRelease(%q): %v", releaseTag, tc.release, err)
				}

				prereleaseTag := fmt.Sprintf("gopls/%s", tc.release)
				prerelease, err := tasks.Gerrit.GetTag(ctx, "tools", prereleaseTag)
				if err != nil {
					t.Fatalf("failed to get tag %q: %v", prereleaseTag, err)
				}

				// verify the release tag and the input pre-release tag point to the same
				// commit.
				if release.Revision != prerelease.Revision {
					t.Errorf("tagRelease(%s) add the release tag to commit %s, but should add to commit %s", tc.release, prerelease.Revision, release.Revision)
				}
			}
		})
	}
}

func TestExecuteAndMonitorChange(t *testing.T) {
	mustHaveShell(t)

	testcases := []struct {
		name   string
		branch string
		script string
		watch  []string
		want   map[string]string
	}{
		{
			name:   "write all three files with different content",
			branch: "master",
			script: `echo -n "foo" > file_a
echo -n "foo" > file_b
echo -n "foo" > file_c
`,
			watch: []string{"file_a", "file_b", "file_c"},
			want:  map[string]string{"file_a": "foo", "file_b": "foo", "file_c": "foo"},
		},
		{
			name:   "ignore file_c changes",
			branch: "master",
			script: `echo -n "foo" > file_a
echo -n "foo" > file_b
echo -n "foo" > file_c
`,
			watch: []string{"file_a", "file_b"},
			want:  map[string]string{"file_a": "foo", "file_b": "foo"},
		},
		{
			name:   "write two files with different content",
			branch: "master",
			script: `echo -n "foo" > file_a
echo -n "foo" > file_b
`,
			watch: []string{"file_a", "file_b", "file_c"},
			want:  map[string]string{"file_a": "foo", "file_b": "foo"},
		},
		{
			name:   "write one file with different content in foo branch",
			branch: "foo",
			script: `echo -n "foo" > file_a`,
			watch:  []string{"file_a", "file_b", "file_c"},
			want:   map[string]string{"file_a": "foo"},
		},
		{
			name:   "create a file in foo branch",
			branch: "foo",
			script: `echo -n "foo" > file_d`,
			watch:  []string{"file_a", "file_b", "file_c", "file_d"},
			want:   map[string]string{"file_d": "foo"},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			initial := tools.Commit(map[string]string{
				"gopls/go.mod": "module golang.org/x/tools\n",
				"gopls/go.sum": "\n",
				"file_a":       "file_a",
				"file_b":       "file_b",
				"file_c":       "file_c",
			})
			if tc.branch != "master" {
				tools.Branch(tc.branch, initial)
			}

			cloudBuild := NewFakeCloudBuild(t, NewFakeGerrit(t, tools), "", nil)
			ctx := &workflow.TaskContext{
				Context: context.Background(),
				Logger:  &testLogger{t, ""},
			}
			got, err := executeAndMonitorChange(ctx, cloudBuild, "tools", tc.branch, tc.script, tc.watch)
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("executeAndMonitorChange() = %v want = %v", got, tc.want)
			}
		})
	}
}

func TestGoplsReleaseFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping an end-to-end workflow test in short mode")
	}
	mustHaveShell(t)

	testcases := []struct {
		name string
		// The fields below are the prepared states before running the gopls
		// release flow.
		// commitTags specifies a sequence of (possibly) tagged commits.
		// For each entry, a new commit is created, and if the entry is
		// non empty that commit is tagged with the entry value.
		commitTags []string
		// goplsGoMod specifies the content of gopls/go.mod in x/tools' master
		// branch before the flow execution.
		goplsGoMod string
		release    releaseVersion

		// The fields below are the desired states.
		// wantPrereleaseTag is the expected prerelease tag in x/tools repo
		// associated with the final release tag.
		wantPrereleaseTag string
		// wantCommit controls whether a commit should be made to a particular
		// branch in a specified repository.
		wantCommit map[string]map[string]bool
		// wantGoplsGoMod specifies the desired content of gopls/go.mod in x/tools'
		// master branch after the flow execution.
		wantGoplsGoMod string
	}{
		{
			name:              "release patch v0.16.3-pre.3, update vscode-go release and master branch",
			commitTags:        []string{"gopls/v0.16.2", "gopls/v0.16.3-pre.1", "gopls/v0.16.3-pre.2", "gopls/v0.16.3-pre.3"},
			goplsGoMod:        "foo",
			release:           releaseVersion{Major: 0, Minor: 16, Patch: 3},
			wantPrereleaseTag: "gopls/v0.16.3-pre.3",
			wantCommit: map[string]map[string]bool{
				"tools": {
					"master": false, "gopls-release-branch.0.16": false,
				},
				"vscode-go": {
					"master": true, "release-v0.44": true,
				},
			},
			wantGoplsGoMod: "foo",
		},
		{
			name:              "release minor v0.17.0-pre.2, update vscode-go release and master branch, update tools gopls go.mod",
			commitTags:        []string{"gopls/v0.16.0", "gopls/v0.17.0-pre.1", "gopls/v0.17.0-pre.2"},
			goplsGoMod:        "foo",
			release:           releaseVersion{Major: 0, Minor: 17, Patch: 0},
			wantPrereleaseTag: "gopls/v0.17.0-pre.2",
			wantCommit: map[string]map[string]bool{
				"tools": {
					"master": true, "gopls-release-branch.0.17": false,
				},
				"vscode-go": {
					"master": true, "release-v0.44": true,
				},
			},
			wantGoplsGoMod: "bar",
		},
		{
			name:              "release minor v0.17.0-pre.2, update vscode-go release and master branch, skip update tools gopls go.mod",
			commitTags:        []string{"gopls/v0.16.0", "gopls/v0.17.0-pre.1", "gopls/v0.17.0-pre.2"},
			goplsGoMod:        "bar",
			release:           releaseVersion{Major: 0, Minor: 17, Patch: 0},
			wantPrereleaseTag: "gopls/v0.17.0-pre.2",
			wantCommit: map[string]map[string]bool{
				"tools": {
					"master": false, "gopls-release-branch.0.17": false,
				},
				"vscode-go": {
					"master": true, "release-v0.44": true,
				},
			},
			wantGoplsGoMod: "bar",
		},
	}

	for _, tc := range testcases {
		runTestWithInput := func(input map[string]any) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			releaseBranch := goplsReleaseBranchName(tc.release)

			vscodego := NewFakeRepo(t, "vscode-go")
			initial := vscodego.Commit(map[string]string{
				"extension/src/goToolsInformation.ts": "foo", // arbitrary initial contents, to be mutated by the fakeGo script below
			})
			vscodego.Branch("release-v0.44", initial)

			tools := NewFakeRepo(t, "tools")
			initial = tools.Commit(map[string]string{
				"gopls/go.mod": tc.goplsGoMod,
				"gopls/go.sum": "\n",
			})
			tools.Branch(releaseBranch, initial)
			for i, tag := range tc.commitTags {
				commit := tools.CommitOnBranch(releaseBranch, map[string]string{
					"README.md": fmt.Sprintf("THIS IS READ ME FOR %v.", i),
				})
				if tag != "" {
					tools.Tag(tag, commit)
				}
			}

			gerrit := NewFakeGerrit(t, tools, vscodego)

			// Var before records the initial state of the branch head prior to
			// executing the release flow.
			before := map[string]map[string]string{}
			for repo := range tc.wantCommit {
				if _, ok := before[repo]; !ok {
					before[repo] = map[string]string{}
				}
				for branch := range tc.wantCommit[repo] {
					commit, err := gerrit.ReadBranchHead(ctx, repo, branch)
					if err != nil {
						t.Fatal(err)
					}
					before[repo][branch] = commit
				}
			}

			// fakeGo handles multiple arguments in gopls release flow:
			// - go get will write "bar" content to gopls/go.mod in x/tools master
			// branch to simulate the dependency upgrade.
			// - go mod will exit without error.
			// - go run will write "bar" content to file in vscode-go project
			// containing gopls versions.
			var fakeGo = fmt.Sprintf(`#!/bin/bash -exu

case "$1" in
"get")
	echo -n "bar" > go.mod
	exit 0
	;;
"mod")
	exit 0
	;;
"run")
	# Only update the goToolsInformation.ts if runs generate.go.
	for param in "$@:2"; do
			if [[ $param == *"generate.go"* ]]; then
				echo -n "bar" > extension/src/goToolsInformation.ts
			fi
	done
	exit 0
	;;
*)
	echo unexpected command $@
	exit 1
	;;
esac
`)

			var gotSubject string // subject of the announcement email that was sent

			tasks := &ReleaseGoplsTasks{
				Gerrit:     gerrit,
				CloudBuild: NewFakeCloudBuild(t, gerrit, "", nil, FakeBinary{Name: "go", Implementation: fakeGo}),
				Github: &FakeGitHub{
					Milestones: map[int]string{
						1: fmt.Sprintf("gopls/%s", tc.release),
					},
					Issues: map[int]*github.Issue{
						1: {
							Number:    github.Int(1),
							Title:     github.String(fmt.Sprintf("x/tools/gopls: release version %s", tc.release)),
							Milestone: &github.Milestone{ID: github.Int64(1)},
						},
					},
				},
				SendMail: func(h MailHeader, c MailContent) error {
					gotSubject = c.Subject
					return nil
				},
				ApproveAction: func(tc *workflow.TaskContext) error { return nil },
			}

			wd := tasks.NewReleaseDefinition()
			w, err := workflow.Start(wd, input)
			if err != nil {
				t.Fatal(err)
			}

			_, err = w.Run(ctx, &verboseListener{t: t})
			if err != nil {
				t.Fatal(err)
			}

			// Verify that the expected commits were made to each repository's branches.
			// Ensure no unexpected commits were merged.
			for repo := range tc.wantCommit {
				for branch, wantCommit := range tc.wantCommit[repo] {
					afterHead, err := gerrit.ReadBranchHead(ctx, repo, branch)
					if err != nil {
						t.Fatal(err)
					}
					beforeHead := before[repo][branch]

					// The branch head commit should not change after the process runs.
					if !wantCommit {
						if afterHead != beforeHead {
							t.Errorf("repo %s branch %s should not have any commit, before head %s, after head %s", repo, branch, beforeHead, afterHead)
						}
						continue
					}

					// The branch head should advance to the next commit.
					var gitHistory []string
					switch repo {
					case "tools":
						gitHistory = tools.History()
					case "vscode-go":
						gitHistory = vscodego.History()
					default:
						t.Fatal(fmt.Errorf("unexpected repo name %q", repo))
					}
					var beforeIndex, afterIndex int
					for i, commit := range gitHistory {
						if commit == beforeHead {
							beforeIndex = i
						}
						if commit == afterHead {
							afterIndex = i
						}
					}

					if beforeIndex-afterIndex != 1 {
						t.Errorf("the repo %s branch %s should have exactly one commit merged, before head %s, after head %s, history %v", repo, branch, beforeHead, afterHead, gitHistory)
					}
				}
			}

			// Verify the release tag exists and matches the expected prerelease tag.
			wantCommit, err := gerrit.GetTag(ctx, "tools", tc.wantPrereleaseTag)
			if err != nil {
				t.Fatalf("can not get the commit for tag %s", tc.wantPrereleaseTag)
			}
			gotCommit, err := gerrit.GetTag(ctx, "tools", fmt.Sprintf("gopls/%s", tc.release))
			if err != nil {
				t.Errorf("can not get the commit for tag %s", fmt.Sprintf("gopls/%s", tc.release))
			}
			if wantCommit.Revision != gotCommit.Revision {
				t.Errorf("the flow create release tag upon commit %s, but should tag on commit %s which have tag %s", gotCommit.Revision, wantCommit.Revision, tc.wantPrereleaseTag)
			}

			// Verify the content of following files are expected.
			contentChecks := []struct {
				repo   string
				branch string
				path   string
				want   string
			}{
				{
					repo:   "tools",
					branch: "master",
					path:   "gopls/go.mod",
					want:   tc.wantGoplsGoMod,
				},
				{
					repo:   "vscode-go",
					branch: "master",
					path:   "extension/src/goToolsInformation.ts",
					want:   "bar",
				},
				{
					repo:   "vscode-go",
					branch: "release-v0.44",
					path:   "extension/src/goToolsInformation.ts",
					want:   "bar",
				},
			}
			for _, check := range contentChecks {
				commit, err := gerrit.ReadBranchHead(ctx, check.repo, check.branch)
				if err != nil {
					t.Fatal(err)
				}
				got, err := gerrit.ReadFile(ctx, check.repo, commit, check.path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != check.want {
					t.Errorf("Content of %q = %q, want %q", check.path, got, check.want)
				}
			}

			if wantSubject := fmt.Sprintf("Gopls %s is released", tc.release); gotSubject != wantSubject {
				// The full email content is checked by TestAnnouncementMail.
				t.Errorf("NewReleaseDefinition().Run(): got email subject %q, want %q", gotSubject, wantSubject)
			}
		}
		t.Run("manual input version: "+tc.name, func(t *testing.T) {
			runTestWithInput(map[string]any{
				reviewersParam.Name:           []string(nil),
				"explicit version (optional)": tc.release.String(),
				"next version":                "use explicit version",
			})
		})
		versionBump := "next patch"
		if tc.release.Patch == 0 {
			versionBump = "next minor"
		}
		t.Run("interpret version "+versionBump+": "+tc.name, func(t *testing.T) {
			runTestWithInput(map[string]any{
				reviewersParam.Name:           []string(nil),
				"explicit version (optional)": "",
				"next version":                versionBump,
			})
		})
	}
}
