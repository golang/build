// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/google/go-github/v48/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	"golang.org/x/build/internal/secret"
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
	Gerrit             GerritClient
	GitHub             GitHubClientInterface
	CloudBuild         CloudBuildClient
	ApproveAction      func(*wf.TaskContext) error
	SendMail           func(MailHeader, MailContent) error
	AnnounceMailHeader MailHeader
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

func vscodeGoReleaseBranch(release releaseVersion) string {
	return fmt.Sprintf("release-v%v.%v", release.Major, release.Minor)
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

	issue := wf.Task1(wd, "create release milestone and issue", r.createReleaseMilestoneAndIssue, release, wf.After(verified))
	branched := wf.Action2(wd, "create release branch", r.createReleaseBranch, release, prerelease, wf.After(verified))
	build := wf.Task3(wd, "generate package extension (.vsix) for release candidate", r.generatePackageExtension, release, prerelease, revision, wf.After(verified))

	tagged := wf.Action3(wd, "tag release candidate", r.tag, revision, release, prerelease, wf.After(branched))
	released := wf.Action3(wd, "create release note", r.createGitHubReleaseDraft, release, prerelease, build, wf.After(tagged))

	wf.Action4(wd, "mail announcement", r.mailPrereleaseAnnouncement, release, prerelease, revision, issue, wf.After(released))
	return wd
}

// findRevision determines the appropriate revision for the current release.
// Returns the head of the master branch if this is the first release candidate
// for a stable minor version (as no release branch exists yet).
// Returns the head of the corresponding release branch otherwise.
func (r *ReleaseVSCodeGoTasks) findRevision(ctx *wf.TaskContext, release releaseVersion, prerelease string) (string, error) {
	branch := vscodeGoReleaseBranch(release)
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
	}, nil)
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
go run tools/release/release.go package &> go-package-output.log
cat npm-output.log
cat go-package-output.log
`
		saveScript := cloudBuildClientScriptPrefix
		for _, file := range []string{"npm-output.log", "go-package-output.log", vsixFileName(release, prerelease)} {
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
				Script: fmt.Sprintf(packageScriptFmt, versionString(release, prerelease)),
				Dir:    "vscode-go/extension",
			},
			{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: saveScript,
				Dir:    "vscode-go/extension",
			},
		}
	}

	build, err := r.CloudBuild.RunCustomSteps(ctx, steps, nil)
	if err != nil {
		return CloudBuild{}, err
	}

	outputs, err := buildToOutputs(ctx, r.CloudBuild, build)
	if err != nil {
		return CloudBuild{}, err
	}

	ctx.Printf("the output from npm ci:\n%s\n", outputs["npm-output.log"])
	ctx.Printf("the output from package generation:\n%s\n", outputs["go-package-output.log"])

	return build, nil
}

// publishPackageExtension generate the package extension and publish to vscode
// marketplace.
func (r *ReleaseVSCodeGoTasks) publishPackageExtension(ctx *wf.TaskContext, release releaseVersion, build CloudBuild) error {
	// Publishing to the VSCode Marketplace requires manual retries instead of
	// automatic ones.
	ctx.DisableRetries()
	steps := func(resultURL string) []*cloudbuildpb.BuildStep {
		const publishScriptFmt = cloudBuildClientScriptPrefix + `
export TAG_NAME=%s

