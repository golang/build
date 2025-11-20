// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"
	goversion "go/version"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/modfile"
)

type UpdateProxyTestRepoTasks struct {
	Gerrit  DestructiveGerritClient // destructive needed to overwrite v1.0.0 tag
	Project string
	Branch  string
	// ChangeLink is an optional function to customize the URL of the review page
	// for the CL with the given change ID (in <project>~<changeNumber> format).
	ChangeLink func(changeID string) string
}

// UpdateProxyTestRepo updates the module proxy test repo whose purpose is to make
// sure modules containing go directives of the latest published version are fetchable.
func (t *UpdateProxyTestRepoTasks) UpdateProxyTestRepo(ctx *wf.TaskContext, published Published) error {
	// Read the go.mod file and check if version is higher.
	head, err := t.Gerrit.ReadBranchHead(ctx, t.Project, t.Branch)
	if err != nil {
		return err
	}
	ctx.Printf("Using commit %q as the branch head.", head)
	b, err := t.Gerrit.ReadFile(ctx, t.Project, head, "go.mod")
	if err != nil {
		return err
	}
	f, err := modfile.ParseLax("", b, nil)
	if err != nil {
		return err
	}
	// If the published version is lower than the current go.mod's go directive version, don't update.
	if f.Go != nil && goversion.Compare(published.Version, "go"+f.Go.Version) < 0 {
		ctx.Printf("No update needed.")
		return nil
	}

	// What's left at this point is to mail a Gerrit CL, and, after it's submitted, update a tag.
	// Out of abundance of caution, if something during those steps produces an unexpected error,
	// disable automatic retries. A human can inspect what happened and retry manually as needed.
	ctx.DisableRetries()

	// Create the go.mod file update CL and await its submission.
	changeID, err := t.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: t.Project, Branch: t.Branch,
		Subject: fmt.Sprintf("update go version to %s", published.Version),
	}, nil, map[string]string{
		"go.mod": fmt.Sprintf("module test\n\ngo %s\n", strings.TrimPrefix(published.Version, "go")),
	})
	if err != nil {
		return err
	}
	ctx.Printf("Awaiting review/submit of %s.", t.changeLink(changeID))
	submitted, err := AwaitCondition(ctx, time.Minute, func() (string, bool, error) {
		return t.Gerrit.Submitted(ctx, changeID, "")
	})
	if err != nil {
		return err
	}
	// Forcibly update the v1.0.0 tag.
	err = t.Gerrit.ForceTag(ctx, t.Project, "v1.0.0", submitted)
	if err != nil {
		return err
	}
	ctx.Printf("Updated the v1.0.0 tag of %q test repo to point to a commit whose go directive is %q.", t.Project, strings.TrimPrefix(published.Version, "go"))

	return nil
}

func (t *UpdateProxyTestRepoTasks) changeLink(changeID string) string {
	if t.ChangeLink != nil {
		return t.ChangeLink(changeID)
	}
	return ChangeLink(changeID)
}
