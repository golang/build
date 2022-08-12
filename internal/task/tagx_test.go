package task

import (
	"context"
	"flag"
	"testing"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

var flagRunTagXTest = flag.Bool("run-tagx-test", false, "run tag x/ repo test, which is read-only and safe. Must have a Gerrit cookie in gitcookies.")

func TestSelectReposLive(t *testing.T) {
	if !*flagRunTagXTest {
		t.Skip("Not enabled by flags")
	}

	tasks := &TagXReposTasks{
		Gerrit: &RealGerritClient{
			Client: gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth()),
		},
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t},
	}
	repos, err := tasks.SelectRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range repos {
		t.Logf("%#v", r)
	}
}
