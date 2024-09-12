// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/google/go-github/v48/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	"golang.org/x/build/internal/workflow"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
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
	CloudBuild    CloudBuildClient
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
var vscodeGoReleaseIssueTmplStr string

// vscodeGoActiveReleaseBranch returns the current active release branch in
// vscode-go project.
func vscodeGoActiveReleaseBranch(ctx *wf.TaskContext, gerrit GerritClient) (string, error) {
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

	release := wf.Task1(wd, "determine the release version", r.determineReleaseVersion, versionBumpStrategy)
	prerelease := wf.Task1(wd, "find the next pre-release version", r.nextPrereleaseVersion, release)
	revision := wf.Task2(wd, "find the revision for the pre-release version", r.findRevision, release, prerelease)
	approved := wf.Action2(wd, "await release coordinator's approval", r.approvePrereleaseVersion, release, prerelease)

	verified := wf.Action1(wd, "verify the release candidate", r.verifyTestResults, revision, wf.After(approved))

	_ = wf.Task1(wd, "create release milestone and issue", r.createReleaseMilestoneAndIssue, release, wf.After(verified))
	branched := wf.Action2(wd, "create release branch", r.createReleaseBranch, release, prerelease, wf.After(verified))
	_ = wf.Task3(wd, "generate package extension (.vsix) for release candidate", r.generatePackageExtension, release, prerelease, revision, wf.After(verified))

	_ = wf.Action3(wd, "tag release candidate", r.tag, revision, release, prerelease, wf.After(branched))

	return wd
}

// findRevision determines the appropriate revision for the current release.
// Returns the head of the master branch if this is the first release candidate
// for a stable minor version (as no release branch exists yet).
// Returns the head of the corresponding release branch otherwise.
func (r *ReleaseVSCodeGoTasks) findRevision(ctx *wf.TaskContext, release releaseVersion, prerelease string) (string, error) {
	branch := fmt.Sprintf("release-v%v.%v", release.Major, release.Minor)
	if release.Patch == 0 && prerelease == "rc.1" {
		branch = "master"
	}

	return r.Gerrit.ReadBranchHead(ctx, "vscode-go", branch)
}

func (r *ReleaseVSCodeGoTasks) verifyTestResults(ctx *wf.TaskContext, revision string) error {
	// We are running all tests in a docker as a user 'node' (uid: 1000)
	// Let the user own the directory.
	testScript := `#!/usr/bin/env bash
set -eux
set -o pipefail

chown -R 1000:1000 .
./build/all.bash testlocal &> output.log
`

	build, err := r.CloudBuild.RunCustomSteps(ctx, func(resultURL string) []*cloudbuildpb.BuildStep {
		return []*cloudbuildpb.BuildStep{
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"clone", "https://go.googlesource.com/vscode-go", "vscode-go"},
			},
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"checkout", revision},
				Dir:  "vscode-go",
			},
			{
				Name:   "gcr.io/cloud-builders/docker",
				Script: testScript,
				Dir:    "vscode-go",
			},
			{
				Name: "gcr.io/cloud-builders/gsutil",
				Args: []string{"cp", "output.log", fmt.Sprintf("%s/output.log", resultURL)},
				Dir:  "vscode-go",
			},
		}
	})
	if err != nil {
		return err
	}

	outputs, err := buildToOutputs(ctx, r.CloudBuild, build)
	if err != nil {
		return err
	}

	ctx.Printf("the output from test run:\n%s\n", outputs["output.log"])
	return nil
}

