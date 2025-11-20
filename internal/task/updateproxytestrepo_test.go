// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"testing"

	"golang.org/x/build/internal/workflow"
)

func TestUpdateProxyTestRepo(t *testing.T) {
	tc := []struct {
		name       string
		old, new   string
		wantUpdate bool
	}{
		{"minor version", "1.18.1", "1.18.5", true},
		{"update to rc", "1.20", "1.21rc1", true},
		{"update rc to point", "1.18rc1", "1.18.0", true},
		{"no update earlier major", "1.18.5", "1.17.4", false},
		{"no update earlier major rc", "1.18rc1", "1.17", false},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			fakeRepo := NewFakeRepo(t, "fake")
			fakeGerrit := NewFakeGerrit(t, fakeRepo)

			fakeRepo.CommitOnBranch("master", map[string]string{
				"go.mod": fmt.Sprintf("module test\n\ngo %s\n", tt.old),
			})
			fakeRepo.Tag("v1.0.0", "master")

			upgradeGoVersion := &UpdateProxyTestRepoTasks{
				Gerrit:  fakeGerrit,
				Project: fakeRepo.name,
				Branch:  "master",
			}

			ctx := &workflow.TaskContext{
				Context: context.Background(),
				Logger:  &testLogger{t, ""},
			}
			if err := upgradeGoVersion.UpdateProxyTestRepo(ctx, Published{Version: "go" + tt.new}); err != nil {
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
				wantVersion := tt.new
				if !tt.wantUpdate {
					wantVersion = tt.old
				}

				want := fmt.Sprintf("module test\n\ngo %s\n", wantVersion)
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
		})
	}
}
