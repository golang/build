// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"runtime"
	"testing"
	"testing/synctest"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/internal/workflowtest"
)

func TestCheckAlreadyPublicizedIgnoresAbandoned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("Requires bash shell scripting support.")
		}

		pubRepo := NewFakeRepo(t, "go")
		base := pubRepo.Commit(map[string]string{"README": "hello"})
		pubRepo.Branch("release-branch.go1.26", base)

		privRepo := CloneFakeRepo(t, "go", pubRepo)
		privRepo.Branch("internal-release-branch.go1.26.1", base)
		privRepo.CommitOnBranchWithMessage("internal-release-branch.go1.26.1",
			"crypto/tls: fix vuln\n\nFixes CVE-2026-1234\n\nChange-Id: I0000000000000000000000000000000000000001",
			map[string]string{"security1.txt": "fix1"})

		pubGerrit := NewFakeGerrit(t, pubRepo)
		privGerrit := NewFakeGerrit(t, privRepo)
		ctx := &wf.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: t.Name()}}

		securityCommit, err := privGerrit.ReadBranchHead(ctx, "go", "internal-release-branch.go1.26.1")
		if err != nil {
			t.Fatal(err)
		}
		pubGerrit.AddChange("go", "pub-1", &gerrit.ChangeInfo{
			ID:           "pub-1",
			ChangeID:     "I0000000000000000000000000000000000000001",
			ChangeNumber: 9001,
			Branch:       "release-branch.go1.26",
			Status:       gerrit.ChangeStatusAbandoned,
		}, "crypto/tls: fix vuln")

		repo, err := new(Git).CloneBranch(ctx, pubGerrit.GitRepoURL("go"), "release-branch.go1.26")
		if err != nil {
			t.Fatal(err)
		}
		defer repo.Close()
		if _, err := repo.RunCommand(ctx, "fetch", privGerrit.GitRepoURL("go"), "refs/heads/internal-release-branch.go1.26.1"); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.RunCommand(ctx, "cherry-pick", base+".."+securityCommit); err != nil {
			t.Fatal(err)
		}

		existing, err := checkAlreadyPublicized(ctx, repo, pubGerrit, "go", "release-branch.go1.26", base)
		if err != nil {
			t.Fatalf("checkAlreadyPublicized: %v", err)
		}
		if len(existing) != 0 {
			t.Errorf("checkAlreadyPublicized treated abandoned CLs as already publicized: %v", existing)
		}
	})
}