func (r *ReleaseVSCodeGoTasks) createReleaseMilestoneAndIssue(ctx *wf.TaskContext, semv releaseVersion) (int, error) {
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

	content := fmt.Sprintf(vscodeGoReleaseIssueTmplStr, version)
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

// createReleaseBranch creates corresponding release branch only for the initial
// release candidate of a minor version.
func (r *ReleaseVSCodeGoTasks) createReleaseBranch(ctx *wf.TaskContext, release releaseVersion, prerelease string) error {
	branch := fmt.Sprintf("release-v%v.%v", release.Major, release.Minor)
	releaseHead, err := r.Gerrit.ReadBranchHead(ctx, "vscode-go", branch)

	if err == nil {
		ctx.Printf("Found the release branch %q with head pointing to %s\n", branch, releaseHead)
		return nil
	}

	if !errors.Is(err, gerrit.ErrResourceNotExist) {
		return fmt.Errorf("failed to read the release branch: %w", err)
	}

	// Require vscode release branch existence if this is a non-minor release.
	if release.Patch != 0 {
		return fmt.Errorf("release branch is required for patch releases: %w", err)
	}

	rc, err := prereleaseNumber(prerelease)
	if err != nil {
		return err
	}

	// Require vscode release branch existence if this is not the first rc in
	// a minor release.
	if rc != 1 {
		return fmt.Errorf("release branch is required for non-initial release candidates: %w", err)
	}

	// Create the release branch using the revision from the head of master branch.
	head, err := r.Gerrit.ReadBranchHead(ctx, "vscode-go", "master")
	if err != nil {
		return err
	}

	ctx.DisableRetries() // Beyond this point we want retries to be done manually, not automatically.
	_, err = r.Gerrit.CreateBranch(ctx, "vscode-go", branch, gerrit.BranchInput{Revision: head})
	if err != nil {
		return err
	}
	ctx.Printf("Created branch %q at revision %s.\n", branch, head)
	return nil
}

func (r *ReleaseVSCodeGoTasks) generatePackageExtension(ctx *wf.TaskContext, release releaseVersion, prerelease, revision string) (CloudBuild, error) {
	steps := func(resultURL string) []*cloudbuildpb.BuildStep {
		const packageScriptFmt = cloudBuildClientScriptPrefix + `
export TAG_NAME=%s

npm ci &> npm-output.log
go run tools/release/release.go package &> go-output.log
cat npm-output.log
cat go-output.log
`

		versionString := release.String()
		if prerelease != "" {
			versionString += "-" + prerelease
		}
		// The version inside of vsix does not have prefix "v".
		vsix := fmt.Sprintf("go-%s.vsix", versionString[1:])

		saveScript := cloudBuildClientScriptPrefix
		for _, file := range []string{"npm-output.log", "go-output.log", vsix} {
			saveScript += fmt.Sprintf("gsutil cp %s %s/%s\n", file, resultURL, file)
		}
		return []*cloudbuildpb.BuildStep{
			{
				Name:   "bash",
				Script: cloudBuildClientDownloadGoScript,
			},
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"clone", "https://go.googlesource.com/vscode-go", "vscode-go"},
			},
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"checkout", revision},
				Dir:  "vscode-go",
			},
			{
				Name:   "gcr.io/cloud-builders/npm",
				Script: fmt.Sprintf(packageScriptFmt, versionString),
				Dir:    "vscode-go/extension",
			},
			{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: saveScript,
				Dir:    "vscode-go/extension",
			},
		}
	}

	build, err := r.CloudBuild.RunCustomSteps(ctx, steps)
	if err != nil {
		return CloudBuild{}, err
	}

	outputs, err := buildToOutputs(ctx, r.CloudBuild, build)
	if err != nil {
		return CloudBuild{}, err
	}

	ctx.Printf("the output from npm ci:\n%s\n", outputs["npm-output.log"])
	ctx.Printf("the output from package generation:\n%s\n", outputs["go-output.log"])

	return build, nil
}

// determineReleaseVersion determines the release version for the upcoming
// stable release of vscode-go by examining all existing tags in the repository.
//
// The versionBumpStrategy input indicates whether the pre-release should target
// the next minor or next patch version.
func (r *ReleaseVSCodeGoTasks) determineReleaseVersion(ctx *wf.TaskContext, versionBumpStrategy string) (releaseVersion, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return releaseVersion{}, err
	}

	release, _ := latestVersion(tags, isReleaseVersion, isVSCodeGoStableVersion)
	switch versionBumpStrategy {
	case "next minor":
		release.Minor += 2
		release.Patch = 0
	case "next patch":
		release.Patch += 1
	default:
		return releaseVersion{}, fmt.Errorf("unknown version selection strategy: %q", versionBumpStrategy)
	}
	return release, err
}

// nextPrereleaseVersion inspects the tags in vscode-go repo that match with the
// given version and finds the next pre-release version.
func (r *ReleaseVSCodeGoTasks) nextPrereleaseVersion(ctx *wf.TaskContext, release releaseVersion) (string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return "", err
	}

	_, prerelease := latestVersion(tags, isSameReleaseVersion(release), isPrereleaseMatchRegex(`^rc\.\d+$`))
	if prerelease == "" {
		return "rc.1", nil
	}

	pre, err := prereleaseNumber(prerelease)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("rc.%v", pre+1), nil
}

func (r *ReleaseVSCodeGoTasks) tag(ctx *wf.TaskContext, commit string, release releaseVersion, prerelease string) error {
	tag := fmt.Sprintf("%s-%s", release, prerelease)
	if err := r.Gerrit.Tag(ctx, "vscode-go", tag, commit); err != nil {
		return err
	}
	ctx.Printf("tagged commit %s with tag %s", commit, tag)
	return nil
}

