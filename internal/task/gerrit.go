// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
)

type GerritClient interface {
	// GitilesURL returns the URL to the Gitiles server for this Gerrit instance.
	GitilesURL() string
	// CreateAutoSubmitChange creates a change with the given metadata and
	// contents, starts trybots with auto-submit enabled, and returns its change ID.
	// If the content of a file is empty, that file will be deleted from the repository.
	// If the requested contents match the state of the repository, no change
	// is created and the returned change ID will be empty.
	// Reviewers is the username part of a golang.org or google.com email address.
	CreateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string, contents map[string]string) (string, error)
	// Submitted checks if the specified change has been submitted or failed
	// trybots. If the CL is submitted, returns the submitted commit hash.
	// If parentCommit is non-empty, the submitted CL's parent must match it.
	Submitted(ctx context.Context, changeID, parentCommit string) (string, bool, error)
	// GetTag returns tag information for a specified tag.
	GetTag(ctx context.Context, project, tag string) (gerrit.TagInfo, error)
	// Tag creates a tag on project at the specified commit.
	Tag(ctx context.Context, project, tag, commit string) error
	// ListTags returns all the tags on project.
	ListTags(ctx context.Context, project string) ([]string, error)
	// ReadBranchHead returns the head of a branch in project.
	// If the branch doesn't exist, it returns an error matching gerrit.ErrResourceNotExist.
	ReadBranchHead(ctx context.Context, project, branch string) (string, error)
	// ListBranches returns the branch info for all the branch in a project.
	ListBranches(ctx context.Context, project string) ([]gerrit.BranchInfo, error)
	// CreateBranch create the given branch and returns the created branch's revision.
	CreateBranch(ctx context.Context, project, branch string, input gerrit.BranchInput) (string, error)
	// ListProjects lists all the projects on the server.
	ListProjects(ctx context.Context) ([]string, error)
	// ReadFile reads a file from project at the specified commit.
	// If the file doesn't exist, it returns an error matching gerrit.ErrResourceNotExist.
	ReadFile(ctx context.Context, project, commit, file string) ([]byte, error)
	// GetCommitsInRefs gets refs in which the specified commits were merged into.
	GetCommitsInRefs(ctx context.Context, project string, commits, refs []string) (map[string][]string, error)
	// QueryChanges gets changes which match the query.
	QueryChanges(ctx context.Context, query string) ([]*gerrit.ChangeInfo, error)
	// SetHashtags modifies the hashtags for a CL.
	SetHashtags(ctx context.Context, changeID string, hashtags gerrit.HashtagsInput) error
	// GetChange gets information about a specific change.
	GetChange(ctx context.Context, changeID string, opts ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error)
}

type RealGerritClient struct {
	Gitiles string // Gitiles server URL, without trailing slash. For example, "https://go.googlesource.com".
	Client  *gerrit.Client
}

func (c *RealGerritClient) GitilesURL() string {
	return c.Gitiles
}

func (c *RealGerritClient) CreateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string, files map[string]string) (_ string, err error) {
	defer func() {
		// Check if status code is known to be not retryable.
		if he := (*gerrit.HTTPError)(nil); errors.As(err, &he) && he.Res.StatusCode/100 == 4 {
			ctx.DisableRetries()
		}
	}()

	reviewerEmails, err := coordinatorEmails(reviewers)
	if err != nil {
		return "", err
	}

	change, err := c.Client.CreateChange(ctx, input)
	if err != nil {
		return "", err
	}
	changeID := fmt.Sprintf("%s~%d", change.Project, change.ChangeNumber)
	anyChange := false
	for path, content := range files {
		var err error
		if content == "" {
			err = c.Client.DeleteFileInChangeEdit(ctx, changeID, path)
		} else {
			err = c.Client.ChangeFileContentInChangeEdit(ctx, changeID, path, content)
		}
		if errors.Is(err, gerrit.ErrNotModified) {
			continue
		}
		if err != nil {
			return "", err
		}
		anyChange = true
	}
	if !anyChange {
		if err := c.Client.AbandonChange(ctx, changeID, "no changes necessary"); err != nil {
			return "", err
		}
		return "", nil
	}

	if err := c.Client.PublishChangeEdit(ctx, changeID); err != nil {
		return "", err
	}

	var reviewerInputs []gerrit.ReviewerInput
	for _, r := range reviewerEmails {
		if r == "joedian@golang.org" {
			// A temporary workaround for 'https://go-review.googlesource.com/accounts/joedian@golang.org' being 404.
			// TODO(go.dev/issue/68276): Generalize this.
			continue
		}
		reviewerInputs = append(reviewerInputs, gerrit.ReviewerInput{Reviewer: r})
	}
	if err := c.Client.SetReview(ctx, changeID, "current", gerrit.ReviewInput{
		Labels: map[string]int{
			"Commit-Queue": 1,
			"Auto-Submit":  1,
		},
		Reviewers: reviewerInputs,
	}); err != nil {
		return "", err
	}
	return changeID, nil
}

