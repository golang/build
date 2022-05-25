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
	AwaitSubmit(ctx context.Context, changeID string) (string, error)
	// Tag creates a tag on project at the specified commit.
	Tag(ctx context.Context, project, tag, commit string) error
}

type realGerritClient struct {
	client *gerrit.Client
}

func (c *realGerritClient) CreateAutoSubmitChange(ctx context.Context, input gerrit.ChangeInput, files map[string]string) (string, error) {
	change, err := c.client.CreateChange(ctx, input)
	if err != nil {
		return "", err
	}
	changeID := fmt.Sprintf("%s~%d", change.Project, change.ChangeNumber)
	for path, content := range files {
		if content == "" {
			if err := c.client.DeleteFileInChangeEdit(ctx, changeID, path); err != nil {
				return "", err
			}
		} else {
			if err := c.client.ChangeFileContentInChangeEdit(ctx, changeID, path, content); err != nil {
				return "", err
			}
		}
	}

	if err := c.client.PublishChangeEdit(ctx, changeID); err != nil {
		return "", err
	}
	if err := c.client.SetReview(ctx, changeID, "current", gerrit.ReviewInput{
		Labels: map[string]int{
			"Run-TryBot":  1,
			"Auto-Submit": 1,
		},
	}); err != nil {
		return "", err
	}
	return changeID, nil
}

func (c *realGerritClient) AwaitSubmit(ctx context.Context, changeID string) (string, error) {
	for {
		detail, err := c.client.GetChangeDetail(ctx, changeID, gerrit.QueryChangesOpt{
			Fields: []string{"CURRENT_REVISION", "DETAILED_LABELS"},
		})
		if err != nil {
			return "", err
		}
		if detail.Status == "MERGED" {
			return detail.CurrentRevision, nil
		}
		for _, approver := range detail.Labels["TryBot-Result"].All {
			if approver.Value < 0 {
				return "", fmt.Errorf("trybots failed on %v", changeLink(changeID))
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func (c *realGerritClient) Tag(ctx context.Context, project, tag, commit string) error {
	_, err := c.client.CreateTag(ctx, project, tag, gerrit.TagInput{
		Revision: commit,
	})
	return err
}

// changeLink returns a link to the review page for the CL with the specified
// change ID. The change ID must be in the project~cl# form.
func changeLink(changeID string) string {
	parts := strings.SplitN(changeID, "~", 3)
	if len(parts) != 2 {
		return fmt.Sprintf("(unparseable change ID %q)", changeID)
	}
	return "https://go.dev/cl/" + parts[1]
}
