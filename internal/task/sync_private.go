// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"

	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
)

type PrivateMasterSyncTask struct {
	Git              *Git
	PrivateGerritURL string
	Ref              string
}

func (t *PrivateMasterSyncTask) NewDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam, groups.SecurityTeam}})
	// We use a Task, instead of an Action, even though we don't actually want
	// to return any result, because nothing depends on the Action, and if we
	// use an Action the definition will tell us we don't reference it anywhere
	// and say it should be deleted.
	synced := wf.Task0(wd, "Sync go-private master to public", func(ctx *wf.TaskContext) (string, error) {
		repo, err := t.Git.Clone(ctx, t.PrivateGerritURL)
		if err != nil {
			return "", err
		}

		// NOTE: we assume this is generally safe in the case of a race between
		// submitting a patch and resetting the master branch due to the ordering
		// of operations at Gerrit. If the submit wins, we reset the master
		// branch, and the submitted commit is orphaned, which is the expected
		// behavior anyway. If the reset wins, the submission will either be
		// cherry-picked onto the new base, which should either succeed, or fail
		// due to a merge conflict, or Gerrit will reject the submission because
		// something changed underneath it. Either case seems fine.
		if _, err := repo.RunCommand(ctx, "push", "--force", "origin", fmt.Sprintf("origin/%s:refs/heads/master", t.Ref)); err != nil {
			return "", err
		}

		return "finished", nil
	})
	wf.Output(wd, fmt.Sprintf("Reset master to %s", t.Ref), synced)
	return wd
}
