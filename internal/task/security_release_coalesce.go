// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"errors"
	"fmt"
	goversion "go/version"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
)

// SecurityReleaseCoalesceTask is the workflow used to preparing patches for
// minor security releases. The workflow is described in detail in
// go/go-security-release-workflow.
//
// In short, this workflow:
//  1. Checks that all patches are ready, indicated by two Code-Review+2's labels
//     and a Security-Patch-Ready+1 label (this is checked via submit requirements
//     rather than directly inspecting the labels) and lack of merge conflicts
//  2. Creates a new branch from master HEAD
//  3. Moves all patches from master onto the new branch
//  4. Submits the rebased patches
//  5. Create internal release branches
//  6. Creates cherry-picks of the submitted patches onto the two release branches,
//     setting Commit-Queue+1
type SecurityReleaseCoalesceTask struct {
	PrivateGerrit GerritClient
	Version       *VersionTasks
}

func (x *SecurityReleaseCoalesceTask) NewDefinition() *wf.Definition {
	// TODO: this is currently not particularly tolerant of failures that happen
	// half way through the workflow. Will need to think a bit about how we can
	// recover in failure situations that doesn't require manually cleaning a
	// bunch of stuff up before re-running the workflow.

	var numOnlyRe = regexp.MustCompile(`^\d+$`)

	wd := wf.New(wf.ACL{Groups: []string{groups.SecurityTeam}})
	clNums := wf.Param(wd, wf.ParamDef[[]string]{
		Name:      "Security Patch CL Numbers",
		ParamType: wf.SliceShort,
		Doc:       `Gerrit CL numbers for each security patch in a release`,
		Example:   "123456",
		Check: func(nums []string) error {
			for _, num := range nums {
				if !numOnlyRe.MatchString(num) {
					return errors.New("CL numbers must contain only numbers")
				}
			}
			return nil
		},
	})

	// check CLs are ready
	cls := wf.Task1(wd, "Check changes", x.CheckChanges, clNums)
	// look up branch names
	branchInfo := wf.Task0(wd, "Get branch names", x.GetBranchNames, wf.After(cls))
	// create checkpoint branch
	checkpointBranch := wf.Task1(wd, "Create checkpoint branch", x.CreateCheckpoint, branchInfo)
	// rebase changes to checkpoint branch
	cls = wf.Task2(wd, "Move changes onto checkpoint branch", x.MoveAndRebaseChanges, checkpointBranch, cls)
	// wait for changes to be submittable, and then submit them
	cls = wf.Task1(wd, "Await submissions", x.WaitAndSubmit, cls)
	// create internal release branches
	internalReleaseBranches := wf.Task1(wd, "Create internal release branches", x.CreateInternalReleaseBranches, branchInfo, wf.After(cls))
	// create cherry-picks to internal release branches
	cherryPicks := wf.Task2(wd, "Create cherry-picks", x.CreateCherryPicks, internalReleaseBranches, cls)
	wf.Output(wd, "Cherry-picks", cherryPicks)

	return wd
}

type branchInfo struct {
	CheckpointName        string
	PublicReleaseBranches []string
}

func (x *SecurityReleaseCoalesceTask) GetBranchNames(ctx *wf.TaskContext) (branchInfo, error) {
	currentMajor, _, err := x.Version.GetCurrentMajor(ctx.Context)
	if err != nil {
		return branchInfo{}, err
	}
	nextMinors, err := x.Version.GetNextMinorVersions(ctx.Context, []int{currentMajor, currentMajor - 1})
	if err != nil {
		return branchInfo{}, err
	}

	return branchInfo{
		CheckpointName: fmt.Sprintf("%s-and-%s-checkpoint", nextMinors[0], nextMinors[1]),
		PublicReleaseBranches: []string{
			fmt.Sprintf("release-branch.%s", nextMinors[0]),
			fmt.Sprintf("release-branch.%s", nextMinors[1]),
		},
	}, nil
}

func (x *SecurityReleaseCoalesceTask) CheckChanges(ctx *wf.TaskContext, clNums []string) ([]*gerrit.ChangeInfo, error) {
	var cls []*gerrit.ChangeInfo
	for _, num := range clNums {
		ci, err := x.PrivateGerrit.GetChange(ctx.Context, num, gerrit.QueryChangesOpt{Fields: []string{"SUBMITTABLE"}})
		if err != nil {
			return nil, err
		}
		if !ci.Submittable {
			return nil, fmt.Errorf("Change %s is not submittable", internalGerritChangeURL(num))
		}
		ra, err := x.PrivateGerrit.GetRevisionActions(ctx.Context, num, "current")
		if err != nil {
			return nil, err
		}
		if ra["submit"] == nil || !ra["submit"].Enabled {
			return nil, fmt.Errorf("Change %s is not submittable", internalGerritChangeURL(num))
		}
		cls = append(cls, ci)
	}
	return cls, nil
}

func (x *SecurityReleaseCoalesceTask) CreateCheckpoint(ctx *wf.TaskContext, bi branchInfo) (string, error) {
	publicHead, err := x.PrivateGerrit.ReadBranchHead(ctx.Context, "go", "public")
	if err != nil {
		return "", err
	}
	if _, err := x.PrivateGerrit.CreateBranch(ctx.Context, "go", bi.CheckpointName, gerrit.BranchInput{Revision: publicHead}); err != nil {
		return "", err
	}
	return bi.CheckpointName, nil
}

