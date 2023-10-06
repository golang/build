// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"testing"
	"time"

	"golang.org/x/build/internal/workflow"
)

func TestUpdateProxyTestRepo(t *testing.T) {
	fakeRepo := NewFakeRepo(t, "fake")
	fakeGerrit := NewFakeGerrit(t, fakeRepo)
	fakeRepo.CommitOnBranch("master", map[string]string{
		"go.mod": "module test\n\ngo 1.18\n",
	})
	fakeRepo.Tag("v1.0.0", "master")
	// We need to do this so we can push to the branch we checked out.
	fakeRepo.runGit("config", "receive.denyCurrentBranch", "updateInstead")

	upgradeGoVersion := &UpdateProxyTestRepoTasks{
		Git:       &Git{},
		GerritURL: fakeRepo.dir.dir,
		Branch:    "master",
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := upgradeGoVersion.UpdateProxyTestRepo(&workflow.TaskContext{Context: ctx}, Published{Version: "go1.21.2"}); err != nil {
		t.Fatal(err)
	}

	tags, err := fakeGerrit.ListTags(ctx, fakeRepo.name)
	if err != nil {
		t.Fatalf("unable to list tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v1.0.0" {
		t.Errorf("expect v1.0.0, got %v", tags)
	}

	checkCommit := func(commit string) {
		value, err := fakeGerrit.ReadFile(ctx, fakeRepo.name, commit, "go.mod")
		if err != nil {
			t.Fatalf("unable to read go.mod: %v", err)
		}
		want := "module test\n\ngo 1.21.2\n"
		if string(value) != want {
			t.Errorf("expected %q, got %q", want, string(value))
		}

	}

	tag, err := fakeGerrit.GetTag(ctx, fakeRepo.name, "v1.0.0")
	if err != nil {
		t.Fatalf("unable to get tag v1.0.0: %v", err)
	}
	checkCommit(tag.Revision)

	head, err := fakeGerrit.ReadBranchHead(ctx, fakeRepo.name, "master")
	if err != nil {
		t.Fatalf("unable to read branch head: %v", err)
	}
	checkCommit(head)
}
