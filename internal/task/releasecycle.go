// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

// ReleaseCycleTasks implements tasks related to the Go release cycle (go.dev/s/release).
type ReleaseCycleTasks struct {
	Gerrit GerritClient
}

// ApplyWaitReleaseCLs applies a "wait-release" hashtag to remaining open CLs that are
// adding new APIs and should wait for next release. This is done once at the start of
// the release freeze.
func (t ReleaseCycleTasks) ApplyWaitReleaseCLs(ctx *workflow.TaskContext) (result struct{}, _ error) {
	clsToWait, err := t.Gerrit.QueryChanges(ctx, "repo:go status:open -is:wip dir:api/next -hashtag:wait-release")
	if err != nil {
		return struct{}{}, err
	}
	ctx.Printf("Processing %d open Gerrit CLs to be marked with wait-release hashtag.", len(clsToWait))
	for _, cl := range clsToWait {
		const dryRun = false
		if dryRun {
			ctx.Printf("[dry run] Would've waited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
			continue
		}
		err := t.Gerrit.SetHashtags(ctx, cl.ID, gerrit.HashtagsInput{Add: []string{"wait-release"}})
		if err != nil {
			return struct{}{}, err
		}
		ctx.Printf("Waited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
		time.Sleep(3 * time.Second) // Take a moment between updating CLs to avoid a high rate of modify operations.
	}
	return struct{}{}, nil
}

// UnwaitWaitReleaseCLs changes all open Gerrit CLs with hashtag "wait-release" into "ex-wait-release".
// This is done once at the opening of a release cycle, currently via a standalone workflow.
func (t ReleaseCycleTasks) UnwaitWaitReleaseCLs(ctx *workflow.TaskContext) (result struct{}, _ error) {
	waitingCLs, err := t.Gerrit.QueryChanges(ctx, "status:open hashtag:wait-release")
	if err != nil {
		return struct{}{}, err
	}
	ctx.Printf("Processing %d open Gerrit CL with wait-release hashtag.", len(waitingCLs))
	for _, cl := range waitingCLs {
		const dryRun = false
		if dryRun {
			ctx.Printf("[dry run] Would've unwaited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
			continue
		}
		err := t.Gerrit.SetHashtags(ctx, cl.ID, gerrit.HashtagsInput{
			Remove: []string{"wait-release"},
			Add:    []string{"ex-wait-release"},
		})
		if err != nil {
			return struct{}{}, err
		}
		ctx.Printf("Unwaited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
		time.Sleep(3 * time.Second) // Take a moment between updating CLs to avoid a high rate of modify operations.
	}
	return struct{}{}, nil
}
