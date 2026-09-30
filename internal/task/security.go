// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relmeta"
)

type PatchChanges struct {
	Patch   *relmeta.SecurityPatch
	Changes []*gerrit.ChangeInfo
}

func securityRiders(p *relmeta.SecurityPatch) (string, error) {
	if p.CVE == "" {
		return "", fmt.Errorf("security patch %d has no CVE", p.ID)
	}
	if p.GitHubIssueID == 0 {
		return "", fmt.Errorf("security patch %d has no GitHub issue", p.ID)
	}
	return fmt.Sprintf("Fixes %s\nFor golang/go#%d", p.CVE, p.GitHubIssueID), nil
}

func insertRiders(message, riders string) string {
	message = strings.TrimRight(message, "\n")
	if i := strings.LastIndex(message, "\n\n"); i >= 0 && strings.Contains(message[i+2:], "Change-Id:") {
		return message[:i+2] + riders + "\n\n" + message[i+2:] + "\n"
	}
	return message + "\n\n" + riders + "\n"
}

func MoveAndRebase(ctx *wf.TaskContext, client GerritClient, changeID, branch string) (*gerrit.ChangeInfo, error) {
	ci, err := client.GetChange(ctx, changeID)
	if err != nil {
		return nil, err
	}
	// TODO(nealpatel): Think through how to
	// deal with this footgun.
	//
	// For patches that specify DeploymentMap,
	// this implies that a trusted patch owner
	// who accidentally submits will cause the
	// workflow to be blind: The workflow thinks
	// it landed on the target branch but it only
	// landed on the `-staging` branch.
	if ci.Status == gerrit.ChangeStatusMerged {
		return ci, nil
	}
	movedCI, err := client.MoveChange(ctx, ci.ID, branch)
	if err != nil {
		var httpErr *gerrit.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Res.StatusCode != http.StatusConflict || string(httpErr.Body) != "Change is already destined for the specified branch\n" {
			return nil, err
		}
	} else {
		ci = &movedCI
	}
	rebasedCI, err := client.RebaseChange(ctx, ci.ID, "")
	if err != nil {
		var httpErr *gerrit.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Res.StatusCode != http.StatusConflict || string(httpErr.Body) != "Change is already up to date.\n" {
			return nil, err
		}
	} else {
		ci = &rebasedCI
	}
	return ci, nil
}

func MoveAndRebaseAll(ctx *wf.TaskContext, client GerritClient, cp Checkpoint, patches []*PatchChanges) ([]*PatchChanges, error) {
	for _, p := range patches {
		riders, err := securityRiders(p.Patch)
		if err != nil {
			return nil, err
		}
		for i, ci := range p.Changes {
			ci, err := MoveAndRebase(ctx, client, ci.ID, cp.Branch)
			if err != nil {
				return nil, err
			}
			if ci.Status == gerrit.ChangeStatusMerged {
				p.Changes[i] = ci
				continue
			}
			cm, err := client.GetCommitMessage(ctx, ci.ID)
			if err != nil {
				return nil, err
			}
			if !strings.Contains(cm, riders) {
				if err := client.SetCommitMessage(ctx, ci.ID, insertRiders(cm, riders)); err != nil {
					return nil, err
				}
			}
			p.Changes[i] = ci
		}
	}
	return patches, nil
}

var (
	commitCVERE   = regexp.MustCompile(`(?m)^Fixes CVE-\d{4}-\d+`)
	commitIssueRE = regexp.MustCompile(`(?m)^\w+ (?:golang/go)?#(\d+)`)
	RiderIssueRE  = regexp.MustCompile(`(?m)^\w+ golang/go#(\d+)`)
)

func PrivateChangeURL[T int | string](project string, clNum T) string {
	return fmt.Sprintf("https://go-internal-review.git.corp.google.com/c/%s/+/%v", project, clNum)
}

func DeployedChangelists(p *relmeta.SecurityPatch, project, branch string) []string {
	if len(p.DeploymentMap) == 0 {
		return p.Changelists
	}
	var cls []string
	for _, clURL := range p.Changelists {
		if p.DeploymentMap[clURL] == project+":"+branch {
			cls = append(cls, clURL)
		}
	}
	return cls
}

func CheckDeploymentMap(p *relmeta.SecurityPatch, project string, branches []string) error {
	if len(p.DeploymentMap) == 0 {
		return nil
	}
	for _, clURL := range p.Changelists {
		deployment, ok := p.DeploymentMap[clURL]
		if !ok {
			return fmt.Errorf("security patch %d: changelist %s is missing from the deployment map", p.ID, clURL)
		}
		deployedProject, branch, ok := strings.Cut(deployment, ":")
		if !ok {
			return fmt.Errorf("security patch %d: changelist %s is deployed to %q, want <project>:<branch>", p.ID, clURL, deployment)
		}
		if deployedProject == project && !slices.Contains(branches, branch) {
			return fmt.Errorf("security patch %d: changelist %s is deployed to %q, want one of %q", p.ID, clURL, branch, branches)
		}
	}
	return nil
}

