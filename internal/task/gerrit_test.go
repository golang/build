// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
)

func TestFakeGerritDeleteBranch(t *testing.T) {
	ctx := context.Background()
	repo := NewFakeRepo(t, "deletebranch")
	gc := NewFakeGerrit(t, repo)

	head, err := gc.ReadBranchHead(ctx, "deletebranch", "master")
	if err != nil {
		t.Fatalf("ReadBranchHead(master) failed: %v", err)
	}

	// Create a branch, then confirm it exists.
	if _, err := gc.CreateBranch(ctx, "deletebranch", "topic", gerrit.BranchInput{Revision: head}); err != nil {
		t.Fatalf("CreateBranch(topic) failed: %v", err)
	}
	if _, err := gc.ReadBranchHead(ctx, "deletebranch", "topic"); err != nil {
		t.Fatalf("ReadBranchHead(topic) after create failed: %v", err)
	}

	// Delete the branch, then confirm it's gone.
	if err := gc.DeleteBranch(ctx, "deletebranch", "topic"); err != nil {
		t.Fatalf("DeleteBranch(topic) failed: %v", err)
	}
	if _, err := gc.ReadBranchHead(ctx, "deletebranch", "topic"); !errors.Is(err, gerrit.ErrResourceNotExist) {
		t.Fatalf("ReadBranchHead(topic) after delete = %v, want error satisfying gerrit.ErrResourceNotExist", err)
	}

	// Deleting a branch that doesn't exist mirrors real Gerrit's 404,
	// returning an error satisfying gerrit.ErrResourceNotExist so that
	// delete-then-recreate callers can delete unconditionally.
	if err := gc.DeleteBranch(ctx, "deletebranch", "topic"); !errors.Is(err, gerrit.ErrResourceNotExist) {
		t.Fatalf("DeleteBranch(topic) on missing branch = %v, want error satisfying gerrit.ErrResourceNotExist", err)
	}
}

func TestNoOpCL(t *testing.T) {
	if !*flagRunVersionTest {
		t.Skip("Not enabled by flags")
	}
	cl := gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth())
	gcl := &RealGerritClient{Client: cl}

	ctx := &wf.TaskContext{Context: context.Background()}
	changeID, err := gcl.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "scratch",
		Branch:  "master",
		Subject: "no-op CL test",
	}, nil, map[string]string{"NONEXISTANT_FILE": ""})
	if err != nil {
		t.Fatal(err)
	}
	if changeID != "" {
		t.Fatalf("creating no-op change resulted in a CL %v (%q), wanted none", ChangeLink(changeID), changeID)
	}
}