npm ci &> npm-output.log
go run tools/release/release.go publish &> go-publish-output.log
cat npm-output.log
cat go-publish-output.log
`
		versionString := release.String()
		saveScript := cloudBuildClientScriptPrefix
		for _, file := range []string{"npm-output.log", "go-publish-output.log", vsixFileName(release, "")} {
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
				// Copy the vsix files to the vscode-go/extension directory that is the
				// default directory "go run tools/release/release.go" expects vsix
				// files to publish.
				// TODO(hxjiang): write the vsix file to a separate gcs bucket and read
				// it from there.
				Name: "gcr.io/cloud-builders/gsutil",
				Args: []string{"cp", build.ResultURL + "/" + vsixFileName(release, ""), "."},
				Dir:  "vscode-go/extension",
			},
			{
				Name:      "gcr.io/cloud-builders/npm",
				Script:    fmt.Sprintf(publishScriptFmt, versionString),
				Dir:       "vscode-go/extension",
				SecretEnv: []string{"VSCE_PAT"},
			},
			{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: saveScript,
				Dir:    "vscode-go/extension",
			},
		}
	}

	build, err := r.CloudBuild.RunCustomSteps(ctx, steps, &CloudBuildOptions{
		AvailableSecrets: &cloudbuildpb.Secrets{
			SecretManager: []*cloudbuildpb.SecretManagerSecret{
				{
					VersionName: "projects/$PROJECT_ID/secrets/" + secret.NameVSCodeMarketplacePublishToken + "/versions/latest",
					Env:         "VSCE_PAT",
				},
			},
		},
	})
	if err != nil {
		return err
	}

	outputs, err := buildToOutputs(ctx, r.CloudBuild, build)
	if err != nil {
		return err
	}

	ctx.Printf("the output from npm ci:\n%s\n", outputs["npm-output.log"])
	ctx.Printf("the output from package generation:\n%s\n", outputs["go-package-output.log"])
	ctx.Printf("the output from package publication:\n%s\n", outputs["go-publish-output.log"])

	return nil
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

func vsixFileName(release releaseVersion, prerelease string) string {
	// The version inside of vsix does not have prefix "v".
	return fmt.Sprintf("go-%s.vsix", versionString(release, prerelease)[1:])
}

// vscodeGoReleaseData holds data for the "vscode-go" extension release notes
// template.
type vscodeGoReleaseData struct {
	// PreviousTag and CurrentTag are tags used to show the differences between
	// the current release and the previous one.
	PreviousTag string
	CurrentTag  string

	// Milestone is the tag containing issues for the current release.
	Milestone string

	// Body is the main content of the GitHub release notes after
	// the Git diff and milestone sections.
	Body string
}

//go:embed template/vscode-go-release-note.md
var vscodeGoReleaseNoteTmpl string

//go:embed template/vscode-go-prerelease-instructions.md
var vscodeGoPrereleaseInstallationInstructions string

// readChangeLogHeading function reads the CHANGELOG.md in vscode-go repo's
// master branch and finds the corresponding content under the heading.
func (r *ReleaseVSCodeGoTasks) readChangeLogHeading(ctx context.Context, heading string) (string, error) {
	head, err := r.Gerrit.ReadBranchHead(ctx, "vscode-go", "master")
	if err != nil {
		return "", err
	}
	source, err := r.Gerrit.ReadFile(ctx, "vscode-go", head, "CHANGELOG.md")
	if err != nil {
		return "", err
	}

	var output bytes.Buffer
	found := false

	reader := bufio.NewReader(bytes.NewReader(source))
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}

		// Reach to the next level 2 heading.
		if bytes.HasPrefix(line, []byte("## ")) && found {
			break
		}

		if !found && bytes.HasPrefix(line, []byte("## "+heading)) {
			found = true
			continue
		}

		if found {
			output.Write(line)
		}
	}

	return strings.TrimSpace(output.String()), nil
}

func (r *ReleaseVSCodeGoTasks) vscodeGoGitHubReleaseBody(ctx context.Context, release releaseVersion, prerelease string) (string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return "", err
	}

	// Release notes display the diff between the current release and:
	// - Stable minor version(vX.EVEN.0): Latest patch of the last stable minor
	//   (vX.EVEN-2.LATEST)
	// - Stable patch version(vX.Y.Z): Previous patch version (vX.Y.Z-1)
	// - Insider version(vX.ODD.Z): Last stable minor (vX.ODD-1.0-rc.1) - branch
	// 	 cut.
	var previousRelease releaseVersion
	var previousPrerelease string
	if isVSCodeGoInsiderVersion(release, prerelease) {
		previousRelease = releaseVersion{Major: release.Major, Minor: release.Minor - 1, Patch: 0}
		previousPrerelease = "rc.1"
	} else {
		if release.Patch == 0 {
			previousRelease, _ = latestVersion(tags, isSameMajorMinor(release.Major, release.Minor-2), isReleaseVersion)
		} else {
			previousRelease, _ = latestVersion(tags, isSameMajorMinor(release.Major, release.Minor), isReleaseVersion)
		}
	}

	current, previous := versionString(release, prerelease), versionString(previousRelease, previousPrerelease)
	data := vscodeGoReleaseData{
		CurrentTag:  current,
		PreviousTag: previous,
	}
	// Insider version.
	if isVSCodeGoInsiderVersion(release, prerelease) {
		// For insider version, the milestone will point to the next stable minor.
		// If the insider version is vX.ODD.Z, the milestone will be vX.ODD+1.0.
		data.Milestone = releaseVersion{Major: release.Major, Minor: release.Minor + 1, Patch: 0}.String()

		if release.Patch == 0 {
			// An insider minor version release (vX.ODD.0) is normally a dummy release
			// immediately after a stable minor version release (vX.ODD-1.0).
			// The body of the insider release will point to the corresponding stable
			// release for reference.
			const vscodeGoMinorInsiderFmt = `%s is a pre-release version identical to the official release %s, incorporating all the same bug fixes and improvements. This may include additional, experimental features that are not yet ready for general release. These features are still under development and may be subject to change or removal.

