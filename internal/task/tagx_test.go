// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"flag"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"go.chromium.org/luci/auth"
	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/grpc/prpc"
	"go.chromium.org/luci/hardcoded/chromeinfra"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
	wf "golang.org/x/build/internal/workflow"
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
	deps := func(modPaths ...string) []*TagDep {
		var deps = make([]*TagDep, len(modPaths))
		for i, p := range modPaths {
			deps[i] = &TagDep{p, true}
		}
		return deps
	}
	tests := []struct {
		repos []TagRepo
		want  []string
	}{
		{
			repos: []TagRepo{
				{Name: "text", Deps: deps("tools")},
				{Name: "tools", Deps: deps("text")},
				{Name: "sys"},
				{Name: "net", Deps: deps("sys")},
			},
			want: []string{
				"tools,text,tools",
				"text,tools,text",
			},
		},
		{
			repos: []TagRepo{
				{Name: "text", Deps: deps("tools")},
				{Name: "tools", Deps: deps("fake")},
				{Name: "fake", Deps: deps("text")},
			},
			want: []string{
				"tools,fake,text,tools",
				"text,tools,fake,text",
				"fake,text,tools,fake",
			},
		},
		{
			repos: []TagRepo{
				{Name: "text", Deps: deps("tools")},
				{Name: "tools", Deps: deps("fake", "text")},
				{Name: "fake", Deps: deps("tools")},
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
				{Name: "text", Deps: deps("tools")},
				{Name: "tools", Deps: deps("fake", "text")},
				{Name: "fake1", Deps: deps("fake2")},
				{Name: "fake2", Deps: deps("tools")},
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

var flagRunFindMissingBuildersLiveTest = flag.String("run-find-missing-builders-test", "", "run greenness test for repo@rev")
var flagRunMissingBuilds = flag.Bool("run-missing-builds", false, "run missing builds from missing builders test")

func TestFindMissingBuildersLive(t *testing.T) {
	if !testing.Verbose() || flag.Lookup("test.run").Value.String() != "^TestFindMissingBuildersLive$" {
		t.Skip("not running a live test requiring manual verification if not explicitly requested with go test -v -run=^TestFindMissingBuildersLive$")
	}
	repo, commit, ok := strings.Cut(*flagRunFindMissingBuildersLiveTest, "@")
	if !ok {
		t.Fatalf("-run-find-missing-builders-test flag must be module@rev: %q", *flagRunFindMissingBuildersLiveTest)
	}

	ctx := &workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}
	luciHTTPClient, err := auth.NewAuthenticator(ctx, auth.SilentLogin, chromeinfra.DefaultAuthOptions()).Client()
	if err != nil {
		t.Fatal("auth.NewAuthenticator:", err)
	}
	buildsClient := buildbucketpb.NewBuildsClient(&prpc.Client{
		C:    luciHTTPClient,
		Host: "cr-buildbucket.appspot.com",
	})
	buildersClient := buildbucketpb.NewBuildersClient(&prpc.Client{
		C:    luciHTTPClient,
		Host: "cr-buildbucket.appspot.com",
	})

	tasks := &TagXReposTasks{
		Gerrit: &RealGerritClient{
			Client:  gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth),
			Gitiles: "https://go.googlesource.com",
		},
		BuildBucket: &RealBuildBucketClient{
			BuildsClient:   buildsClient,
			BuildersClient: buildersClient,
		},
	}
	builds, err := tasks.findMissingBuilders(ctx, TagRepo{Name: repo}, commit)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("missing builds for %v at %v: %v", repo, commit, builds)

	if !*flagRunMissingBuilds {
		return
	}

	t.Logf("build error (if any): %v", tasks.runMissingBuilders(ctx, TagRepo{Name: repo}, commit, builds))
}

func TestAwaitGreen(t *testing.T) {
	tests := []struct {
		findBuild, passBuild, pass bool
	}{
		{findBuild: true, pass: true},
		{findBuild: false, passBuild: true, pass: true},
		{findBuild: false, passBuild: false, pass: false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("find_%v_pass_%v", tt.findBuild, tt.passBuild), func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			commit := tools.Commit(map[string]string{
				"gopls.go": "I'm gopls!",
			})
			deps := newTagXTestDeps(t, tools)
			if !tt.findBuild {
				deps.buildbucket.MissingBuilds = []string{
					"x_tools-go1.0-linux-amd64",
				}
			}
			if !tt.passBuild {
				deps.buildbucket.FailBuilds = []string{"x_tools-go1.0-linux-amd64"}
			}

			res, err := deps.tagXTasks.AwaitGreen(deps.ctx, TagRepo{Name: "tools"}, commit)
			t.Logf("commit, err = %v, %v", res, err)
			if (err == nil) != tt.pass {
				t.Fatalf("success = %v (err %v), wanted %v", err == nil, err, tt.pass)
			}
			if tt.pass && res != commit {
				t.Fatalf("green commit = %v, want %v", res, commit)
			}
		})
	}
}

