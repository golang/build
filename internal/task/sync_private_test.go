package task

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"

	"golang.org/x/build/internal/workflowtest"
)

func TestSyncPrivate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fakeRepo := NewFakeRepo(t, "fake")
		masterCommit := fakeRepo.CommitOnBranch("master", map[string]string{
			"hello": "there",
		})
		fakeRepo.Branch("public", masterCommit)
		publicCommit := fakeRepo.CommitOnBranch("public", map[string]string{
			"general": "kenobi",
		})

		sync := &PrivateMasterSyncTask{
			Git:              &Git{},
			PrivateGerritURL: fakeRepo.dir.dir, // kind of wild that this works
			Ref:              "public",
		}

		wd := sync.NewDefinition()
		w := workflowtest.Start(t, wd, map[string]any{})
		workflowtest.Run(t, context.Background(), w, nil)

		fakeRepo.runGit("switch", "master")
		newMasterCommit := strings.TrimSpace(string(fakeRepo.runGit("rev-parse", "HEAD")))
		// newMasterCommit := fakeRepo.ReadBranchHead(context.Background(), )

		if newMasterCommit != publicCommit {
			t.Fatalf("unexpected master commit: got %q, want %q", newMasterCommit, publicCommit)
		}
	})
}
