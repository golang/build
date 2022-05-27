package task

import (
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

var flagRunVersionTest = flag.Bool("run-version-test", false, "run version test, which will submit CLs to go.googlesource.com/scratch. Must have a Gerrit cookie in gitcookies.")

func TestGetNextVersionsLive(t *testing.T) {
	if !*flagRunVersionTest {
		t.Skip("Not enabled by flags")
	}

	cl := gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth())
	tasks := &VersionTasks{
		Gerrit:  &realGerritClient{client: cl},
		Project: "go",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t},
	}

	versions, err := tasks.GetNextVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// It's hard to check correctness automatically.
	t.Errorf("manually verify results: %#v", versions)
}

func TestGetNextVersions(t *testing.T) {
	tasks := &VersionTasks{
		Gerrit: &versionsClient{
			tags: []string{
				"go1.1", "go1.2",
				"go1.3beta1", "go1.3beta2", "go1.3rc1", "go1.3", "go1.3.1", "go1.3.2", "go1.3.3",
				"go1.4beta1", "go1.4beta2", "go1.4rc1", "go1.4", "go1.4.1",
				"go1.5beta1", "go1.5rc1",
			},
		},
		Project: "go",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t},
	}
	versions, err := tasks.GetNextVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := NextVersions{
		CurrentMinor:  "go1.4.2",
		PreviousMinor: "go1.3.4",
		Beta:          "go1.5beta2",
		RC:            "go1.5rc2",
		Major:         "go1.5",
	}
	if diff := cmp.Diff(versions, want); diff != "" {
		t.Fatalf("GetNextVersions mismatch (-want +got):\n%s", diff)
	}
}

type versionsClient struct {
	tags []string
	GerritClient
}

func (c *versionsClient) ListTags(ctx context.Context, project string) ([]string, error) {
	return c.tags, nil
}

func TestVersion(t *testing.T) {
	if !*flagRunVersionTest {
		t.Skip("Not enabled by flags")
	}
	cl := gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth())
	tasks := &VersionTasks{
		Gerrit:  &realGerritClient{client: cl},
		Project: "scratch",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t},
	}

	changeID, err := tasks.CreateAutoSubmitVersionCL(ctx, "master", "version string")
	if err != nil {
		t.Fatal(err)
	}
	_, err = tasks.AwaitCL(ctx, changeID)
	if strings.Contains(err.Error(), "trybots failed") {
		t.Logf("Trybots failed, as they usually do: %v. Abandoning CL and ending test.", err)
		if err := cl.AbandonChange(ctx, changeID, "test is done"); err != nil {
			t.Fatal(err)
		}
		return
	}

	changeID, err = tasks.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "scratch",
		Branch:  "master",
		Subject: "Clean up VERSION",
	}, map[string]string{"VERSION": ""})
	if err != nil {
		t.Fatalf("cleaning up VERSION: %v", err)
	}
	if _, err := tasks.AwaitCL(ctx, changeID); err != nil {
		t.Fatalf("cleaning up VERSION: %v", err)
	}
}
