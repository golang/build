// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"errors"
	"fmt"
	"net/http"
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
