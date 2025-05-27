// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/go-github/v48/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/oauth2"
)

func TestCheckBlockers(t *testing.T) {
	var errManualApproval = fmt.Errorf("manual approval is required")
	for _, tc := range [...]struct {
		name    string
		issues  map[int]*github.Issue
		version string
		kind    ReleaseKind
		want    error
	}{
		{
			name:    "beta 1 with one hard blocker",
			issues:  map[int]*github.Issue{123: {Labels: []*github.Label{{Name: github.String("release-blocker")}}, Milestone: &github.Milestone{ID: github.Int64(1)}}},
			version: "go1.20beta1", kind: KindBeta,
			want: errManualApproval,
		},
		{
			name:    "beta 1 with one blocker marked okay-after-beta1",
			issues:  map[int]*github.Issue{123: {Labels: []*github.Label{{Name: github.String("release-blocker")}, {Name: github.String("okay-after-beta1")}}, Milestone: &github.Milestone{ID: github.Int64(1)}}},
			version: "go1.20beta1", kind: KindBeta,
			want: nil, // Want no error.
		},
		{
			name:    "beta 2 with one hard blocker and meaningless okay-after-beta1 label",
			issues:  map[int]*github.Issue{123: {Labels: []*github.Label{{Name: github.String("release-blocker")}, {Name: github.String("okay-after-beta1")}}, Milestone: &github.Milestone{ID: github.Int64(1)}}},
			version: "go1.20beta2", kind: KindBeta,
			want: errManualApproval,
		},
		{
			name:    "RC 1 with one hard blocker",
			issues:  map[int]*github.Issue{123: {Labels: []*github.Label{{Name: github.String("release-blocker")}}, Milestone: &github.Milestone{ID: github.Int64(1)}}},
			version: "go1.20rc1", kind: KindRC,
			want: errManualApproval,
		},
		{
			name:    "RC 1 with one blocker marked okay-after-rc1",
			issues:  map[int]*github.Issue{123: {Labels: []*github.Label{{Name: github.String("release-blocker")}, {Name: github.String("okay-after-rc1")}}, Milestone: &github.Milestone{ID: github.Int64(1)}}},
			version: "go1.20rc1", kind: KindRC,
			want: nil, // Want no error.
		},
		{
			name:    "RC 2 with one hard blocker and meaningless okay-after-rc1 label",
			issues:  map[int]*github.Issue{123: {Labels: []*github.Label{{Name: github.String("release-blocker")}, {Name: github.String("okay-after-rc1")}}, Milestone: &github.Milestone{ID: github.Int64(1)}}},
			version: "go1.20rc2", kind: KindRC,
			want: errManualApproval,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &MilestoneTasks{
				Client: &FakeGitHub{
					Milestones:       map[int]string{1: "random-milestone"},
					Issues:           tc.issues,
					DisallowComments: true,
				},
				ApproveAction: func(*workflow.TaskContext) error { return errManualApproval },
			}
			ctx := &workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t: t}}
			got := tasks.CheckBlockers(ctx, ReleaseMilestones{1, 2}, tc.version, tc.kind)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

var flagMilestonesVersion = flag.Int("milestones-relnote-version", 0, "Go 1.N version to use in TestFetchRelnoteMilestoneAndIssue")

func TestFetchRelnoteMilestoneAndIssue(t *testing.T) {
	if *flagMilestonesVersion == 0 {
		t.Skip("Not enabled by flags")
	}

	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	))
	clV3, clV4 := github.NewClient(httpClient), githubv4.NewClient(httpClient)

	tasks := &MilestoneTasks{
		Client:    &GitHubClient{V3: clV3, V4: clV4},
		RepoOwner: "golang", RepoName: "go",
	}

	ctx := &workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t: t}}
	got, err := tasks.FetchRelnoteMilestoneAndIssue(ctx, *flagMilestonesVersion)
	if err != nil {
		t.Fatal("FetchRelnoteMilestoneAndIssue:", err)
	}
	t.Logf("FetchRelnoteMilestoneAndIssue: %#v", got)
}

var (
	flagRunDestructiveMilestonesTest = flag.Bool("run-destructive-milestones-test", false, "Run the milestone test. Requires repository owner and name flags, and GITHUB_TOKEN set in the environment.")
	flagOwner                        = flag.String("milestones-github-owner", "", "Owner of testing repository")
	flagRepo                         = flag.String("milestones-github-repo", "", "Testing repository")
)

