// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

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

func MoveAndRebaseAll(ctx *wf.TaskContext, client GerritClient, branch string, patches []*PatchChanges) ([]*PatchChanges, error) {
	for _, p := range patches {
		riders, err := securityRiders(p.Patch)
		if err != nil {
			return nil, err
		}
		for i, ci := range p.Changes {
			// Idempotent. Changes can be in the MERGED (HTTP 409) state which means
			// that they cannot be moved or rebased. Refetch it and if it is MERGED,
			// skip it similarly to submitPrivateChanges.
			fresh, err := client.GetChange(ctx, ci.ID)
			if err != nil {
				return nil, err
			}
			if fresh.Status == gerrit.ChangeStatusMerged {
				p.Changes[i] = fresh
				continue
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
		for _, clURL := range p.Changelists {
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
