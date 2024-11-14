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
	"time"

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

//go:embed template/vscode-go-changelog-entry.md
var vscodeGoChangelogEntryTmplStr string

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
	coordinators := wf.Param(wd, releaseCoordinatorsParam)

	release := wf.Task1(wd, "determine the release version", r.determineReleaseVersion, versionBumpStrategy)
	prerelease := wf.Task1(wd, "find the next pre-release version", r.nextPrereleaseVersion, release)
	approved := wf.Action2(wd, "await release coordinator's approval", r.approveStablePrerelease, release, prerelease)

	branch := wf.Task2(wd, "create release branch", r.createReleaseBranch, release, prerelease, wf.After(approved))

	changeID := wf.Task2(wd, "update package.json in release branch", r.updatePackageJSONVersionInReleaseBranch, release, coordinators, wf.After(branch))
	submitted := wf.Task1(wd, "await package.json CL submission", clAwaiter{r.Gerrit}.awaitSubmission, changeID)

	// Read the head of the release branch after the required CL submission.
	revision := wf.Task2(wd, "find the revision for the pre-release version", r.Gerrit.ReadBranchHead, wf.Const("vscode-go"), branch, wf.After(submitted))
	verified := wf.Action1(wd, "verify the release candidate", r.verifyTestResults, revision)

	issue := wf.Task2(wd, "create release milestone and issue", r.createReleaseMilestoneAndIssue, release, coordinators, wf.After(verified))
	build := wf.Task3(wd, "generate package extension (.vsix) for release candidate", r.generatePackageExtension, release, prerelease, revision, wf.After(verified))

	tagged := wf.Action3(wd, "tag the release candidate", r.tag, revision, release, prerelease, wf.After(branch))
	released := wf.Action3(wd, "create release note", r.createGitHubReleaseDraft, release, prerelease, build, wf.After(tagged))

	wf.Action4(wd, "mail announcement", r.mailAnnouncement, release, prerelease, revision, issue, wf.After(released))
	return wd
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

