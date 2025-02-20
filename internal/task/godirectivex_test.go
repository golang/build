// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task_test

import (
	"context"
	"flag"
	"slices"
	"testing"

	"golang.org/x/build/internal/task"
	wf "golang.org/x/build/internal/workflow"
)

func TestSelectGolangOrgXReposLive(t *testing.T) {
	if !testing.Verbose() || flag.Lookup("test.run").Value.String() != "^TestSelectGolangOrgXReposLive$" {
		t.Skip("not running a live test requiring manual verification if not explicitly requested with go test -v -run=^TestSelectGolangOrgXReposLive$")
	}

	tasks := task.GoDirectiveXReposTasks{}
	ctx := &wf.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}
	repos, err := tasks.SelectRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(repos)
	for _, r := range repos {
		t.Logf("%#v", "golang.org/x/"+r)
	}
}
