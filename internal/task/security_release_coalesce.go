// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"errors"
	"fmt"
	goversion "go/version"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
	yaml "gopkg.in/yaml.v3"
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
//  6. Creates cherry-picks of the submitted patches onto the release branches,
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
	milestoneNum := wf.Param(wd, wf.ParamDef[string]{
		Name:      "Release Milestone",
		ParamType: wf.BasicString,
		Doc:       `Release milestone for the security patch(es) being released.`,
		Example:   "123456",
		Check: func(num string) error {
			if !numOnlyRe.MatchString(num) {
				return errors.New("Milestone number must contain only numbers")
			}
			return nil
		},
	})

	// check CLs are ready
	cls := wf.Task1(wd, "Check changes", x.CheckChanges, milestoneNum)
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
	// TODO: consider using the release milestone to derive
	// the active version patch and backports?
	currentMajor, _, err := x.Version.GetCurrentMajor(ctx)
	if err != nil {
		return branchInfo{}, err
	}
	nextMinors, err := x.Version.GetNextMinorVersions(ctx, []int{currentMajor, currentMajor - 1})
	if err != nil {
		return branchInfo{}, err
	}
	switch _, err := x.Version.Gerrit.ReadBranchHead(ctx, "go", fmt.Sprintf("release-branch.go1.%d", currentMajor+1)); {
	case errors.Is(err, gerrit.ErrResourceNotExist):
		// The next release branch hasn't been cut yet. Include release branches for minors only.
		return branchInfo{
			CheckpointName: fmt.Sprintf("%s-%s-checkpoint", nextMinors[0], nextMinors[1]),
			PublicReleaseBranches: []string{
				fmt.Sprintf("release-branch.%s", nextMinors[0]),
				fmt.Sprintf("release-branch.%s", nextMinors[1]),
			},
		}, nil
	case err == nil:
		// Include release branches for the minors and the next release candidate.
		nextRC, err := x.Version.GetNextVersion(ctx, currentMajor+1, KindRC)
		if err != nil {
			return branchInfo{}, err
		}
		return branchInfo{
			CheckpointName: fmt.Sprintf("%s-%s-%s-checkpoint", nextRC, nextMinors[0], nextMinors[1]),
			PublicReleaseBranches: []string{
				fmt.Sprintf("release-branch.%s", nextRC),
				fmt.Sprintf("release-branch.%s", nextMinors[0]),
				fmt.Sprintf("release-branch.%s", nextMinors[1]),
			},
		}, nil
	default:
		return branchInfo{}, err
	}
}

// ReleaseMilestone contains all
// patches and their respective
// metadata for a given release.
type ReleaseMilestone struct {
	BuganizerID int              `yaml:"buganizer_id"`
	Patches     []*SecurityPatch `yaml:"security_patches"`
}

// SecurityPatch is a subset of the
// required metadata to release all
// patches contained by a milestone.
type SecurityPatch struct {
	Changelists      []string `yaml:"changelists"`
	TargetedReleases []string `yaml:"target_releases"`
}

func (x *SecurityReleaseCoalesceTask) CheckChanges(ctx *wf.TaskContext, milestoneNum string) ([]*gerrit.ChangeInfo, error) {
	const project = "security-metadata"
	var cls []*gerrit.ChangeInfo

	head, err := x.PrivateGerrit.ReadBranchHead(ctx, project, "main")
	if err != nil {
		return nil, err
	}

	buf, err := x.PrivateGerrit.ReadFile(ctx, project, head, path.Join("data", "milestones", milestoneNum+".yaml"))
	if err != nil {
		return nil, err
	}

	var rm ReleaseMilestone
	if err := yaml.NewDecoder(bytes.NewReader(buf)).Decode(&rm); err != nil {
		return nil, fmt.Errorf("cannot read milestone: %v", err)
	}

	for _, patch := range rm.Patches {
		for _, url := range patch.Changelists {
			_, num, _ := strings.Cut(url, "/+/")
			ci, err := x.PrivateGerrit.GetChange(ctx, num, gerrit.QueryChangesOpt{Fields: []string{"SUBMITTABLE"}})
			if err != nil {
				return nil, err
			}
			if !ci.Submittable {
				return nil, fmt.Errorf("change %s is not submittable", internalGerritChangeURL(num))
			}
			ra, err := x.PrivateGerrit.GetRevisionActions(ctx, num, "current")
			if err != nil {
				return nil, err
			}
			if ra["submit"] == nil || !ra["submit"].Enabled {
				return nil, fmt.Errorf("change %s is not submittable", internalGerritChangeURL(num))
			}
			cls = append(cls, ci)
		}
	}
	return cls, nil
}