func (r *ReleaseVSCodeGoTasks) createReleaseMilestoneAndIssue(ctx *wf.TaskContext, semv releaseVersion, coordinators []string) (int, error) {
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

	if len(coordinators) == 0 {
		return 0, fmt.Errorf("the input coordinators slice is empty")
	}
	assignee, err := lookupCoordinator(coordinators[0])
	if err != nil {
		return 0, fmt.Errorf("failed to find the coordinator %q", coordinators[0])
	}
	issue, _, err := r.GitHub.CreateIssue(ctx, "golang", "vscode-go", &github.IssueRequest{
		Title:     github.String(title),
		Body:      github.String(fmt.Sprintf(vscodeGoReleaseIssueTmplStr, version)),
		Assignee:  github.String(assignee.GitHub),
		Milestone: github.Int(milestoneID),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create release tracking issue for %q: %w", version, err)
	}
	ctx.Printf("created releasing issue %v", *issue.Number)
	return *issue.Number, nil
}

// createReleaseBranch creates corresponding release branch only for the initial
// release candidate of a minor version.
func (r *ReleaseVSCodeGoTasks) createReleaseBranch(ctx *wf.TaskContext, release releaseVersion, prerelease string) (string, error) {
	branch := fmt.Sprintf("release-v%v.%v", release.Major, release.Minor)
	releaseHead, err := r.Gerrit.ReadBranchHead(ctx, "vscode-go", branch)

	if err == nil {
		ctx.Printf("Found the release branch %q with head pointing to %s\n", branch, releaseHead)
		return branch, nil
	}

	if !errors.Is(err, gerrit.ErrResourceNotExist) {
		return "", fmt.Errorf("failed to read the release branch: %w", err)
	}

	// Require vscode release branch existence if this is a non-minor release.
	if release.Patch != 0 {
		return "", fmt.Errorf("release branch is required for patch releases: %w", err)
	}

	rc, err := prereleaseNumber(prerelease)
	if err != nil {
		return "", err
	}

	// Require vscode release branch existence if this is not the first rc in
	// a minor release.
	if rc != 1 {
		return "", fmt.Errorf("release branch is required for non-initial release candidates: %w", err)
	}

	// Create the release branch using the revision from the head of master branch.
	head, err := r.Gerrit.ReadBranchHead(ctx, "vscode-go", "master")
	if err != nil {
		return "", err
	}

	ctx.DisableRetries() // Beyond this point we want retries to be done manually, not automatically.
	_, err = r.Gerrit.CreateBranch(ctx, "vscode-go", branch, gerrit.BranchInput{Revision: head})
	if err != nil {
		return "", err
	}
	ctx.Printf("Created branch %q at revision %s.\n", branch, head)
	return branch, nil
}

// generatePackageExtension builds the vscode-go package extension from source.
//
// Uses the 'revision' parameter to determine the commit to build.
// Uses the 'release' and 'prerelease' to determine the output file name.
//
// Returns a CloudBuild struct with information about the built package
// extension in Google Cloud Storage (GCS).
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
	Date string

	// PreviousTag and CurrentTag are tags used to show the differences between
	// the current release and the previous one.
	PreviousTag string
	CurrentTag  string

	// Milestone is the tag containing issues for the current release.
	Milestone string

	// NextStable is the next stable version. Only used if the current release is
	// insider release.
	NextStable string

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
	baseRelease, basePrerelease, err := r.diffBaseVersion(ctx, release)
	if err != nil {
		return "", err
	}

	var data vscodeGoReleaseData
	current, previous := versionString(release, prerelease), versionString(baseRelease, basePrerelease)
	// Insider version.
	if isVSCodeGoInsiderVersion(release, prerelease) {
		var body string
		if release.Patch == 0 {
			// An insider minor version release (vX.ODD.0) is normally a dummy release
			// immediately after a stable minor version release (vX.ODD-1.0).
			// The body of the insider release will point to the corresponding stable
			// release for reference.
			const vscodeGoMinorInsiderFmt = `%s is a pre-release version identical to the official release %s, incorporating all the same bug fixes and improvements. This may include additional, experimental features that are not yet ready for general release. These features are still under development and may be subject to change or removal.

See release https://github.com/golang/vscode-go/releases/tag/%s for details.`
			body = fmt.Sprintf(vscodeGoMinorInsiderFmt, current, baseRelease, baseRelease)
		} else {
			// An insider patch version release (vX.ODD.Z Z > 0) is built from master
			// branch. The GitHub release body will be copied from the CHANGELOG.md
			// in master branch under heading ## Unreleased.
			body, err = r.readChangeLogHeading(ctx, "Unreleased")
			if err != nil {
				return "", err
			}
		}

		data = vscodeGoReleaseData{
			Date:        time.Now().Format(time.DateOnly),
			CurrentTag:  current,
			PreviousTag: previous,
			Milestone:   vscodeGoMilestone(release),
			NextStable:  releaseVersion{Major: release.Major, Minor: release.Minor + 1, Patch: 0}.String(),
			Body:        strings.TrimSpace(body),
		}
	} else {
		var body string
		if prerelease != "" {
			// For prerelease, the main body of the release will contain the
			// instructions of installation.
			body = vscodeGoPrereleaseInstallationInstructions
		} else if release.Patch == 0 {
			// The main body of the release will be copied from the CHANGELOG.md in
			// master branch under heading ## Unreleased.
			body, err = r.readChangeLogHeading(ctx, "Unreleased")
			if err != nil {
				return "", err
			}
		}

		data = vscodeGoReleaseData{
			Date:        time.Now().Format(time.DateOnly),
			CurrentTag:  current,
			PreviousTag: previous,
			Milestone:   release.String(),
			Body:        strings.TrimSpace(body),
		}
	}

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
		TagName: github.String(versionString),
		Name:    github.String("Release " + versionString),
		Body:    github.String(body),
		// Both insider and release candidate are considered as prerelease.
		Prerelease: github.Bool(isVSCodeGoInsiderVersion(release, prerelease) || prerelease != ""),
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

type vscodeGoReleaseAnnouncement struct {
	Version string
}

type vscodeGoInsiderAnnouncement struct {
	Commit        string
	Version       string
	StableVersion string
}

func (r *ReleaseVSCodeGoTasks) mailAnnouncement(ctx *wf.TaskContext, release releaseVersion, prerelease, revision string, issue int) error {
	var announce any
	if prerelease != "" {
		announce = vscodeGoPrereleaseAnnouncement{
			Version: versionString(release, prerelease),
			Branch:  vscodeGoReleaseBranch(release),
			Commit:  revision,
			Issue:   issue,
		}
	} else if isVSCodeGoInsiderVersion(release, prerelease) {
		announce = vscodeGoInsiderAnnouncement{
			Version:       release.String(),
			Commit:        revision,
			StableVersion: releaseVersion{Major: release.Major, Minor: release.Minor + 1, Patch: 0}.String(),
		}
	} else {
		announce = vscodeGoReleaseAnnouncement{
			Version: release.String(),
		}
	}

	content, err := announcementMail(announce)
	if err != nil {
		return err
	}
	ctx.Printf("announcement subject: %s\n\n", content.Subject)
	ctx.Printf("announcement body HTML:\n%s\n", content.BodyHTML)
	ctx.Printf("announcement body text:\n%s", content.BodyText)
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

	reviewers := wf.Param(wd, reviewersParam)

	packageChangeID := wf.Task1(wd, "update package.json in master branch", r.updatePackageJSONVersionInMasterBranch, reviewers)
	packageSubmitted := wf.Task1(wd, "await package.json CL submission", clAwaiter{r.Gerrit}.awaitSubmission, packageChangeID)

	release := wf.Task0(wd, "determine the insider version", r.determineInsiderVersion)
	revision := wf.Task2(wd, "read the head of master branch", r.Gerrit.ReadBranchHead, wf.Const("vscode-go"), wf.Const("master"), wf.After(packageSubmitted))
	approved := wf.Action2(wd, "await release coordinator's approval", r.approveInsiderRelease, release, revision)

	verified := wf.Action1(wd, "verify the determined commit", r.verifyTestResults, revision, wf.After(approved))
	build := wf.Task3(wd, "generate package extension (.vsix) from the commit", r.generatePackageExtension, release, wf.Const(""), revision, wf.After(verified))

	tagged := wf.Action3(wd, "tag the insider release", r.tag, revision, release, wf.Const(""), wf.After(build))

	released := wf.Action3(wd, "create release note", r.createGitHubReleaseDraft, release, wf.Const(""), build, wf.After(tagged))

	changelogChangeID := wf.Task2(wd, "update CHANGELOG.md in the master branch", r.addChangeLog, release, reviewers, wf.After(tagged))
	changelogSubmitted := wf.Task1(wd, "await CHANGELOG.md CL submission", clAwaiter{r.Gerrit}.awaitSubmission, changelogChangeID)
	// Publish only after the CHANGELOG.md update is merged to ensure the change
	// log reflects the latest released version.
	published := wf.Action2(wd, "publish to vscode marketplace", r.publishPackageExtension, release, build, wf.After(changelogSubmitted))

	wf.Action4(wd, "mail announcement", r.mailAnnouncement, release, wf.Const(""), revision, wf.Const(0), wf.After(released), wf.After(published))
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

// updatePackageJSONVersionInReleaseBranch updates the "package.json" and
// "package-lock.json" files in the release branch of the vscode-go repository.
// The "package.json" and "package-lock.json" will reference the current release
// version.
func (r *ReleaseVSCodeGoTasks) updatePackageJSONVersionInReleaseBranch(ctx *wf.TaskContext, release releaseVersion, reviewers []string) (string, error) {
	return r.updatePackageJSONVersion(ctx, vscodeGoReleaseBranch(release), release.String()[1:], reviewers)
}

// updatePackageJSONVersionInMasterBranch updates the "package.json" and
// "package-lock.json" files in the master branch of the vscode-go repository.
// The updated "package.json" and "package-lock.json" will reference the next
// stable release version with special suffix "-dev" to indicate this is a
// prerelease.
func (r *ReleaseVSCodeGoTasks) updatePackageJSONVersionInMasterBranch(ctx *wf.TaskContext, reviewers []string) (string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return "", err
	}

	latestStable, _ := latestVersion(tags, isReleaseVersion, isVSCodeGoStableVersion)
	if latestStable == (releaseVersion{}) {
		return "", fmt.Errorf("no released stable version in vscode-go")
	}

	// "package.json" in master branch should point to the next vscode-go stable
	// version with suffix "-dev". The version inside of package.json does not
	// have the prefix "v".
	// If the latest released stable version is v0.44.2, the package.json should
	// point to "0.46.0-dev".
	devVersion := versionString(releaseVersion{Major: latestStable.Major, Minor: latestStable.Minor + 2, Patch: 0}, "dev")[1:]

	return r.updatePackageJSONVersion(ctx, "master", devVersion, reviewers)
}

// updatePackageJSONVersion updates the "package.json" and "package-lock.json"
// files in the given branch of the vscode-go repository with the desired version.
func (r *ReleaseVSCodeGoTasks) updatePackageJSONVersion(ctx *wf.TaskContext, branch string, version string, reviewers []string) (string, error) {
	clTitle := "extension/package.json: update version to " + version
	if branch != "master" {
		clTitle = "[" + branch + "]" + clTitle
	}
	openCL, err := openCL(ctx, r.Gerrit, "vscode-go", branch, clTitle)
	if err != nil {
		return "", fmt.Errorf("failed to find the open CL of title %q in branch %q: %w", clTitle, branch, err)
	}
	if openCL != "" {
		ctx.Printf("not creating CL: found existing CL %s", openCL)
		return openCL, nil
	}

	steps := func(resultURL string) []*cloudbuildpb.BuildStep {
		script := fmt.Sprintf(cloudBuildClientScriptPrefix+`
# Make a copy of interested files.
cp package.json package.json.before
cp package-lock.json package-lock.json.before

npm ci &> npm-output.log
npx vsce package %s --no-git-tag-version &> npx-output.log
cat npm-output.log
cat npx-output.log
`, version)

		saveScriptFmt := cloudBuildClientScriptPrefix
		for _, file := range []string{
			"package.json",
			"package-lock.json",
			"package.json.before",
			"package-lock.json.before",
			"npx-output.log",
			"npm-output.log",
		} {
			saveScriptFmt += fmt.Sprintf("gsutil cp %[1]s %[2]s/%[1]s\n", file, resultURL)
		}

		return []*cloudbuildpb.BuildStep{
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"clone", "https://go.googlesource.com/vscode-go", "vscode-go"},
			},
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"checkout", branch},
				Dir:  "vscode-go",
			},
			{
				Name:   "gcr.io/cloud-builders/npm",
				Script: script,
				Dir:    "vscode-go/extension",
			},
			{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: saveScriptFmt,
				Dir:    "vscode-go/extension",
			},
		}
	}

	build, err := r.CloudBuild.RunCustomSteps(ctx, steps, nil)
	if err != nil {
		return "", err
	}

	outputs, err := buildToOutputs(ctx, r.CloudBuild, build)
	if err != nil {
		return "", err
	}

	ctx.Printf("the output from npm ci:\n%s\n", outputs["npm-output.log"])
	ctx.Printf("the output from npx package:\n%s\n", outputs["npx-output.log"])

	changed := map[string]string{}
	// "npx vsce package" generate "package.json" with an offending trailing
	// new line. Remove this new line before creating the CL.
	if after := strings.TrimRight(outputs["package.json"], "\n"); after != outputs["package.json.before"] {
		changed["extension/package.json"] = after
	}
	if after := outputs["package-lock.json"]; after != outputs["package-lock.json.before"] {
		changed["extension/package-lock.json"] = after
	}

	// Skip CL creation as nothing changed.
	if len(changed) == 0 {
		ctx.Printf("package.json and package-lock.json is already up-to-date")
		return "", nil
	}

	changeID, err := r.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "vscode-go",
		Branch:  branch,
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the package.json and package-lock.json.\n", clTitle),
	}, reviewers, changed)
	if err != nil {
		return "", err
	}

	ctx.Printf("created auto-submit change %s under branch master in vscode-go repo.", changeID)
	return changeID, nil
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

