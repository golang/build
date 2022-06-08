package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
)

type GerritClient interface {
	// CreateAutoSubmitChange creates a change with the given metadata and contents, sets
	// Run-TryBots and Auto-Submit, and returns its change ID.
	// If the content of a file is empty, that file will be deleted from the repository.
	CreateAutoSubmitChange(ctx context.Context, input gerrit.ChangeInput, contents map[string]string) (string, error)
	// AwaitSubmit waits for the specified change to be auto-submitted or fail
	// trybots. If the CL is submitted, returns the submitted commit hash.
	// If parentCommit is non-empty, the submitted CL's parent must match it.
	AwaitSubmit(ctx context.Context, changeID, parentCommit string) (string, error)
	// Tag creates a tag on project at the specified commit.
	Tag(ctx context.Context, project, tag, commit string) error
	// ListTags returns all the tags on project.
	ListTags(ctx context.Context, project string) ([]string, error)
	// ReadBranchHead returns the head of a branch in project.
	ReadBranchHead(ctx context.Context, project, branch string) (string, error)
}

type RealGerritClient struct {
	Client *gerrit.Client
}

func (c *RealGerritClient) CreateAutoSubmitChange(ctx context.Context, input gerrit.ChangeInput, files map[string]string) (string, error) {
	change, err := c.Client.CreateChange(ctx, input)
	if err != nil {
		return "", err
	}
	changeID := fmt.Sprintf("%s~%d", change.Project, change.ChangeNumber)
	for path, content := range files {
		if content == "" {
			if err := c.Client.DeleteFileInChangeEdit(ctx, changeID, path); err != nil {
				return "", err
			}
		} else {
			if err := c.Client.ChangeFileContentInChangeEdit(ctx, changeID, path, content); err != nil {
				return "", err
			}
		}
	}

	if err := c.Client.PublishChangeEdit(ctx, changeID); err != nil {
		return "", err
	}
	if err := c.Client.SetReview(ctx, changeID, "current", gerrit.ReviewInput{
		Labels: map[string]int{
			"Run-TryBot":  1,
			"Auto-Submit": 1,
		},
	}); err != nil {
		return "", err
	}
	return changeID, nil
}

func (c *RealGerritClient) AwaitSubmit(ctx context.Context, changeID, parentCommit string) (string, error) {
	for {
		detail, err := c.Client.GetChangeDetail(ctx, changeID, gerrit.QueryChangesOpt{
			Fields: []string{"CURRENT_REVISION", "DETAILED_LABELS", "CURRENT_COMMIT"},
		})
		if err != nil {
			return "", err
		}
		if detail.Status == "MERGED" {
			parents := detail.Revisions[detail.CurrentRevision].Commit.Parents
			if parentCommit != "" && (len(parents) != 1 || parents[0].CommitID != parentCommit) {
				return "", fmt.Errorf("expected merged CL %v to have one parent commit %v, has %v", ChangeLink(changeID), parentCommit, parents)
			}
			return detail.CurrentRevision, nil
		}
		for _, approver := range detail.Labels["TryBot-Result"].All {
			if approver.Value < 0 {
				return "", fmt.Errorf("trybots failed on %v", ChangeLink(changeID))
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func (c *RealGerritClient) Tag(ctx context.Context, project, tag, commit string) error {
	info, err := c.Client.GetTag(ctx, project, tag)
	if err != nil && err != gerrit.ErrTagNotExist {
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

func (c *RealGerritClient) ReadBranchHead(ctx context.Context, project, branch string) (string, error) {
	branchInfo, err := c.Client.GetBranch(ctx, project, branch)
	if err != nil {
		return "", err
	}
	return branchInfo.Revision, nil
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