type tagXTestDeps struct {
	ctx         *wf.TaskContext
	gerrit      *FakeGerrit
	buildbucket *FakeBuildBucketClient
	tagXTasks   *TagXReposTasks
}

// mustHaveShell skips if the current environment doesn't support shell
// scripting (/bin/bash).
func mustHaveShell(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}
}

func newTagXTestDeps(t *testing.T, repos ...*FakeRepo) *tagXTestDeps {
	const fakeGo = `#!/bin/bash -exu

case "$1" in
"get")
	ls go.mod go.sum >/dev/null
	for i in "${@:2}"; do
		if [ "$i" = "toolchain@none" ]; then
			echo "// pretend we've dropped toolchain directive" >> go.mod
		else
			echo "// pretend we've upgraded to $i" >> go.mod
			echo "$i h1:asdasd" | tr '@' ' ' >> go.sum
		fi
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

	mustHaveShell(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fakeGerrit := NewFakeGerrit(t, repos...)
	var projects []string
	for _, r := range repos {
		projects = append(projects, r.name)
	}
	fakeBuildBucket := NewFakeBuildBucketClient(0, fakeGerrit.GerritURL(), "ci", projects)
	tasks := &TagXReposTasks{
		Gerrit:      fakeGerrit,
		CloudBuild:  NewFakeCloudBuild(t, fakeGerrit, "project", nil, FakeBinary{Name: "go", Implementation: fakeGo}),
		BuildBucket: fakeBuildBucket,
	}
	return &tagXTestDeps{
		ctx:         &wf.TaskContext{Context: ctx, Logger: &testLogger{t: t}},
		gerrit:      fakeGerrit,
		buildbucket: fakeBuildBucket,
		tagXTasks:   tasks,
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
		"go.mod":       "module golang.org/x/tools\nrequire golang.org/x/mod v1.0.0\ngo 1.18\nrequire golang.org/x/sys v0.1.0\nrequire golang.org/x/build v0.0.0\n",
		"go.sum":       "\n",
		"gopls/go.mod": "module golang.org/x/tools/gopls\nrequire golang.org/x/mod v1.0.0\n",
		"gopls/go.sum": "\n",
	})
	tools.Tag("v1.1.5", tools1)
	build := NewFakeRepo(t, "build")
	build.Commit(map[string]string{
		"go.mod": "module golang.org/x/build\ngo 1.18\nrequire golang.org/x/tools v1.0.0\nrequire golang.org/x/sys v0.1.0\n",
		"go.sum": "\n",
	})

	deps := newTagXTestDeps(t, sys, mod, tools, build)

	wd := deps.tagXTasks.NewDefinition()
	w, err := workflow.Start(wd, map[string]interface{}{
		reviewersParam.Name: []string(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := deps.ctx
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
	if !strings.Contains(string(goplsMod), "tidied!") || strings.Contains(string(goplsMod), "upgraded") {
		t.Errorf("gopls go.mod should be tidied and not upgraded:\n%s", goplsMod)
	}

	tags, err = deps.gerrit.ListTags(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Errorf("build has tags %q, should not have been tagged", tags)
	}
	goMod, err = deps.gerrit.ReadFile(ctx, "build", "master", "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "tools@v1.2.0") || !strings.Contains(string(goMod), "sys@v0.2.0") {
		t.Errorf("build should use tools v1.2.0 and sys v0.2.0. go.mod: %v", string(goMod))
	}
	if !strings.Contains(string(goMod), "tidied!") {
		t.Error("build go.mod should be tidied")
	}
}

func testTagSingleRepo(t *testing.T, skipPostSubmit bool) {
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
	deps.buildbucket.MissingBuilds = []string{"x_foo-gotip-linux-amd64"}

	args := map[string]interface{}{
		"Repository name":   "foo",
		reviewersParam.Name: []string(nil),
	}
	if skipPostSubmit {
		deps.buildbucket.FailBuilds = []string{"x_foo-gotip-linux-amd64"}
		args["Skip post submit result (optional)"] = true
	} else {
		args["Skip post submit result (optional)"] = false
	}

	wd := deps.tagXTasks.NewSingleDefinition()
	w, err := workflow.Start(wd, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx := deps.ctx
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

func TestTagSingleRepo(t *testing.T) {
	t.Run("with post-submit check", func(t *testing.T) { testTagSingleRepo(t, false) })
	// If skipPostSubmit is false, AwaitGreen should sit an spin for a minute before failing
	t.Run("without post-submit check", func(t *testing.T) { testTagSingleRepo(t, true) })
}

type verboseListener struct {
	t              *testing.T
	outputListener func(string, interface{})
	onStall        func()
}

func (l *verboseListener) WorkflowStalled(workflowID uuid.UUID) error {
	l.t.Logf("workflow %q: stalled", workflowID.String())
	if l.onStall != nil {
		l.onStall()
	}
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