func (r *ReleaseVSCodeGoTasks) approveStablePrerelease(ctx *wf.TaskContext, release releaseVersion, prerelease string) error {
	ctx.Printf("The next release candidate will be %s", versionString(release, prerelease))
	return r.ApproveAction(ctx)
}

func (r *ReleaseVSCodeGoTasks) approveInsiderRelease(ctx *wf.TaskContext, release releaseVersion, commit string) error {
	// The insider version is picked from the actively developed master branch.
	// The commit information is essential for the release coordinator.
	ctx.Printf("The insider version %s will released based on commit %s", release, commit)
	ctx.Printf("See commit detail: https://go.googlesource.com/vscode-go/+/%s", commit)
	return r.ApproveAction(ctx)
}

func (r *ReleaseVSCodeGoTasks) approveStableRelease(ctx *wf.TaskContext, release releaseVersion, prerelease string) error {
	ctx.Printf("The release candidate %s will be released as %s", versionString(release, prerelease), release)
	ctx.Printf("See release candidate detail: https://go.googlesource.com/vscode-go/+/refs/tags/%s", versionString(release, prerelease))
	return r.ApproveAction(ctx)
}

// NewReleaseDefinition creates a new workflow definition for vscode-go stable
// version release.
func (r *ReleaseVSCodeGoTasks) NewReleaseDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	versionBumpStrategy := wf.Param(wd, nextVersionParam)
	reviewers := wf.Param(wd, reviewersParam)

	release := wf.Task1(wd, "determine the release version", r.determineReleaseVersion, versionBumpStrategy)
	prerelease := wf.Task1(wd, "find the latest pre-release version", r.latestPrereleaseVersion, release)

	approved := wf.Action2(wd, "await release coordinator's approval", r.approveStableRelease, release, prerelease)

	commit := wf.Task2(wd, "find the commit for the release candidate", r.findVSCodeReleaseCommit, release, prerelease, wf.After(approved))
	// Skip test result verification because it was already executed in the
	// prerelease flow.
	build := wf.Task3(wd, "generate package extension (.vsix) from release candidate tag", r.generatePackageExtension, release, wf.Const(""), commit)

	tagged := wf.Action3(wd, "tag the stable release", r.tag, commit, release, wf.Const(""), wf.After(build))
	released := wf.Action3(wd, "create release note", r.createGitHubReleaseDraft, release, wf.Const(""), build, wf.After(tagged))

	changeID := wf.Task2(wd, "update CHANGELOG.md in the master branch", r.addChangeLog, release, reviewers, wf.After(build))
	submitted := wf.Task1(wd, "await CHANGELOG.md CL submission", clAwaiter{r.Gerrit}.awaitSubmission, changeID)
	// Publish only after the CHANGELOG.md update is merged to ensure the change
	// log reflects the latest released version.
	published := wf.Action2(wd, "publish to vscode marketplace", r.publishPackageExtension, release, build, wf.After(submitted))

	wf.Action4(wd, "mail announcement", r.mailAnnouncement, release, wf.Const(""), wf.Const(""), wf.Const(0), wf.After(released), wf.After(published))
	return wd
}