func (c *RealGerritClient) Submitted(ctx context.Context, changeID, parentCommit string) (string, bool, error) {
	detail, err := c.Client.GetChangeDetail(ctx, changeID, gerrit.QueryChangesOpt{
		Fields: []string{"CURRENT_REVISION", "DETAILED_LABELS", "CURRENT_COMMIT"},
	})
	if err != nil {
		return "", false, err
	}
	if detail.Status == "MERGED" {
		parents := detail.Revisions[detail.CurrentRevision].Commit.Parents
		if parentCommit != "" && (len(parents) != 1 || parents[0].CommitID != parentCommit) {
			return "", false, fmt.Errorf("expected merged CL %v to have one parent commit %v, has %v", ChangeLink(changeID), parentCommit, parents)
		}
		return detail.CurrentRevision, true, nil
	}
	for _, approver := range detail.Labels["TryBot-Result"].All {
		if approver.Value < 0 {
			return "", false, fmt.Errorf("trybots failed on %v", ChangeLink(changeID))
		}
	}
	return "", false, nil
}

func (c *RealGerritClient) Tag(ctx context.Context, project, tag, commit string) error {
	info, err := c.Client.GetTag(ctx, project, tag)
	if err != nil && !errors.Is(err, gerrit.ErrResourceNotExist) {
		return fmt.Errorf("checking if tag already exists: %v", err)
	}
	if err == nil {
		if info.Revision != commit {
			return fmt.Errorf("tag %q already exists on revision %q rather than our %q", tag, info.Revision, commit)
		} else {
			// Nothing to do.
			return nil
		}
	}

	_, err = c.Client.CreateTag(ctx, project, tag, gerrit.TagInput{
		Revision: commit,
	})
	return err
}

func (c *RealGerritClient) ListTags(ctx context.Context, project string) ([]string, error) {
	tags, err := c.Client.GetProjectTags(ctx, project)
	if err != nil {
		return nil, err
	}
	var tagNames []string
	for _, tag := range tags {
		tagNames = append(tagNames, strings.TrimPrefix(tag.Ref, "refs/tags/"))
	}
	return tagNames, nil
}

func (c *RealGerritClient) GetTag(ctx context.Context, project, tag string) (gerrit.TagInfo, error) {
	return c.Client.GetTag(ctx, project, tag)
}

func (c *RealGerritClient) ReadBranchHead(ctx context.Context, project, branch string) (string, error) {
	branchInfo, err := c.Client.GetBranch(ctx, project, branch)
	if err != nil {
		return "", err
	}
	return branchInfo.Revision, nil
}

func (c *RealGerritClient) ListBranches(ctx context.Context, project string) ([]gerrit.BranchInfo, error) {
	return c.Client.ListBranches(ctx, project)
}

func (c *RealGerritClient) CreateBranch(ctx context.Context, project, branch string, input gerrit.BranchInput) (string, error) {
	branchInfo, err := c.Client.CreateBranch(ctx, project, branch, input)
	if err != nil {
		return "", err
	}
	return branchInfo.Revision, nil
}

func (c *RealGerritClient) ListProjects(ctx context.Context) ([]string, error) {
	projects, err := c.Client.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return names, nil
}

func (c *RealGerritClient) ReadFile(ctx context.Context, project, commit, file string) ([]byte, error) {
	body, err := c.Client.GetFileContent(ctx, project, commit, file)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

func (c *RealGerritClient) GetCommitsInRefs(ctx context.Context, project string, commits, refs []string) (map[string][]string, error) {
	return c.Client.GetCommitsInRefs(ctx, project, commits, refs)
}

// ChangeLink returns a link to the review page for the CL with the specified
// change ID. The change ID must be in the project~cl# form.
func ChangeLink(changeID string) string {
	parts := strings.SplitN(changeID, "~", 3)
	if len(parts) != 2 {
		return fmt.Sprintf("(unparseable change ID %q)", changeID)
	}
	return "https://go.dev/cl/" + parts[1]
}

func (c *RealGerritClient) QueryChanges(ctx context.Context, query string) ([]*gerrit.ChangeInfo, error) {
	return c.Client.QueryChanges(ctx, query)
}

func (c *RealGerritClient) GetChange(ctx context.Context, changeID string, opts ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error) {
	return c.Client.GetChange(ctx, changeID, opts...)
}

func (c *RealGerritClient) SetHashtags(ctx context.Context, changeID string, hashtags gerrit.HashtagsInput) error {
	_, err := c.Client.SetHashtags(ctx, changeID, hashtags)
	return err
}
