package task

import (
	"context"
	"flag"
	"strings"
	"testing"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

var flagRunVersionTest = flag.Bool("run-version-test", false, "run version test, which will submit CLs to go.googlesource.com/scratch. Must have a Gerrit cookie in gitcookies.")

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