func CheckPrivateChanges(ctx *wf.TaskContext, client GerritClient, project string, patches []*relmeta.SecurityPatch) ([]*PatchChanges, error) {
	var (
		checked  []*PatchChanges
		lintErrs []error
	)
	for _, p := range patches {
		if p.Track == relmeta.Public {
			continue
		}
		var cls []*gerrit.ChangeInfo
		for _, clURL := range DeployedChangelists(p, project, "public") {
			_, num, ok := strings.Cut(clURL, "/+/")
			if !ok {
				return nil, fmt.Errorf("security patch %d: malformed changelist URL %q", p.ID, clURL)
			}
			ci, err := client.GetChange(ctx, num, gerrit.QueryChangesOpt{Fields: []string{"SUBMITTABLE"}})
			if err != nil {
				return nil, err
			}
			if ci.Project != project {
				return nil, fmt.Errorf("change %s is for project %q, want %q", PrivateChangeURL(project, num), ci.Project, project)
			}
			if ci.Status == gerrit.ChangeStatusMerged {
				cls = append(cls, ci)
				continue
			}
			if !ci.Submittable {
				return nil, fmt.Errorf("change %s is not submittable", PrivateChangeURL(project, num))
			}
			ra, err := client.GetRevisionActions(ctx, num, "current")
			if err != nil {
				return nil, err
			}
			if ra["submit"] == nil || !ra["submit"].Enabled {
				return nil, fmt.Errorf("change %s is not submittable", PrivateChangeURL(project, num))
			}
			if ci.Branch == "public" {
				cm, err := client.GetCommitMessage(ctx, num)
				if err != nil {
					return nil, err
				}
				if commitCVERE.MatchString(cm) {
					lintErrs = append(lintErrs, fmt.Errorf("change %s must not contain a CVE reference", PrivateChangeURL(project, num)))
				}
				if commitIssueRE.MatchString(cm) {
					lintErrs = append(lintErrs, fmt.Errorf("change %s must not contain a GitHub issue reference", PrivateChangeURL(project, num)))
				}
			}
			cls = append(cls, ci)
		}
		checked = append(checked, &PatchChanges{Patch: p, Changes: cls})
	}
	if err := errors.Join(lintErrs...); err != nil {
		return nil, err
	}
	return checked, nil
}

func SubmitPrivateChanges(ctx *wf.TaskContext, client GerritClient, project string, patches []*PatchChanges) ([]*PatchChanges, error) {
	if _, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		var blocking []string
		for _, p := range patches {
			for i, change := range p.Changes {
				if change.Status == gerrit.ChangeStatusMerged {
					continue
				}
				ci, err := client.GetChange(ctx, change.ID, gerrit.QueryChangesOpt{Fields: []string{"SUBMITTABLE"}})
				if err != nil {
					return "", false, err
				}
				if ci.Status == gerrit.ChangeStatusMerged {
					p.Changes[i] = ci
					continue
				}
				if !ci.Submittable {
					blocking = append(blocking, PrivateChangeURL(project, ci.ChangeNumber))
					continue
				}
				submitted, err := client.SubmitChange(ctx, ci.ID)
				if err != nil {
					return "", false, err
				}
				p.Changes[i] = &submitted
			}
		}
		if len(blocking) == 0 {
			return "", true, nil
		}
		ctx.Printf("awaiting non-submittable CL(s): %s", strings.Join(blocking, ", "))
		return "", false, nil
	}); err != nil {
		return nil, err
	}
	return patches, nil
}

func AwaitSubmitted(ctx *wf.TaskContext, client GerritClient, changeIDs []string) error {
	if len(changeIDs) == 0 {
		ctx.Printf("No CLs were necessary.")
		return nil
	}
	ctx.Printf("Awaiting review/submit of %d changes.", len(changeIDs))
	for _, c := range changeIDs {
		ctx.Printf("• %s", ChangeLink(c))
	}
	_, err := AwaitCondition(ctx, 10*time.Second, func() (struct{}, bool, error) {
		for _, c := range changeIDs {
			_, submitted, err := client.Submitted(ctx, c, "")
			if err != nil {
				return struct{}{}, false, err
			}
			if !submitted {
				return struct{}{}, false, nil
			}
		}
		return struct{}{}, true, nil
	})
	return err
}

type Checkpoint struct {
	Branch       string
	StartingHead string
}

func CreateCheckpoint(ctx *wf.TaskContext, client GerritClient, project, prefix string) (Checkpoint, error) {
	publicHead, err := client.ReadBranchHead(ctx, project, "public")
	if err != nil {
		return Checkpoint{}, err
	}
	// Append the formatted timestamp to make any restarts idempotent.
	branch := prefix + "-" + time.Now().UTC().Format("20060102-150405")
	if _, err := client.CreateBranch(ctx, project, branch, gerrit.BranchInput{Revision: publicHead}); err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{Branch: branch, StartingHead: publicHead}, nil
}

func ConvertPatchChangelists(ctx *wf.TaskContext, private, public GerritClient, project string, rm *relmeta.ReleaseMilestone, patches []*PatchChanges, reviewers []string) (*relmeta.ReleaseMilestone, error) {
	if rm == nil || len(patches) == 0 {
		return rm, nil
	}
	var sps []*relmeta.SecurityPatch
	for _, p := range patches {
		sps = append(sps, p.Patch)
	}
	external, err := ResolveExternalChangelists(ctx, private, public, project, sps)
	if err != nil {
		return nil, err
	}
	return ConvertInternalChangelists(ctx, private, strconv.FormatInt(rm.ID, 10), external, reviewers)
}

func ReadCheckpointHead(ctx context.Context, client GerritClient, project string, cp Checkpoint) (string, error) {
	return client.ReadBranchHead(ctx, project, cp.Branch)
}
