package task

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/build/internal/workflow"
)

func TestSyncPrivate(t *testing.T) {
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
	w, err := workflow.Start(wd, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Run(context.Background(), &verboseListener{t: t})
	if err != nil {
		t.Fatal(err)
	}

	fakeRepo.runGit("switch", "master")
	newMasterCommit := strings.TrimSpace(string(fakeRepo.runGit("rev-parse", "HEAD")))
	// newMasterCommit := fakeRepo.ReadBranchHead(context.Background(), )

	if newMasterCommit != publicCommit {
		t.Fatalf("unexpected master commit: got %q, want %q", newMasterCommit, publicCommit)
	}
}