func TestMilestones(t *testing.T) {
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}

	if !*flagRunDestructiveMilestonesTest {
		t.Skip("Not enabled by flags")
	}
	if *flagOwner == "golang" {
		t.Fatal("This is a destructive test! Don't run it on a real repository.")
	}

	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	))
	clV3, clV4 := github.NewClient(httpClient), githubv4.NewClient(httpClient)

	normal, blocker, err := resetRepo(ctx, clV3)
	if err != nil {
		t.Fatal(err)
	}

	tasks := &MilestoneTasks{
		Client:    &GitHubClient{V3: clV3, V4: clV4},
		RepoOwner: *flagOwner, RepoName: *flagRepo,
		ApproveAction: func(*workflow.TaskContext) error {
			return fmt.Errorf("not approved")
		},
	}
	milestones, err := tasks.FetchMilestones(ctx, "go1.20", KindMajor)
	if err != nil {
		t.Fatalf("GetMilestones: %v", err)
	}
	if err := tasks.PushIssues(ctx, milestones, "go1.20beta1", KindBeta); err != nil {
		t.Fatalf("Pushing issues for beta release: %v", err)
	}
	pushedBlocker, _, err := clV3.Issues.Get(ctx, *flagOwner, *flagRepo, blocker.GetNumber())
	if err != nil {
		t.Fatal(err)
	}
	if len(pushedBlocker.Labels) != 1 || *pushedBlocker.Labels[0].Name != "release-blocker" {
		t.Errorf("release blocking issue has labels %#v, should only have release-blocker", pushedBlocker.Labels)
	}
	err = tasks.CheckBlockers(ctx, milestones, "go1.20", KindMajor)
	if err == nil || !strings.Contains(err.Error(), "open release blockers") {
		t.Fatalf("CheckBlockers with an open release blocker didn't give expected error: %v", err)
	}
	if _, _, err := clV3.Issues.Edit(ctx, *flagOwner, *flagRepo, *blocker.Number, &github.IssueRequest{State: github.String("closed")}); err != nil {
		t.Fatal(err)
	}
	if err := tasks.CheckBlockers(ctx, milestones, "go1.20", KindMajor); err != nil {
		t.Fatalf("CheckBlockers with no release blockers failed: %v", err)
	}
	if err := tasks.PushIssues(ctx, milestones, "go1.20", KindMajor); err != nil {
		t.Fatalf("PushIssues for major release failed: %v", err)
	}
	milestone, _, err := clV3.Issues.GetMilestone(ctx, *flagOwner, *flagRepo, milestones.Current)
	if err != nil {
		t.Fatal(err)
	}
	if milestone.GetState() != "closed" {
		t.Errorf("current milestone is %q, should be closed", milestone.GetState())
	}
	pushedNormal, _, err := clV3.Issues.Get(ctx, *flagOwner, *flagRepo, normal.GetNumber())
	if err != nil {
		t.Fatal(err)
	}
	if pushedNormal.GetMilestone().GetNumber() != milestones.Next {
		t.Errorf("issue %v is on milestone %v, should have been pushed to %v", normal.GetNumber(), pushedNormal.GetMilestone().GetNumber(), milestones.Next)
	}
}

// resetRepo clears out the test repository and sets it to have:
// - a single milestone, Go1.20
// - a normal issue in that milestone
// - an okay-after-beta1 release blocking issue in that milestone, which is returned.
func resetRepo(ctx context.Context, client *github.Client) (normal, blocker *github.Issue, err error) {
	milestones, _, err := client.Issues.ListMilestones(ctx, *flagOwner, *flagRepo, &github.MilestoneListOptions{State: "all"})
	if err != nil {
		return nil, nil, err
	}
	for _, m := range milestones {
		if _, err := client.Issues.DeleteMilestone(ctx, *flagOwner, *flagRepo, *m.Number); err != nil {
			return nil, nil, err
		}
	}
	issues, _, err := client.Issues.ListByRepo(ctx, *flagOwner, *flagRepo, nil)
	if err != nil {
		return nil, nil, err
	}
	for _, i := range issues {
		if _, _, err := client.Issues.Edit(ctx, *flagOwner, *flagRepo, *i.Number, &github.IssueRequest{
			State: github.String("CLOSED"),
		}); err != nil {
			return nil, nil, err
		}
	}
	currentMilestone, _, err := client.Issues.CreateMilestone(ctx, *flagOwner, *flagRepo, &github.Milestone{Title: github.String("Go1.20")})
	if err != nil {
		return nil, nil, err
	}
	normal, _, err = client.Issues.Create(ctx, *flagOwner, *flagRepo, &github.IssueRequest{
		Title:     github.String("Non-release-blocker"),
		Milestone: currentMilestone.Number,
	})
	if err != nil {
		return nil, nil, err
	}
	blocker, _, err = client.Issues.Create(ctx, *flagOwner, *flagRepo, &github.IssueRequest{
		Title:     github.String("Release-blocker"),
		Milestone: currentMilestone.Number,
		Labels:    &[]string{"release-blocker", "okay-after-beta1"},
	})
	return normal, blocker, err
}
