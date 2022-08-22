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

func TestCycles(t *testing.T) {
	tests := []struct {
		repos []TagRepo
		want  []string
	}{
		{
			repos: []TagRepo{
				{Name: "text", Deps: []string{"tools"}},
				{Name: "tools", Deps: []string{"text"}},
				{Name: "sys"},
				{Name: "net", Deps: []string{"sys"}},
			},
			want: []string{
				"tools,text,tools",
				"text,tools,text",
			},
		},
		{
			repos: []TagRepo{
				{Name: "text", Deps: []string{"tools"}},
				{Name: "tools", Deps: []string{"fake"}},
				{Name: "fake", Deps: []string{"text"}},
			},
			want: []string{
				"tools,fake,text,tools",
				"text,tools,fake,text",
				"fake,text,tools,fake",
			},
		},
		{
			repos: []TagRepo{
				{Name: "text", Deps: []string{"tools"}},
				{Name: "tools", Deps: []string{"fake", "text"}},
				{Name: "fake", Deps: []string{"tools"}},
			},
			want: []string{
				"tools,text,tools",
				"text,tools,text",
				"tools,fake,tools",
				"fake,tools,fake",
			},
		},
		{
			repos: []TagRepo{
				{Name: "text", Deps: []string{"tools"}},
				{Name: "tools", Deps: []string{"fake", "text"}},
				{Name: "fake1", Deps: []string{"fake2"}},
				{Name: "fake2", Deps: []string{"tools"}},
			},
			want: []string{
				"tools,text,tools",
				"text,tools,text",
			},
		},
	}

	for _, tt := range tests {
		var repos []TagRepo
		for _, r := range tt.repos {
			repos = append(repos, TagRepo{
				Name:    r.Name,
				ModPath: r.Name,
				Deps:    r.Deps,
			})
		}
		cycles := checkCycles(repos)
		got := map[string]bool{}
		for _, cycle := range cycles {
			got[strings.Join(cycle, ",")] = true
		}
		want := map[string]bool{}
		for _, cycle := range tt.want {
			want[cycle] = true
		}

		if diff := cmp.Diff(got, want); diff != "" {
			t.Errorf("%v result unexpected: %v", tt.repos, diff)
		}
	}
}
