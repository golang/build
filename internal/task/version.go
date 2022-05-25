package task

import (
	"fmt"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

// VersionTasks contains tasks related to versioning the release.
type VersionTasks struct {
	Gerrit  GerritClient
	Project string
}

// CreateAutoSubmitVersionCL mails an auto-submit change to update VERSION on branch.
func (t *VersionTasks) CreateAutoSubmitVersionCL(ctx *workflow.TaskContext, branch, version string) (string, error) {
	return t.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: t.Project,
		Branch:  branch,
		Subject: fmt.Sprintf("[%v] %v", branch, version),
	}, map[string]string{
		"VERSION": version,
	})
}

// AwaitCL waits for the specified CL to be submitted.
func (t *VersionTasks) AwaitCL(ctx *workflow.TaskContext, changeID string) (string, error) {
	ctx.Printf("Awaiting review/submit of %v", changeLink(changeID))
	return t.Gerrit.AwaitSubmit(ctx, changeID)
}

// TagRelease tags commit as version.
func (t *VersionTasks) TagRelease(ctx *workflow.TaskContext, version, commit string) (string, error) {
	return "", t.Gerrit.Tag(ctx, t.Project, version, commit)
}