func (x *SecurityReleaseCoalesceTask) CreateCheckpoint(ctx *wf.TaskContext, bi branchInfo) (string, error) {
	publicHead, err := x.PrivateGerrit.ReadBranchHead(ctx, "go", "public")
	if err != nil {
		return "", err
	}
	if _, err := x.PrivateGerrit.CreateBranch(ctx, "go", bi.CheckpointName, gerrit.BranchInput{Revision: publicHead}); err != nil {
		return "", err
	}
	return bi.CheckpointName, nil
}

func (x *SecurityReleaseCoalesceTask) MoveAndRebaseChanges(ctx *wf.TaskContext, checkpointBranch string, cls []*gerrit.ChangeInfo) ([]*gerrit.ChangeInfo, error) {
	for i, ci := range cls {
		movedCI, err := x.PrivateGerrit.MoveChange(ctx, ci.ID, checkpointBranch)
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
		rebasedCI, err := x.PrivateGerrit.RebaseChange(ctx, movedCI.ID, "")
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

			ci, err := x.PrivateGerrit.GetChange(ctx, change.ID, gerrit.QueryChangesOpt{Fields: []string{"SUBMITTABLE"}})
			if err != nil {
				return "", false, err
			}

			if !ci.Submittable {
				continue
			}

			submitted, err := x.PrivateGerrit.SubmitChange(ctx, ci.ID)
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

// majorFromMinor converts a release branch name from its minor version form to
// its major version form (i.e., release-branch.go1.2.3 to release-branch.go1.2).
func majorFromMinor(branch string) string {
	stripped := strings.TrimPrefix(branch, "release-branch.")
	major := goversion.Lang(stripped)
	return "release-branch." + major
}

var internalReleaseBranchPrefix = "internal-"

func (x *SecurityReleaseCoalesceTask) CreateInternalReleaseBranches(ctx *wf.TaskContext, bi branchInfo) ([]string, error) {
	// TODO: update step to commit the metadata
	// about the submitted changes and their
	// branch hashes to security-metadata.
	var internalBranches []string
	for _, next := range bi.PublicReleaseBranches {
		publicHead, err := x.PrivateGerrit.ReadBranchHead(ctx, "go", majorFromMinor(next))
		if err != nil {
			return nil, err
		}
		internalReleaseBranch := internalReleaseBranchPrefix + next
		if _, err := x.PrivateGerrit.CreateBranch(ctx, "go", internalReleaseBranch, gerrit.BranchInput{Revision: publicHead}); err != nil {
			return nil, err
		}
		internalBranches = append(internalBranches, internalReleaseBranch)
	}
	return internalBranches, nil
}

func (x *SecurityReleaseCoalesceTask) CreateCherryPicks(ctx *wf.TaskContext, releaseBranches []string, cls []*gerrit.ChangeInfo) (map[string][]string, error) {
	// TODO: this currently assumes we want to cherry-pick everything to all
	// branches, which is _normally_ the case, but sometimes is not accurate. We
	// can manually just abandon cherry-picks we don't care about, but probably
	// we should have a way to indicate which branches we want each patch
	// cherry-picked onto.

	cherryPicks := map[string][]string{}
	for _, ci := range cls {
		for _, releaseBranch := range releaseBranches {
			commitMessage, err := x.PrivateGerrit.GetCommitMessage(ctx, ci.ID)
			if err != nil {
				return nil, err
			}
			// TODO: might be cleaner to just pass this information from CreateInternalReleaseBranches
			commitMessage = fmt.Sprintf("[%s] %s", majorFromMinor(strings.TrimPrefix(releaseBranch, internalReleaseBranchPrefix)), commitMessage)

			cpCI, conflicts, err := x.PrivateGerrit.CreateCherryPick(ctx, ci.ID, releaseBranch, commitMessage)
			if err != nil {
				return nil, err
			}
			if conflicts {
				ctx.Printf("Cherry-pick of %s has merge conflicts against %s: %s", internalGerritChangeURL(ci.ChangeNumber), releaseBranch, internalGerritChangeURL(cpCI.ChangeNumber))
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
