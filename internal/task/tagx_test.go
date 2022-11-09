package task

import (
	"context"
	"encoding/json"
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
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/build/types"
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

var flagRunIsGreenLiveTest = flag.String("run-is-green-test", "", "run greenness test for repo@rev")

func TestIsGreenLive(t *testing.T) {
	if *flagRunIsGreenLiveTest == "" {
		t.Skip("no module/rev specified")
	}

	repo, rev, ok := strings.Cut(*flagRunIsGreenLiveTest, "@")
	if !ok {
		t.Fatalf("--run-is-green-test must be module@rev: %q", *flagRunIsGreenLiveTest)
	}

	tasks := &TagXReposTasks{
		Gerrit: &RealGerritClient{
			Client: gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth),
		},
		DashboardURL: "https://build.golang.org",
	}
	ctx := &workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}
	greenCommit, ok, err := tasks.findGreen(ctx, TagRepo{Name: repo, ModPath: "golang.org/x/" + repo}, rev, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("module %v green: %v at rev %v", repo, ok, greenCommit)
}

func TestIsGreen(t *testing.T) {
	type revLine struct {
		goBranch        string
		goRev, toolsRev int
		pass            bool
	}
	tests := []struct {
		name         string
		rev          string
		lines        []revLine
		wantGreenRev string
	}{
		{
			name: "simple OK",
			rev:  "tools-1",
			lines: []revLine{
				{"master", 1, 1, true},
				{"release-branch.go1.19", 1, 1, true},
				{"release-branch.go1.18", 1, 1, true},
			},
			wantGreenRev: "tools-1",
		},
		{
			name: "missing release branch runs",
			rev:  "tools-1",
			lines: []revLine{
				{"master", 3, 3, true},
				{"master", 2, 2, true},
				{"release-branch.go1.19", 2, 2, true},
				{"release-branch.go1.18", 2, 2, true},
				{"master", 1, 1, true},
			},
			wantGreenRev: "tools-2",
		},
		{
			name: "succeed despite failures",
			rev:  "tools-1",
			lines: []revLine{
				{"master", 3, 1, false},
				{"master", 2, 1, true},
				{"master", 1, 1, false},
				{"release-branch.go1.19", 1, 1, true},
				{"release-branch.go1.18", 1, 1, true},
			},
			wantGreenRev: "tools-1",
		},
		{
			name: "not green yet",
			rev:  "tools-1",
			lines: []revLine{
				{"master", 3, 1, true},
				{"release-branch.go1.19", 1, 1, false},
				{"release-branch.go1.18", 1, 1, true},
			},
			wantGreenRev: "",
		},
		{
			name: "commit not registered on dashboard",
			rev:  "tools-2",
			lines: []revLine{
				{"master", 1, 1, true},
				{"release-branch.go1.19", 1, 1, true},
				{"release-branch.go1.18", 1, 1, true},
			},
			wantGreenRev: "",
		},
	}
	for _, tt := range tests {

		fakeDash := func(repo string) *types.BuildStatus {
			var builders []string
			for _, b := range dashboard.Builders {
				builders = append(builders, b.Name)
			}

			if repo == "" {
				// For the front page we only read branches.
				return &types.BuildStatus{
					Builders: builders,
					Revisions: []types.BuildRevision{
						{GoBranch: "master"},
						{GoBranch: "release-branch.go1.19"},
						{GoBranch: "release-branch.go1.18"},
					},
				}
			}
			st := &types.BuildStatus{
				Builders: builders,
			}
			for _, line := range tt.lines {
				rev := types.BuildRevision{
					Repo:       repo,
					Revision:   fmt.Sprintf("tools-%v", line.toolsRev),
					GoRevision: fmt.Sprintf("go-%v-%v", line.goBranch, line.goRev),
					Date:       time.Now().Format(time.RFC3339),
					Branch:     "master",
					GoBranch:   line.goBranch,
				}
				for _, b := range builders {
					switch b {
					case "linux-amd64":
						if line.pass {
							rev.Results = append(rev.Results, "ok")
						} else {
							rev.Results = append(rev.Results, "")
						}
					case "illumos-amd64", "plan9-arm":
						rev.Results = append(rev.Results, "fail")
					default:
						rev.Results = append(rev.Results, "ok")
					}
				}
				st.Revisions = append(st.Revisions, rev)
			}
			return st
		}
		dashServer := httptest.NewServer(fakeDashboard(fakeDash))
		t.Cleanup(dashServer.Close)

		commitsInRefs := func(commits, refs []string) map[string][]string {
			result := map[string][]string{}
			for _, commit := range commits {
				for _, ref := range refs {
					if strings.HasPrefix(commit, "go-"+strings.TrimPrefix(ref, "refs/heads/")) {
						result[commit] = append(result[commit], ref)
					}
				}
			}
			return result
		}

		tasks := &TagXReposTasks{
			Gerrit:       &isGreenGerrit{commitsInRefs: commitsInRefs},
			DashboardURL: dashServer.URL,
		}
		t.Run(tt.name, func(t *testing.T) {
			ctx := &workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}
			green, _, err := tasks.findGreen(ctx, TagRepo{
				Name:    "tools",
				ModPath: "golang.org/x/tools",
			}, tt.rev, false)
			if err != nil {
				t.Fatal(err)
			}
			if green != tt.wantGreenRev {
				t.Errorf("tools green at %q, wanted %q", green, tt.wantGreenRev)
			}
		})
	}
}