// latestPrereleaseVersion inspects the tags in vscode-go repo that match with
// the given version and finds the latest pre-release version.
func (r *ReleaseVSCodeGoTasks) latestPrereleaseVersion(ctx *wf.TaskContext, release releaseVersion) (string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return "", err
	}

	_, prerelease := latestVersion(tags, isSameReleaseVersion(release), isPrereleaseMatchRegex(`^rc\.\d+$`))
	if prerelease == "" {
		return "", fmt.Errorf("could not find any release candidate for version %s", release)
	}

	return prerelease, nil
}

// findVSCodeReleaseCommit returns commit for the VS Code release candidate.
func (r *ReleaseVSCodeGoTasks) findVSCodeReleaseCommit(ctx *wf.TaskContext, release releaseVersion, prerelease string) (string, error) {
	info, err := r.Gerrit.GetTag(ctx, "vscode-go", versionString(release, prerelease))
	if err != nil {
		return "", err
	}

	return info.Revision, nil
}

// diffBaseVersion determines the appropriate base version for generating a
// commit diff against the given input version.
// The base version is selected as follows:
//   - Stable minor version(vX.EVEN.0): Latest patch of the last stable minor
//     (vX.EVEN-2.LATEST).
//   - Stable patch version(vX.Y.Z): Previous patch version (vX.Y.Z-1).
//   - Insider version(vX.ODD.Z): Last stable minor (vX.ODD-1.0-rc.1) - branch cut.
func (r *ReleaseVSCodeGoTasks) diffBaseVersion(ctx context.Context, release releaseVersion) (releaseVersion, string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "vscode-go")
	if err != nil {
		return releaseVersion{}, "", err
	}

	var previousRelease releaseVersion
	var previousPrerelease string
	if isVSCodeGoInsiderVersion(release, "") {
		previousRelease = releaseVersion{Major: release.Major, Minor: release.Minor - 1, Patch: 0}
		previousPrerelease = "rc.1"
	} else {
		if release.Patch == 0 {
			previousRelease, _ = latestVersion(tags, isSameMajorMinor(release.Major, release.Minor-2), isReleaseVersion)
		} else {
			previousRelease, _ = latestVersion(tags, isSameMajorMinor(release.Major, release.Minor), isReleaseVersion)
		}
	}
	return previousRelease, previousPrerelease, nil
}

