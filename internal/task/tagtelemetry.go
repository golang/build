// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
)

// TagTelemetryTasks implements a new workflow definition to tag
// x/telemetry/config whenever the generated config.json changes.
type TagTelemetryTasks struct {
	Gerrit     GerritClient
	CloudBuild CloudBuildClient
}

func (t *TagTelemetryTasks) NewDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	reviewers := wf.Param(wd, reviewersParam)
	changeID := wf.Task1(wd, "generate config CL", t.GenerateConfig, reviewers)
	submitted := wf.Action1(wd, "await config CL submission", t.AwaitSubmission, changeID)
	tag := wf.Task0(wd, "tag if appropriate", t.MaybeTag, wf.After(submitted))
	wf.Output(wd, "tag", tag)

	return wd
}

// GenerateConfig runs the upload config generator in a buildlet, extracts the
// resulting config.json, and creates a CL with the result if anything changed.
//
// It returns the change ID, or "" if the CL was not created.
func (t *TagTelemetryTasks) GenerateConfig(ctx *wf.TaskContext, reviewers []string) (string, error) {
	const clTitle = "config: regenerate upload config"

	// Query for an existing pending config CL, to avoid duplication.
	//
	// Only wait a week, because configs are volatile: we really want to update
	// them within a week.
	query := fmt.Sprintf(`message:%q status:open owner:gobot@golang.org repo:telemetry -age:7d`, clTitle)
	changes, err := t.Gerrit.QueryChanges(ctx, query)
	if err != nil {
		return "", err
	}
	if len(changes) > 0 {
		ctx.Printf("not creating CL: found existing CL %d", changes[0].ChangeNumber)
		return "", nil
	}

	const script = `
cp config/config.json config/config.json.before
go run ./internal/configgen -w
`

	build, err := t.CloudBuild.RunScript(ctx, script, "telemetry", []string{"config/config.json.before", "config/config.json"})
	if err != nil {
		return "", err
	}

	outputs, err := buildToOutputs(ctx, t.CloudBuild, build)
	if err != nil {
		return "", err
	}

	before, after := outputs["config/config.json.before"], outputs["config/config.json"]
	if before == after {
		ctx.Printf("not creating CL: config has not changed")
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "telemetry",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the generated upload config.", clTitle),
		Branch:  "master",
	}
	files := map[string]string{
		"config/config.json": string(after),
	}
	return t.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, files)
}

// AwaitSubmission waits for the CL with the given change ID to be submitted.
//
// The return value is the submitted commit hash, or "" if changeID is "".
func (t *TagTelemetryTasks) AwaitSubmission(ctx *wf.TaskContext, changeID string) error {
	if changeID == "" {
		ctx.Printf("not awaiting: no CL was created")
		return nil
	}

	ctx.Printf("awaiting review/submit of %v", ChangeLink(changeID))
	_, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return t.Gerrit.Submitted(ctx, changeID, "")
	})
	return err
}

// MaybeTag tags x/telemetry/config with the next version if config/config.json
// has changed.
//
// It returns the tag that was created, or "" if no tagging occurred.
func (t *TagTelemetryTasks) MaybeTag(ctx *wf.TaskContext) (string, error) {
	latestTag, latestVersion, err := t.latestConfigVersion(ctx)
	if err != nil {
		return "", err
	}
	if latestTag == "" {
		ctx.Printf("not tagging: no existing release tag found, not tagging the initial version")
		return "", nil
	}
	tagInfo, err := t.Gerrit.GetTag(ctx, "telemetry", latestTag)
	if err != nil {
		return "", fmt.Errorf("reading tag %s: %v", latestTag, err)
	}

	latestConfig, err := t.Gerrit.ReadFile(ctx, "telemetry", tagInfo.Revision, "config/config.json")
	if err != nil {
		return "", fmt.Errorf("reading config/config.json@latest: %v", err)
	}
	master, err := t.Gerrit.ReadBranchHead(ctx, "telemetry", "master")
	if err != nil {
		return "", fmt.Errorf("reading master commit: %v", err)
	}
	masterConfig, err := t.Gerrit.ReadFile(ctx, "telemetry", master, "config/config.json")
	if err != nil {
		return "", fmt.Errorf("reading config/config.json@master: %v", err)
	}

	if bytes.Equal(latestConfig, masterConfig) {
		ctx.Printf("not tagging: no change to config.json since latest tag")
		return "", nil
	}

	nextVer, err := nextMinor(latestVersion)
	if err != nil {
		return "", fmt.Errorf("couldn't pick next version: %v", err)
	}
	tag := "config/" + nextVer

	ctx.Printf("tagging x/telemetry/config at %v as %v", master, tag)
	if err := t.Gerrit.Tag(ctx, "telemetry", tag, master); err != nil {
		return "", fmt.Errorf("failed to tag: %v", err)
	}

	return tag, nil
}

func (t *TagTelemetryTasks) latestConfigVersion(ctx context.Context) (tag, version string, _ error) {
	tags, err := t.Gerrit.ListTags(ctx, "telemetry")
	if err != nil {
		return "", "", err
	}
	latestTag := ""
	latestRelease := ""
	for _, tag := range tags {
		ver, ok := strings.CutPrefix(tag, "config/")
		if !ok {
			continue
		}
		if semver.IsValid(ver) && semver.Prerelease(ver) == "" &&
			(latestRelease == "" || semver.Compare(latestRelease, ver) < 0) {
			latestTag = tag
			latestRelease = ver
		}
	}
	return latestTag, latestRelease, nil
}