type isGreenGerrit struct {
	GerritClient
	commitsInRefs func(commits, refs []string) map[string][]string
}

func (g *isGreenGerrit) GetCommitsInRefs(ctx context.Context, project string, commits, refs []string) (map[string][]string, error) {
	return g.commitsInRefs(commits, refs), nil
}

const fakeGo = `#!/bin/bash -exu

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
  echo "tidied! $*" >> go.mod
  ;;
*)
  echo unexpected command $@
  exit 1
  ;;
esac
`

type tagXTestDeps struct {
	ctx       context.Context
	gerrit    *FakeGerrit
	tagXTasks *TagXReposTasks
}

func newTagXTestDeps(t *testing.T, repos ...*FakeRepo) *tagXTestDeps {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	goServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeTarball("dl/go1.19.linux-amd64.tar.gz", map[string]string{
			"go/bin/go": fakeGo,
		}, w, r)
	}))
	t.Cleanup(goServer.Close)

	goRepo := NewFakeRepo(t, "go")
	go1 := goRepo.Commit(map[string]string{
		"main.go": "I'm the go command or something",
	})
	repos = append(repos, goRepo)

	fakeBuildlets := NewFakeBuildlets(t, "", nil)
	fakeGerrit := NewFakeGerrit(t, repos...)
	var builders, allOK []string
	for _, b := range dashboard.Builders {
		builders = append(builders, b.Name)
		allOK = append(allOK, "ok")
	}
	fakeDash := func(repo string) *types.BuildStatus {
		if repo == "" {
			// For the front page we only read branches.
			return &types.BuildStatus{
				Builders: builders,
				Revisions: []types.BuildRevision{
					{GoBranch: "master"},
				},
			}
		}
		for _, r := range repos {
			if repo != "golang.org/x/"+r.name {
				continue
			}
			st := &types.BuildStatus{
				Builders: builders,
			}
			for _, commit := range r.history {
				st.Revisions = append(st.Revisions, types.BuildRevision{
					Repo:       r.name,
					Revision:   commit,
					GoRevision: go1,
					Date:       time.Now().Format(time.RFC3339),
					Branch:     "master",
					GoBranch:   "master",
					Results:    allOK,
				})
			}
			return st
		}
		return nil
	}
	dashServer := httptest.NewServer(fakeDashboard(fakeDash))
	t.Cleanup(dashServer.Close)
	tasks := &TagXReposTasks{
		Gerrit:         fakeGerrit,
		GerritURL:      fakeGerrit.GerritURL(),
		CreateBuildlet: fakeBuildlets.CreateBuildlet,
		LatestGoBinaries: func(context.Context) (string, error) {
			return goServer.URL + "/dl/go1.19.linux-amd64.tar.gz", nil
		},
		DashboardURL: dashServer.URL,
	}
	return &tagXTestDeps{
		ctx:       ctx,
		gerrit:    fakeGerrit,
		tagXTasks: tasks,
	}
}