See release https://github.com/golang/vscode-go/releases/tag/%s for details.`
			data.Body = fmt.Sprintf(vscodeGoMinorInsiderFmt, current, previousRelease, previousRelease)
		} else {
			// An insider patch version release (vX.ODD.Z Z > 0) is built from master
			// branch. The GitHub release body will be copied from the CHANGELOG.md
			// in master branch under heading ## Unreleased.
			data.Body, err = r.readChangeLogHeading(ctx, "Unreleased")
			if err != nil {
				return "", err
			}
		}
	} else {
		data.Milestone = release.String()
		// Stable version prerelease.
		if prerelease != "" {
			// For prerelease, the main body of the release will contain the
			// instructions of installation.
			data.Body = vscodeGoPrereleaseInstallationInstructions
		} else if release.Patch == 0 {
			// The main body of the release will be copied from the CHANGELOG.md in
			// master branch under heading ## Unreleased.
			data.Body, err = r.readChangeLogHeading(ctx, "Unreleased")
			if err != nil {
				return "", err
			}
		}
	}

	// Remove any trailing spaces.
	data.Body = strings.TrimSpace(data.Body)

	tmpl, err := template.New("vscode release").Parse(vscodeGoReleaseNoteTmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (r *ReleaseVSCodeGoTasks) createGitHubReleaseDraft(ctx *wf.TaskContext, release releaseVersion, prerelease string, build CloudBuild) error {
	body, err := r.vscodeGoGitHubReleaseBody(ctx, release, prerelease)
	if err != nil {
		return err
	}

	versionString := versionString(release, prerelease)
	ctx.DisableRetries() // Beyond this point we want retries to be done manually, not automatically.
	draft, err := r.GitHub.CreateRelease(ctx, "golang", "vscode-go", &github.RepositoryRelease{
		TagName:    github.String(versionString),
		Name:       github.String("Release " + versionString),
		Body:       github.String(body),
		Prerelease: github.Bool(true),
		Draft:      github.Bool(true),
	})
	if err != nil {
		return err
	}
	ctx.Printf("Created the draft release note in %s", draft.GetHTMLURL())

	outFS, err := r.CloudBuild.ResultFS(ctx, build)
	if err != nil {
		return err
	}

	file, err := outFS.Open(vsixFileName(release, prerelease))
	if err != nil {
		return err
	}

	asset, err := r.GitHub.UploadReleaseAsset(ctx, "golang", "vscode-go", draft.GetID(), vsixFileName(release, prerelease), file)
	if err != nil {
		return err
	}

	ctx.Printf("Uploaded asset %s to release %v as asset ID %v", asset.GetName(), draft.GetID(), asset.GetID())
	return nil
}

type vscodeGoPrereleaseAnnouncement struct {
	Commit  string
	Issue   int
	Branch  string
	Version string
}

func (r *ReleaseVSCodeGoTasks) mailPrereleaseAnnouncement(ctx *wf.TaskContext, release releaseVersion, prerelease, revision string, issue int) error {
	announce := vscodeGoPrereleaseAnnouncement{
		Version: release.String() + "-" + prerelease,
		Branch:  vscodeGoReleaseBranch(release),
		Commit:  revision,
		Issue:   issue,
	}
	content, err := announcementMail(announce)
	if err != nil {
		return err
	}
	ctx.Printf("pre-announcement subject: %s\n\n", content.Subject)
	ctx.Printf("pre-announcement body HTML:\n%s\n", content.BodyHTML)
	ctx.Printf("pre-announcement body text:\n%s", content.BodyText)
	return r.SendMail(r.AnnounceMailHeader, content)
}

func versionString(release releaseVersion, prerelease string) string {
	version := release.String()
	if prerelease != "" {
		version += "-" + prerelease
	}
	return version
}

func (r *ReleaseVSCodeGoTasks) tag(ctx *wf.TaskContext, commit string, release releaseVersion, prerelease string) error {
	tag := versionString(release, prerelease)
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
	revision := wf.Task2(wd, "read the head of master branch", r.Gerrit.ReadBranchHead, wf.Const("vscode-go"), wf.Const("master"))
	approved := wf.Action2(wd, "await release coordinator's approval", r.approveInsiderVersion, release, revision)

	verified := wf.Action1(wd, "verify the determined commit", r.verifyTestResults, revision, wf.After(approved))
	build := wf.Task3(wd, "generate package extension (.vsix) from the commit", r.generatePackageExtension, release, wf.Const(""), revision, wf.After(verified))

	tagged := wf.Action3(wd, "tag the commit", r.tag, revision, release, wf.Const(""), wf.After(build))

	_ = wf.Action3(wd, "create release note", r.createGitHubReleaseDraft, release, wf.Const(""), build, wf.After(tagged))
	_ = wf.Action2(wd, "publish to vscode marketplace", r.publishPackageExtension, release, build, wf.After(tagged))

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
