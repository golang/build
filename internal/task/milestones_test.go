package task

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/go-github/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/oauth2"
)

var (
	flagRun   = flag.Bool("run-destructive-milestones-test", false, "Run the milestone test. Requires repository owner and name flags, and GITHUB_TOKEN set in the environment.")
	flagOwner = flag.String("milestones-github-owner", "", "Owner of testing repository")
	flagRepo  = flag.String("milestones-github-repo", "", "Testing repository")
)

func TestMilestones(t *testing.T) {
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t},
	}

	if !*flagRun {
		t.Skip("Not enabled by flags")
	}
	if *flagOwner == "golang" {
		t.Fatal("This is a destructive test! Don't run it on a real repository.")
	}

	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	httpClient := oauth2.NewClient(ctx, src)
	client3 := github.NewClient(httpClient)
	client4 := githubv4.NewClient(httpClient)

	blocker, err := resetRepo(ctx, client3)
	if err != nil {
		t.Fatal(err)
	}

	tasks := &MilestoneTasks{
		Client: &GitHubClient{
			V3: client3,
			V4: client4,
		},
		RepoOwner: *flagOwner,
		RepoName:  *flagRepo,
	}
	milestones, err := tasks.FetchMilestones(ctx, "go1.20", KindFinal)
	if err != nil {
		t.Fatalf("GetMilestones: %v", err)
	}
	_, err = tasks.CheckBlockers(ctx, milestones, KindFinal)
	if err == nil || !strings.Contains(err.Error(), "open release blockers") {
		t.Fatalf("CheckBlockers with an open release blocker didn't give expected error: %v", err)
	}
	if _, _, err := client3.Issues.Edit(ctx, *flagOwner, *flagRepo, *blocker.Number, &github.IssueRequest{State: github.String("closed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CheckBlockers(ctx, milestones, KindFinal); err != nil {
		t.Fatalf("CheckBlockers with no release blockers failed: %v", err)
	}
}

// resetRepo clears out the test repository and sets it to have:
// - a single milestone, Go1.20
// - a normal issue in that milestone
// - a release blocking issue in that milestone, which is returned.
func resetRepo(ctx context.Context, client *github.Client) (*github.Issue, error) {
	milestones, _, err := client.Issues.ListMilestones(ctx, *flagOwner, *flagRepo, nil)
	if err != nil {
		return nil, err
	}
	for _, m := range milestones {
		if _, err := client.Issues.DeleteMilestone(ctx, *flagOwner, *flagRepo, *m.Number); err != nil {
			return nil, err
		}
	}
	issues, _, err := client.Issues.ListByRepo(ctx, *flagOwner, *flagRepo, nil)
	if err != nil {
		return nil, err
	}
	for _, i := range issues {
		if _, _, err := client.Issues.Edit(ctx, *flagOwner, *flagRepo, *i.Number, &github.IssueRequest{
			State: github.String("CLOSED"),
		}); err != nil {
			return nil, err
		}
	}
	currentMilestone, _, err := client.Issues.CreateMilestone(ctx, *flagOwner, *flagRepo, &github.Milestone{Title: github.String("Go1.20")})
	if err != nil {
		return nil, err
	}
	if _, _, err := client.Issues.Create(ctx, *flagOwner, *flagRepo, &github.IssueRequest{
		Title:     github.String("Non-release-blocker"),
		Milestone: currentMilestone.Number,
	}); err != nil {
		return nil, err
	}
	blocker, _, err := client.Issues.Create(ctx, *flagOwner, *flagRepo, &github.IssueRequest{
		Title:     github.String("Release-blocker"),
		Milestone: currentMilestone.Number,
		Labels:    &[]string{"release-blocker"},
	})
	return blocker, err
}

type testLogger struct {
	t *testing.T
}

func (l *testLogger) Printf(format string, v ...interface{}) {
	l.t.Logf("LOG: %s", fmt.Sprintf(format, v...))
}