func TestTagXRepos(t *testing.T) {
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
		"go.mod":       "module golang.org/x/tools\nrequire golang.org/x/mod v1.0.0\ngo 1.18 // tagx:compat 1.16\nrequire golang.org/x/sys v0.1.0\n",
		"go.sum":       "\n",
		"gopls/go.mod": "module golang.org/x/tools/gopls\nrequire golang.org/x/mod v1.0.0\n",
		"gopls/go.sum": "\n",
	})
	tools.Tag("v1.1.5", tools1)

	deps := newTagXTestDeps(t, sys, mod, tools)

	wd := deps.tagXTasks.NewDefinition()
	w, err := workflow.Start(wd, map[string]interface{}{
		reviewersParam.Name: []string(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(deps.ctx, time.Minute)
	defer cancel()
	_, err = w.Run(ctx, &verboseListener{t: t})
	if err != nil {
		t.Fatal(err)
	}

	tag, err := deps.gerrit.GetTag(ctx, "sys", "v0.2.0")
	if err != nil {
		t.Fatalf("sys should have been tagged with v0.2.0: %v", err)
	}
	if tag.Revision != sys2 {
		t.Errorf("sys v0.2.0 = %v, want %v", tag.Revision, sys2)
	}

	tags, err := deps.gerrit.ListTags(ctx, "mod")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"v1.0.0"}) {
		t.Errorf("mod has tags %v, wanted only v1.0.0", tags)
	}

	tag, err = deps.gerrit.GetTag(ctx, "tools", "v1.2.0")
	if err != nil {
		t.Fatalf("tools should have been tagged with v1.2.0: %v", err)
	}
	goMod, err := deps.gerrit.ReadFile(ctx, "tools", tag.Revision, "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "sys@v0.2.0") || !strings.Contains(string(goMod), "mod@v1.0.0") {
		t.Errorf("tools should use sys v0.2.0 and mod v1.0.0. go.mod: %v", string(goMod))
	}
	if !strings.Contains(string(goMod), "tidied!") {
		t.Error("tools go.mod should be tidied")
	}
	goplsMod, err := deps.gerrit.ReadFile(ctx, "tools", tag.Revision, "gopls/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goplsMod), "tidied!") || !strings.Contains(string(goplsMod), "1.16") || strings.Contains(string(goplsMod), "upgraded") {
		t.Error("gopls go.mod should be tidied with -compat 1.16, but not upgraded")
	}
}

func TestTagSingleRepo(t *testing.T) {
	mod := NewFakeRepo(t, "mod")
	mod1 := mod.Commit(map[string]string{
		"go.mod": "module golang.org/x/mod\n",
		"go.sum": "\n",
	})
	mod.Tag("v1.1.0", mod1)
	foo := NewFakeRepo(t, "foo")
	foo1 := foo.Commit(map[string]string{
		"go.mod": "module golang.org/x/foo\nrequire golang.org/x/mod v1.0.0\n",
		"go.sum": "\n",
	})
	foo.Tag("v1.1.5", foo1)
	foo.Commit(map[string]string{
		"main.go": "package main",
	})

	deps := newTagXTestDeps(t, mod, foo)

	wd := deps.tagXTasks.NewSingleDefinition()
	ctx, cancel := context.WithTimeout(deps.ctx, time.Minute)
	w, err := workflow.Start(wd, map[string]interface{}{
		"Repository name":   "foo",
		reviewersParam.Name: []string(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_, err = w.Run(ctx, &verboseListener{t: t})
	if err != nil {
		t.Fatal(err)
	}

	tag, err := deps.gerrit.GetTag(ctx, "foo", "v1.2.0")
	if err != nil {
		t.Fatalf("foo should have been tagged with v1.2.0: %v", err)
	}
	goMod, err := deps.gerrit.ReadFile(ctx, "foo", tag.Revision, "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "mod@v1.1.0") {
		t.Errorf("foo should use mod v1.1.0. go.mod: %v", string(goMod))
	}
}

type verboseListener struct {
	t              *testing.T
	outputListener func(string, interface{})
}

func (l *verboseListener) WorkflowStalled(workflowID uuid.UUID) error {
	l.t.Logf("workflow %q: stalled", workflowID.String())
	return nil
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
	l.t.Logf("%v\ttask %-10v: LOG: %s", time.Now(), l.task, fmt.Sprintf(format, v...))
}

type fakeDashboard func(string) *types.BuildStatus

func (d fakeDashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := d(r.URL.Query().Get("repo"))
	if resp == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(resp)
}