func (x *SecurityReleaseCoalesceTask) MoveAndRebaseChanges(ctx *wf.TaskContext, checkpointBranch string, cls []*gerrit.ChangeInfo) ([]*gerrit.ChangeInfo, error) {
	for i, ci := range cls {
		movedCI, err := x.PrivateGerrit.MoveChange(ctx.Context, ci.ChangeID, checkpointBranch)
		if err != nil {
			// In case we need to re-run the Move step, tolerate the case where the change
			// is already on the branch.
			var httpErr *gerrit.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Res.StatusCode != http.StatusConflict || string(httpErr.Body) != "Change is already destined for the specified branch\n" {
				return nil, err
			}
		} else {
			cls[i] = &movedCI
		}
		rebasedCI, err := x.PrivateGerrit.RebaseChange(ctx.Context, ci.ChangeID, "")
		if err != nil {
			var httpErr *gerrit.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Res.StatusCode != http.StatusConflict || string(httpErr.Body) != "Change is already up to date.\n" {
				return nil, err
			}
		} else {
			cls[i] = &rebasedCI
		}
	}
	return cls, nil
}

func (x *SecurityReleaseCoalesceTask) WaitAndSubmit(ctx *wf.TaskContext, cls []*gerrit.ChangeInfo) ([]*gerrit.ChangeInfo, error) {
	if _, err := AwaitCondition(ctx, time.Second*10, func() (string, bool, error) {
		unsubmitted := len(cls)

		for i, change := range cls {
			if change.Status == gerrit.ChangeStatusMerged {
				unsubmitted--
				continue
			}

			ci, err := x.PrivateGerrit.GetChange(ctx, change.ChangeID, gerrit.QueryChangesOpt{Fields: []string{"SUBMITTABLE"}})
			if err != nil {
				return "", false, err
			}

			if !ci.Submittable {
				continue
			}

			submitted, err := x.PrivateGerrit.SubmitChange(ctx, ci.ChangeID)
			if err != nil {
				return "", false, err
			}

			cls[i] = &submitted
			unsubmitted--
		}

		if unsubmitted == 0 {
			return "", true, nil
		}
		return "", false, nil
	}); err != nil {
		return nil, err
	}

	return cls, nil
}

// majorFromMinor converts a release branch name from it's minor version form to
// it's major version form (i.e. release-branch.go1.2.3 to
// release-branch.go1.2).
func majorFromMinor(branch string) string {
	stripped := strings.TrimPrefix(branch, "release-branch.")
	major := goversion.Lang(stripped)
	return "release-branch." + major
}

var internalReleaseBranchPrefix = "internal-"

func (x *SecurityReleaseCoalesceTask) CreateInternalReleaseBranches(ctx *wf.TaskContext, bi branchInfo) ([]string, error) {
	var internalBranches []string
	for _, nextMinor := range bi.PublicReleaseBranches {
		publicHead, err := x.PrivateGerrit.ReadBranchHead(ctx.Context, "go", majorFromMinor(nextMinor))
		if err != nil {
			return nil, err
		}
		internalReleaseBranch := internalReleaseBranchPrefix + nextMinor
		if _, err := x.PrivateGerrit.CreateBranch(ctx.Context, "go", internalReleaseBranch, gerrit.BranchInput{Revision: publicHead}); err != nil {
			return nil, err
		}
		internalBranches = append(internalBranches, internalReleaseBranch)
	}
	return internalBranches, nil
}

func (x *SecurityReleaseCoalesceTask) CreateCherryPicks(ctx *wf.TaskContext, releaseBranches []string, cls []*gerrit.ChangeInfo) (map[string][]string, error) {
	// TODO: this currently assumes we want to cherry-pick everything to both
	// branches, which is _normally_ the case, but sometimes is not accurate. We
	// can manually just abandon cherry-picks we don't care about, but probably
	// we should have a way to indicate which branches we want each patch
	// cherry-picked onto.

	cherryPicks := map[string][]string{}
	for _, ci := range cls {
		for _, releaseBranch := range releaseBranches {
			commitMessage, err := x.PrivateGerrit.GetCommitMessage(ctx.Context, ci.ID)
			if err != nil {
				return nil, err
			}
			// TODO: might be cleaner to just pass this information from CreateInternalReleaseBranches
			commitMessage = fmt.Sprintf("[%s] %s", majorFromMinor(strings.TrimPrefix(releaseBranch, internalReleaseBranchPrefix)), commitMessage)

			cpCI, conflicts, err := x.PrivateGerrit.CreateCherryPick(ctx.Context, ci.ID, releaseBranch, commitMessage)
			if err != nil {
				return nil, err
			}
			if conflicts {
				ctx.Logger.Printf("Cherry-pick of %d has merge conflicts against %s: %s", internalGerritChangeURL(ci.ChangeNumber), releaseBranch, internalGerritChangeURL(cpCI.ChangeNumber))
			}
			cherryPicks[releaseBranch] = append(cherryPicks[releaseBranch], internalGerritChangeURL(cpCI.ChangeNumber))
		}
	}
	return cherryPicks, nil
}

// internalGerritChangeURL can take either a int or string and return the
// relevant CL URL for a change number.
func internalGerritChangeURL[T int | string](clNum T) string {
	return fmt.Sprintf("https://go-internal-review.git.corp.google.com/c/go/+/%v", clNum)
}