// NewInsiderDefinition create a new workflow definition for vscode-go insider
// version release.
func (r *ReleaseVSCodeGoTasks) NewInsiderDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	release := wf.Task0(wd, "determine the insider version", r.determineInsiderVersion)
	commit := wf.Task2(wd, "read the head of master branch", r.Gerrit.ReadBranchHead, wf.Const("vscode-go"), wf.Const("master"))

	_ = wf.Action2(wd, "await release coordinator's approval", r.approveInsiderVersion, release, commit)

	return wd
}

// determineInsiderVersion determines the release version for the upcoming
// insider release of vscode-go by examining all existing tags in the repository.
func (r *ReleaseVSCodeGoTasks) determineInsiderVersion(ctx *wf.TaskContext) (releaseVersion, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return releaseVersion{}, err
	}

	// The insider version must be higher than the latest stable version to be
	// recognized by the marketplace.
	// VSCode automatically updates extensions to the highest available version.
	// https://code.visualstudio.com/api/working-with-extensions/publishing-extension#prerelease-extensions
	release, _ := latestVersion(tags, isReleaseVersion, isVSCodeGoStableVersion)
	major := release.Major
	minor := release.Minor + 1

	insider, _ := latestVersion(tags, isReleaseVersion, isSameMajorMinor(major, minor))
	if insider == (releaseVersion{}) {
		return releaseVersion{Major: major, Minor: minor, Patch: 0}, nil
	}
	insider.Patch += 1
	return insider, nil
}

func isVSCodeGoStableVersion(release releaseVersion, _ string) bool {
	return release.Minor%2 == 0
}

func isVSCodeGoInsiderVersion(release releaseVersion, _ string) bool {
	return release.Minor%2 == 1
}

// isReleaseVersion reports whether input version is a release version.
// (in other words, not a prerelease).
func isReleaseVersion(_ releaseVersion, prerelease string) bool {
	return prerelease == ""
}

// isPrereleaseVersion reports whether input version is a pre-release version.
// (in other words, not a release).
func isPrereleaseVersion(_ releaseVersion, prerelease string) bool {
	return prerelease != ""
}

// isPrereleaseMatchRegex reports whether the pre-release string of the input
// version matches the regex expression.
func isPrereleaseMatchRegex(regex string) func(releaseVersion, string) bool {
	return func(_ releaseVersion, prerelease string) bool {
		if prerelease == "" {
			return false
		}
		matched, err := regexp.MatchString(regex, prerelease)
		if err != nil {
			return false
		}
		return matched
	}
}

// isSameReleaseVersion reports whether the version string have the same release
// version(same major minor and patch) as input.
func isSameReleaseVersion(want releaseVersion) func(releaseVersion, string) bool {
	return func(got releaseVersion, _ string) bool {
		return got == want
	}
}

func isSameMajorMinor(major, minor int) func(releaseVersion, string) bool {
	return func(got releaseVersion, _ string) bool {
		return got.Major == major && got.Minor == minor
	}
}

// latestVersion returns the releaseVersion and the prerelease tag of the latest
// version from the provided version strings.
// It considers only versions that are valid and match all the filters.
func latestVersion(versions []string, filters ...func(releaseVersion, string) bool) (releaseVersion, string) {
	latest := ""
	latestRelease := releaseVersion{}
	latestPre := ""
	for _, v := range versions {
		release, prerelease, ok := parseVersion(v)
		if !ok {
			continue
		}

		match := true
		for _, filter := range filters {
			if !filter(release, prerelease) {
				match = false
				break
			}
		}

		if !match {
			continue
		}

		if semver.Compare(v, latest) == 1 {
			latest = v
			latestRelease = release
			latestPre = prerelease
		}
	}

	return latestRelease, latestPre
}

func (r *ReleaseVSCodeGoTasks) approvePrereleaseVersion(ctx *wf.TaskContext, release releaseVersion, prerelease string) error {
	ctx.Printf("The next release candidate will be %s-%s", release, prerelease)
	return r.ApproveAction(ctx)
}

func (r *ReleaseVSCodeGoTasks) approveInsiderVersion(ctx *wf.TaskContext, release releaseVersion, commit string) error {
	// The insider version is picked from the actively developed master branch.
	// The commit information is essential for the release coordinator.
	ctx.Printf("The insider version v%v.%v.%v will released based on commit %s", release.Major, release.Minor, release.Patch, commit)
	ctx.Printf("See commit detail: https://go.googlesource.com/vscode-go/+/%s", commit)
	return r.ApproveAction(ctx)
}
