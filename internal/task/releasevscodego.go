// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/go-github/v48/github"
	"golang.org/x/build/internal/relui/groups"
	"golang.org/x/build/internal/workflow"
	wf "golang.org/x/build/internal/workflow"
)

// VSCode extensions and semantic versioning have different understandings of
// release and pre-release.

// From the VSCode extension guidelines:
// - Pre-releases contain the latest changes and use odd-numbered minor versions
// (e.g. v0.45.0).
// - Releases are more stable and use even-numbered minor versions (e.g.
// v0.44.0).
// See: https://code.visualstudio.com/api/working-with-extensions/publishing-extension#prerelease-extensions

// In semantic versioning:
// - Pre-releases use a hyphen and label (e.g. v0.44.0-rc.1).
// - Releases have no hyphen (e.g. v0.44.0).
// See: https://semver.org/spec/v2.0.0.html

// To avoid confusion, vscode-go release flow will use terminology below without
// overloading the term "pre-release":

// - Stable version: VSCode extension's release version (even minor, e.g. v0.44.0)
// - Insider version: VSCode extension's pre-release version (odd minor, e.g. v0.45.0)
// - Release version: Semantic versioning release (no hyphen, e.g. v0.44.0)
// - Pre-release version: Semantic versioning pre-release (with hyphen, e.g. v0.44.0-rc.1)

// ReleaseVSCodeGoTasks implements a set of vscode-go release workflow definitions.
//
// * pre-release workflow: creates a pre-release version of a stable version.
// * release workflow: creates a release version of a stable version.
// * insider workflow: creates a insider version. There are no pre-releases for
// insider versions.
type ReleaseVSCodeGoTasks struct {
	Gerrit        GerritClient
	GitHub        GitHubClientInterface
	ApproveAction func(*wf.TaskContext) error
}

var nextVersionParam = wf.ParamDef[string]{
	Name: "next version",
	ParamType: workflow.ParamType[string]{
		HTMLElement: "select",
		HTMLSelectOptions: []string{
			"next minor",
			"next patch",
			"use explicit version",
		},
	},
}

//go:embed template/vscode-go-release-issue.md
var vscodeGOReleaseIssueTmplStr string

// vsCodeGoActiveReleaseBranch returns the current active release branch in
// vscode-go project.
func vsCodeGoActiveReleaseBranch(ctx *wf.TaskContext, gerrit GerritClient) (string, error) {
	branches, err := gerrit.ListBranches(ctx, "vscode-go")
	if err != nil {
		return "", err
	}

	var latestMajor, latestMinor int
	active := ""
	for _, branch := range branches {
		branchName, found := strings.CutPrefix(branch.Ref, "refs/heads/")
		if !found {
			continue
		}
		majorMinor, found := strings.CutPrefix(branchName, "release-v")
		if !found {
			continue
		}
		versions := strings.Split(majorMinor, ".")
		if len(versions) != 2 {
			continue
		}

		major, err := strconv.Atoi(versions[0])
		if err != nil {
			continue
		}
		minor, err := strconv.Atoi(versions[1])
		if err != nil {
			continue
		}

		// Only consider release branch for stable versions.
		if minor%2 == 1 {
			continue
		}

		if major > latestMajor || (major == latestMajor && minor > latestMinor) {
			latestMajor = major
			latestMinor = minor
			active = branchName
		}
	}

	// "release" is the release branch before vscode-go switch multi release
	// branch model.
	if active == "" {
		active = "release"
	}

	return active, nil
}

// NewPrereleaseDefinition create a new workflow definition for vscode-go pre-release.
func (r *ReleaseVSCodeGoTasks) NewPrereleaseDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	versionBumpStrategy := wf.Param(wd, nextVersionParam)

	semv := wf.Task1(wd, "find the next pre-release version", r.nextPrereleaseVersion, versionBumpStrategy)
	approved := wf.Action1(wd, "await release coordinator's approval", r.approveVersion, semv)

	_ = wf.Task1(wd, "create release milestone and issue", r.createReleaseMilestoneAndIssue, semv, wf.After(approved))

	return wd
}