// milestoneForVersion returns the name of the github milestone associated with
// a given version.
func vscodeGoMilestone(release releaseVersion) string {
	if isVSCodeGoInsiderVersion(release, "") {
		// For insider version, the milestone will point to the next stable minor.
		// If the insider version is vX.ODD.Z, the milestone will be vX.ODD+1.0.
		return releaseVersion{Major: release.Major, Minor: release.Minor + 1, Patch: 0}.String()
	}
	return release.String()
}

// addChangeLog updates the CHANGELOG.md file in the master branch of the
// vscode-go repository with a new heading for the released version.
// For stable minor version, it moves all content from the "Unreleased" section
// to the new version's section.
// For stable patch version and insider version, add a new heading with
// pre-defined content.
// For more details on changelog format, see: https://keepachangelog.com/en/1.1.0/
func (r *ReleaseVSCodeGoTasks) addChangeLog(ctx *wf.TaskContext, release releaseVersion, reviewers []string) (string, error) {
	clTitle := "CHANGELOG.md: add release heading for " + release.String()

	openCL, err := openCL(ctx, r.Gerrit, "vscode-go", "master", clTitle)
	if err != nil {
		return "", err
	}
	if openCL != "" {
		ctx.Printf("not creating CL: found existing CL %s", openCL)
		return openCL, nil
	}

	head, err := r.Gerrit.ReadBranchHead(ctx, "vscode-go", "master")
	if err != nil {
		return "", err
	}

	content, err := r.Gerrit.ReadFile(ctx, "vscode-go", head, "CHANGELOG.md")
	if err != nil {
		return "", err
	}

	lines := bytes.Split(content, []byte("\n"))
	var output bytes.Buffer

	if release.Patch == 0 && isVSCodeGoStableVersion(release, "") {
		for i, line := range lines {
			output.Write(line)
			// Only add a newline if it's not the last line
			if i < len(lines)-1 {
				output.WriteString("\n")
			}
			if string(line) == "## Unreleased" {
				output.WriteString("\n")
				output.WriteString("## " + release.String() + "\n")
				output.WriteString("\n")
				output.WriteString("Date: " + time.Now().Format(time.DateOnly) + "\n")
			}
		}
	} else {
		entryAdded := false
		for i, line := range lines {
			if !entryAdded && bytes.HasPrefix(line, []byte("## ")) && string(line) != "## Unreleased" {
				entryAdded = true
				baseRelease, basePrerelease, err := r.diffBaseVersion(ctx, release)
				if err != nil {
					return "", err
				}

				data := map[string]string{
					"Current":   release.String(),
					"Date":      time.Now().Format(time.DateOnly),
					"Previous":  versionString(baseRelease, basePrerelease),
					"Milestone": vscodeGoMilestone(release),
				}

				if isVSCodeGoInsiderVersion(release, "") {
					data["NextStable"] = fmt.Sprintf("v%v.%v", release.Major, release.Minor+1)
					data["IsPrerelease"] = "true"
				}

				tmpl, err := template.New("vscode release").Parse(vscodeGoChangelogEntryTmplStr)
				if err != nil {
					return "", err
				}

				if err := tmpl.Execute(&output, data); err != nil {
					return "", err
				}
			}
			output.Write(line)
			// Only add a newline if it's not the last line
			if i < len(lines)-1 {
				output.WriteString("\n")
			}
		}
	}

	cl, err := r.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "vscode-go",
		Branch:  "master",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the CHANGELOG.md.\n", clTitle),
	}, reviewers, map[string]string{"CHANGELOG.md": output.String()})
	if err != nil {
		return "", err
	}

	ctx.Printf("created auto-submit change %s under branch master in vscode-go repo.", cl)
	return cl, nil
}
