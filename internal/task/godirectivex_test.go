// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"flag"
	"slices"
	"testing"

	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/internal/workflowtest"
	repospkg "golang.org/x/build/repos"
)

func TestSelectGoDirectiveReposLive(t *testing.T) {
	if !testing.Verbose() || flag.Lookup("test.run").Value.String() != "^TestSelectGoDirectiveReposLive$" {
		t.Skip("not running a live test requiring manual verification if not explicitly requested with go test -v -run=^TestSelectGoDirectiveReposLive$")
	}

	tasks := GoDirectiveXReposTasks{}
	ctx := &wf.TaskContext{
		Context: context.Background(),
		Logger:  &workflowtest.Logger{T: t},
	}
	repos, err := tasks.SelectRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(repos)
	for _, r := range repos {
		if repoInfo, ok := repospkg.ByGerritProject[r]; ok {
			t.Logf("%#v", repoInfo.ImportPath)
		} else {
			t.Errorf("repo not found %#v", r)
		}
	}
}
