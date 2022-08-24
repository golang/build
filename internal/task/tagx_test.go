package task

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
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
		Logger:  &testLogger{t, ""},
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

const fakeGo = `#!/bin/bash -eu

case "$1" in
"get")
  ls go.mod go.sum >/dev/null
  for i in "${@:2}"; do
    echo -e "// pretend we've upgraded to $i" >> go.mod
    echo "$i h1:asdasd" | tr '@' ' ' >> go.sum
  done
  ;;
"mod")
  ls go.mod go.sum >/dev/null
  echo "tidied!" >> go.mod
  ;;
*)
  echo unexpected command $@
  exit 1
  ;;
esac
`

func TestTagXRepos(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}

	goServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeTarball("dl/go1.19.linux-amd64.tar.gz", map[string]string{
			"go/bin/go": fakeGo,
		}, w, r)
	}))
	t.Cleanup(goServer.Close)

	fakeBuildlets := NewFakeBuildlets(t, "")

	sys := NewFakeRepo(t, "sys")
	sys1 := sys.Commit(map[string]string{
		"go.mod": "module golang.org/x/sys\n",
		"go.sum": "\n",
	})
	sys.Tag("v0.1.0", sys1)
	sys2 := sys.Commit(map[string]string{
		"main.go": "package main",
	})
	mod := NewFakeRepo(t, "mod")
	mod1 := mod.Commit(map[string]string{
		"go.mod": "module golang.org/x/mod\n",
		"go.sum": "\n",
	})
	mod.Tag("v1.0.0", mod1)
	tools := NewFakeRepo(t, "tools")
	tools1 := tools.Commit(map[string]string{
		"go.mod": "module golang.org/x/tools\nrequire golang.org/x/mod v1.0.0\nrequire golang.org/x/sys v0.1.0\n",
		"go.sum": "\n",
	})
	tools.Tag("v1.1.5", tools1)
	fakeGerrit := NewFakeGerrit(t, sys, mod, tools)
	tasks := &TagXReposTasks{
		Gerrit:         fakeGerrit,
		GerritURL:      fakeGerrit.GerritURL(),
		CreateBuildlet: fakeBuildlets.CreateBuildlet,
		LatestGoBinaries: func(context.Context) (string, error) {
			return goServer.URL + "/dl/go1.19.linux-amd64.tar.gz", nil
		},
	}
	wd := tasks.NewDefinition()
	w, err := workflow.Start(wd, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = w.Run(ctx, &verboseListener{t: t})
	if err != nil {
		t.Fatal(err)
	}

	tag, err := fakeGerrit.GetTag(ctx, "sys", "v0.2.0")
	if err != nil {
		t.Fatalf("sys should have been tagged with v0.2.0: %v", err)
	}
	if tag.Revision != sys2 {
		t.Errorf("sys v0.2.0 = %v, want %v", tag.Revision, sys2)
	}

	tags, err := fakeGerrit.ListTags(ctx, "mod")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"v1.0.0"}) {
		t.Errorf("mod has tags %v, wanted only v1.0.0", tags)
	}

	tag, err = fakeGerrit.GetTag(ctx, "tools", "v1.2.0")
	if err != nil {
		t.Fatalf("tools should have been tagged with v1.2.0: %v", err)
	}
	goMod, err := fakeGerrit.ReadFile(ctx, "tools", tag.Revision, "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "sys@v0.2.0") || !strings.Contains(string(goMod), "mod@v1.0.0") {
		t.Errorf("tools should use sys v0.2.0 and mod v1.0.0. go.mod: %v", string(goMod))
	}
	if !strings.Contains(string(goMod), "tidied!") {
		t.Error("tools go.mod should be tidied")
	}
}

type verboseListener struct {
	t              *testing.T
	outputListener func(string, interface{})
}

func (l *verboseListener) TaskStateChanged(_ uuid.UUID, _ string, st *workflow.TaskState) error {
	switch {
	case !st.Finished:
		l.t.Logf("task %-10v: started", st.Name)
	case st.Error != "":
		l.t.Logf("task %-10v: error: %v", st.Name, st.Error)
	default:
		l.t.Logf("task %-10v: done: %v", st.Name, st.Result)
		if l.outputListener != nil {
			l.outputListener(st.Name, st.Result)
		}
	}
	return nil
}

func (l *verboseListener) Logger(_ uuid.UUID, task string) workflow.Logger {
	return &testLogger{t: l.t, task: task}
}

type testLogger struct {
	t    *testing.T
	task string // Optional.
}

func (l *testLogger) Printf(format string, v ...interface{}) {
	l.t.Logf("task %-10v: LOG: %s", l.task, fmt.Sprintf(format, v...))
}