func (r *ReleaseVSCodeGoTasks) createReleaseMilestoneAndIssue(ctx *wf.TaskContext, semv semversion) (int, error) {
	version := fmt.Sprintf("v%v.%v.%v", semv.Major, semv.Minor, semv.Patch)

	// The vscode-go release milestone name matches the release version.
	milestoneID, err := r.GitHub.FetchMilestone(ctx, "golang", "vscode-go", version, true)
	if err != nil {
		return 0, err
	}

	title := fmt.Sprintf("Release %s", version)
	issues, err := r.GitHub.FetchMilestoneIssues(ctx, "golang", "vscode-go", milestoneID)
	if err != nil {
		return 0, err
	}
	for id := range issues {
		issue, _, err := r.GitHub.GetIssue(ctx, "golang", "vscode-go", id)
		if err != nil {
			return 0, err
		}
		if title == issue.GetTitle() {
			ctx.Printf("found existing releasing issue %v", id)
			return id, nil
		}
	}

	content := fmt.Sprintf(vscodeGOReleaseIssueTmplStr, version)
	issue, _, err := r.GitHub.CreateIssue(ctx, "golang", "vscode-go", &github.IssueRequest{
		Title:     &title,
		Body:      &content,
		Assignee:  github.String("h9jiang"),
		Milestone: &milestoneID,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create release tracking issue for %q: %w", version, err)
	}
	ctx.Printf("created releasing issue %v", *issue.Number)
	return *issue.Number, nil
}

// nextPrereleaseVersion determines the next pre-release version for the
// upcoming stable release of vscode-go by examining all existing tags in the
// repository.
//
// The versionBumpStrategy input indicates whether the pre-release should target
// the next minor or next patch version.
func (r *ReleaseVSCodeGoTasks) nextPrereleaseVersion(ctx *wf.TaskContext, versionBumpStrategy string) (semversion, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return semversion{}, err
	}

	semv := latestVersion(tags, isReleaseVersion, vsCodeGoStableVersion)
	switch versionBumpStrategy {
	case "next minor":
		semv.Minor += 2
		semv.Patch = 0
	case "next patch":
		semv.Patch += 1
	default:
		return semversion{}, fmt.Errorf("unknown version selection strategy: %q", versionBumpStrategy)
	}

	// latest to track the latest pre-release for the given semantic version.
	latest := 0
	for _, v := range tags {
		cur, ok := parseSemver(v)
		if !ok {
			continue
		}

		if cur.Pre == "" {
			continue
		}

		if cur.Major != semv.Major || cur.Minor != semv.Minor || cur.Patch != semv.Patch {
			continue
		}

		pre, err := cur.prereleaseVersion()
		if err != nil {
			continue
		}
		if pre > latest {
			latest = pre
		}
	}

	semv.Pre = fmt.Sprintf("rc.%v", latest+1)
	return semv, err
}

func vsCodeGoStableVersion(semv semversion) bool {
	return semv.Minor%2 == 0
}

func vsCodeGoInsiderVersion(semv semversion) bool {
	return semv.Minor%2 == 1
}

// isReleaseVersion reports whether semv is a release version.
// (in other words, not a prerelease).
func isReleaseVersion(semv semversion) bool {
	return semv.Pre == ""
}

// isPrereleaseVersion reports whether semv is a pre-release version.
// (in other words, not a release).
func isPrereleaseVersion(semv semversion) bool {
	return semv.Pre != ""
}

// latestVersion returns the latest version in the provided version list,
// considering only versions that match all the specified filters.
// Strings not following semantic versioning are ignored.
func latestVersion(versions []string, filters ...func(semversion) bool) semversion {
	latest := semversion{}
	for _, v := range versions {
		semv, ok := parseSemver(v)
		if !ok {
			continue
		}

		match := true
		for _, filter := range filters {
			if !filter(semv) {
				match = false
				break
			}
		}

		if !match {
			continue
		}

		if semv.Minor > latest.Minor {
			latest = semv
		}

		if semv.Minor == latest.Minor && semv.Patch > latest.Patch {
			latest = semv
		}
	}

	return latest
}

func (r *ReleaseVSCodeGoTasks) approveVersion(ctx *wf.TaskContext, semv semversion) error {
	ctx.Printf("The next release candidate will be v%v.%v.%v-%s", semv.Major, semv.Minor, semv.Patch, semv.Pre)
	return r.ApproveAction(ctx)
}
