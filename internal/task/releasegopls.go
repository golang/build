// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/google/go-github/v48/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
)

var releaseCoordinatorsParam = wf.ParamDef[[]string]{
	Name:      "Release Coordinator Usernames",
	ParamType: wf.SliceShort,
	Doc: `Release Coordinator Usernames is a required list of the coordinators of the release.

The first user in the list will be assigned to the release tracking issue.
All users will be added as reviewers for automatically generated CLs.`,
	Check: CheckCoordinators,
}

// ReleaseGoplsTasks provides workflow definitions and tasks for releasing gopls.
type ReleaseGoplsTasks struct {
	Github             GitHubClientInterface
	Gerrit             GerritClient
	CloudBuild         CloudBuildClient
	SendMail           func(MailHeader, MailContent) error
	AnnounceMailHeader MailHeader
	ApproveAction      func(*wf.TaskContext) error
}

// NewPrereleaseDefinition create a new workflow definition for gopls pre-release.
func (r *ReleaseGoplsTasks) NewPrereleaseDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	// versionBumpStrategy specifies the desired release type: next minor, next
	// patch or use explicit version.
	// This should be the default choice for most releases.
	versionBumpStrategy := wf.Param(wd, nextVersionParam)
	// inputVersion allows manual override of the version, bypassing the version
	// bump strategy.
	// Use with caution.
	inputVersion := wf.Param(wd, wf.ParamDef[string]{Name: "explicit version (optional)"})
	coordinators := wf.Param(wd, releaseCoordinatorsParam)

	release := wf.Task2(wd, "determine the release version", r.determineReleaseVersion, inputVersion, versionBumpStrategy)
	prerelease := wf.Task1(wd, "find the next pre-release version", r.nextPrereleaseVersion, release)
	approved := wf.Action2(wd, "wait for release coordinator approval", r.approvePrerelease, release, prerelease)

	issue := wf.Task3(wd, "create release git issue", r.findOrCreateGitHubIssue, release, coordinators, wf.Const(true), wf.After(approved))
	branchCreated := wf.Action1(wd, "create new branch if minor release", r.createBranchIfMinor, release, wf.After(issue))

	configChangeID := wf.Task3(wd, "update branch's codereview.cfg", r.updateCodeReviewConfig, release, coordinators, issue, wf.After(branchCreated))
	configCommit := wf.Task1(wd, "await config CL submission", clAwaiter{r.Gerrit}.awaitSubmission, configChangeID)

	dependencyChangeID := wf.Task4(wd, "update gopls' x/tools dependency", r.updateXToolsDependency, release, prerelease, coordinators, issue, wf.After(configCommit))
	dependencyCommit := wf.Task1(wd, "await gopls' x/tools dependency CL submission", clAwaiter{r.Gerrit}.awaitSubmission, dependencyChangeID)

	verified := wf.Action1(wd, "verify installing latest gopls using release branch dependency commit", r.verifyGoplsInstallation, dependencyCommit)
	prereleaseVersion := wf.Task3(wd, "tag pre-release", r.tagPrerelease, release, dependencyCommit, prerelease, wf.After(verified))
	prereleaseVerified := wf.Action1(wd, "verify installing latest gopls using release branch pre-release version", r.verifyGoplsInstallation, prereleaseVersion)
	wf.Action4(wd, "mail announcement", r.mailPrereleaseAnnouncement, release, prereleaseVersion, dependencyCommit, issue, wf.After(prereleaseVerified))

	vscodeGoChange := wf.Task4(wd, "update gopls settings in vscode-go", r.updateVSCodeGoGoplsSetting, coordinators, issue, release, prerelease, wf.After(prereleaseVerified))
	_ = wf.Task1(wd, "await gopls settings update CLs submission in vscode-go", clAwaiter{r.Gerrit}.awaitSubmission, vscodeGoChange)

	wf.Output(wd, "version", prereleaseVersion)

	return wd
}

// determineReleaseVersion returns the release version based on coordinator inputs.
//
// Returns the specified input version if provided; otherwise, interpret a new
// version based on the version bumping strategy.
func (r *ReleaseGoplsTasks) determineReleaseVersion(ctx *wf.TaskContext, inputVersion, versionBumpStrategy string) (releaseVersion, error) {
	switch versionBumpStrategy {
	case "use explicit version":
		if inputVersion == "" {
			return releaseVersion{}, fmt.Errorf("the input version should not be empty when choosing explicit version release")
		}
		if err := r.isValidReleaseVersion(ctx, inputVersion); err != nil {
			return releaseVersion{}, err
		}
		release, prerelease, ok := parseVersion(inputVersion)
		if !ok {
			return releaseVersion{}, fmt.Errorf("input version %q can not be parsed as semantic version", inputVersion)
		}
		if prerelease != "" {
			return releaseVersion{}, fmt.Errorf("input version %q can not be a prerelease version", inputVersion)
		}
		return release, nil
	case "next minor", "next patch":
		return r.interpretNextRelease(ctx, versionBumpStrategy)
	default:
		return releaseVersion{}, fmt.Errorf("unknown version selection strategy: %q", versionBumpStrategy)
	}
}

func (r *ReleaseGoplsTasks) interpretNextRelease(ctx *wf.TaskContext, versionBumpStrategy string) (releaseVersion, error) {
	tags, err := r.Gerrit.ListTags(ctx, "tools")
	if err != nil {
		return releaseVersion{}, err
	}

	var versions []string
	for _, tag := range tags {
		if v, ok := strings.CutPrefix(tag, "gopls/"); ok {
			versions = append(versions, v)
		}
	}

	release, _ := latestVersion(versions, isReleaseVersion)
	switch versionBumpStrategy {
	case "next minor":
		release.Minor += 1
		release.Patch = 0
	case "next patch":
		release.Patch += 1
	default:
		return releaseVersion{}, fmt.Errorf("unknown version selection strategy: %q", versionBumpStrategy)
	}

	return release, nil
}

// approvePrerelease prompts the approval for creating a pre-release version.
func (r *ReleaseGoplsTasks) approvePrerelease(ctx *wf.TaskContext, release releaseVersion, pre string) error {
	ctx.Printf("The next release candidate will be %s-%s", release, pre)

	return r.ApproveAction(ctx)
}

// approveRelease prompts the approval for releasing a pre-release version.
func (r *ReleaseGoplsTasks) approveRelease(ctx *wf.TaskContext, release releaseVersion, pre string) error {
	ctx.Printf("The release candidate %s-%s will be released as %s", release, pre, release)

	return r.ApproveAction(ctx)
}

// findOrCreateGitHubIssue locates or creates the release issue for the given
// release milestone.
//
// If the release issue exists, return the issue ID.
// If 'create' is true and no issue exists, a new one is created.
// If 'create' is false and no issue exists, an error is returned.
func (r *ReleaseGoplsTasks) findOrCreateGitHubIssue(ctx *wf.TaskContext, release releaseVersion, coordinators []string, create bool) (int64, error) {
	versionString := release.String()
	milestoneName := fmt.Sprintf("gopls/%s", versionString)
	// All milestones and issues resides under go repo.
	milestoneID, err := r.Github.FetchMilestone(ctx, "golang", "go", milestoneName, false)
	if err != nil {
		return 0, err
	}
	ctx.Printf("found release milestone %v", milestoneID)
	issues, err := r.Github.FetchMilestoneIssues(ctx, "golang", "go", milestoneID)
	if err != nil {
		return 0, err
	}

	title := fmt.Sprintf("x/tools/gopls: release version %s", versionString)
	for id := range issues {
		issue, _, err := r.Github.GetIssue(ctx, "golang", "go", id)
		if err != nil {
			return 0, err
		}
		if title == issue.GetTitle() {
			ctx.Printf("found existing releasing issue %v", id)
			return int64(id), nil
		}
	}

	if !create {
		return 0, fmt.Errorf("could not find any release issue for %s", versionString)
	}

	ctx.DisableRetries()
	content := fmt.Sprintf(`This issue tracks progress toward releasing gopls@%s

- [ ] create or update %s
- [ ] update go.mod/go.sum (remove x/tools replace, update x/tools version)
- [ ] tag gopls/%s-pre.1
- [ ] update Github milestone
- [ ] write release notes
- [ ] smoke test features
- [ ] tag gopls/%s
- [ ] (if vX.Y.0 release): update dependencies in master for the next release
`, versionString, goplsReleaseBranchName(release), versionString, versionString)

	if len(coordinators) == 0 {
		return 0, fmt.Errorf("the input coordinators slice is empty")
	}
	assignee, err := lookupCoordinator(coordinators[0])
	if err != nil {
		return 0, fmt.Errorf("failed to find the coordinator %q", coordinators[0])
	}
	issue, _, err := r.Github.CreateIssue(ctx, "golang", "go", &github.IssueRequest{
		Title:     github.String(title),
		Body:      github.String(content),
		Labels:    &[]string{"gopls", "Tools"},
		Assignee:  github.String(assignee.GitHub),
		Milestone: github.Int(milestoneID),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create release tracking issue for %q: %w", versionString, err)
	}
	ctx.Printf("created releasing issue %v", *issue.Number)
	return int64(*issue.Number), nil
}

// goplsReleaseBranchName returns the branch name for given input release version.
func goplsReleaseBranchName(release releaseVersion) string {
	return fmt.Sprintf("gopls-release-branch.%v.%v", release.Major, release.Minor)
}

// createBranchIfMinor create the release branch if the input version is a minor
// release.
// All patch releases under the same minor version share the same release branch.
func (r *ReleaseGoplsTasks) createBranchIfMinor(ctx *wf.TaskContext, release releaseVersion) error {
	branch := goplsReleaseBranchName(release)

	// Require gopls release branch existence if this is a non-minor release.
	if release.Patch != 0 {
		_, err := r.Gerrit.ReadBranchHead(ctx, "tools", branch)
		return err
	}

	// Return early if the branch already exist.
	// This scenario should only occur if the initial minor release flow failed
	// or was interrupted and subsequently re-triggered.
	if _, err := r.Gerrit.ReadBranchHead(ctx, "tools", branch); err == nil {
		return nil
	}

	// Create the release branch using the revision from the head of master branch.
	head, err := r.Gerrit.ReadBranchHead(ctx, "tools", "master")
	if err != nil {
		return err
	}

	ctx.Printf("Creating branch %s at revision %s.\n", branch, head)
	_, err = r.Gerrit.CreateBranch(ctx, "tools", branch, gerrit.BranchInput{Revision: head})
	return err
}

// openCL checks if an open CL with the given title exists in the specified
// branch.
//
// It returns an empty string if no such CL is found, otherwise it returns the
// CL's change ID.
func openCL(ctx *wf.TaskContext, gerrit GerritClient, repo, branch, title string) (string, error) {
	// Query for an existing pending config CL, to avoid duplication.
	query := fmt.Sprintf(`message:%q status:open owner:gobot@golang.org repo:%s branch:%q -age:7d`, title, repo, branch)
	changes, err := gerrit.QueryChanges(ctx, query)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}

	return changes[0].ChangeID, nil
}

// updateCodeReviewConfig checks if codereview.cfg has the desired configuration.
//
// It returns the change ID required to update the config if changes are needed,
// otherwise it returns an empty string indicating no update is necessary.
func (r *ReleaseGoplsTasks) updateCodeReviewConfig(ctx *wf.TaskContext, release releaseVersion, reviewers []string, issue int64) (string, error) {
	const configFile = "codereview.cfg"

	branch := goplsReleaseBranchName(release)
	clTitle := fmt.Sprintf("all: update %s for %s", configFile, branch)

	openCL, err := openCL(ctx, r.Gerrit, "tools", branch, clTitle)
	if err != nil {
		return "", fmt.Errorf("failed to find the open CL of title %q in branch %q: %w", clTitle, branch, err)
	}
	if openCL != "" {
		ctx.Printf("not creating CL: found existing CL %s", openCL)
		return openCL, nil
	}

	head, err := r.Gerrit.ReadBranchHead(ctx, "tools", branch)
	if err != nil {
		return "", err
	}

	before, err := r.Gerrit.ReadFile(ctx, "tools", head, configFile)
	if err != nil && !errors.Is(err, gerrit.ErrResourceNotExist) {
		return "", err
	}
	const configFmt = `issuerepo: golang/go
branch: %s
parent-branch: master
`
	after := fmt.Sprintf(configFmt, branch)
	// Skip CL creation as config has not changed.
	if string(before) == after {
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "tools",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the %s.\n\nFor golang/go#%v", clTitle, configFile, issue),
		Branch:  branch,
	}

	files := map[string]string{
		configFile: string(after),
	}

	ctx.Printf("creating auto-submit change to %s under branch %q in x/tools repo.", configFile, branch)
	return r.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, files)
}

// nextPrereleaseVersion inspects the tags in tools repo that match with the given
// version and finds the next prerelease version.
func (r *ReleaseGoplsTasks) nextPrereleaseVersion(ctx *wf.TaskContext, release releaseVersion) (string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "tools")
	if err != nil {
		return "", err
	}

	var versions []string
	for _, tag := range tags {
		if v, ok := strings.CutPrefix(tag, "gopls/"); ok {
			versions = append(versions, v)
		}
	}

	_, prerelease := latestVersion(versions, isSameReleaseVersion(release), isPrereleaseMatchRegex(`^pre\.\d+$`))
	if prerelease == "" {
		return "pre.1", nil
	}
	pre, err := prereleaseNumber(prerelease)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pre.%v", pre+1), nil
}

// updateXToolsDependency ensures gopls sub module have the correct x/tools
// version as dependency.
//
// It returns the change ID, or "" if the CL was not created.
func (r *ReleaseGoplsTasks) updateXToolsDependency(ctx *wf.TaskContext, release releaseVersion, pre string, reviewers []string, issue int64) (string, error) {
	if pre == "" {
		return "", fmt.Errorf("the input pre-release version should not be empty")
	}

	branch := goplsReleaseBranchName(release)
	clTitle := fmt.Sprintf("gopls: update go.mod for %s-%s", release, pre)
	openCL, err := openCL(ctx, r.Gerrit, "tools", branch, clTitle)
	if err != nil {
		return "", fmt.Errorf("failed to find the open CL of title %q in branch %q: %w", clTitle, branch, err)
	}
	if openCL != "" {
		ctx.Printf("not creating CL: found existing CL %s", openCL)
		return openCL, nil
	}

	head, err := r.Gerrit.ReadBranchHead(ctx, "tools", branch)
	if err != nil {
		return "", err
	}
	// TODO(hxjiang): Remove -compat flag once gopls no longer supports building
	// with older Go versions.
	script := fmt.Sprintf(`cd gopls
go mod edit -dropreplace=golang.org/x/tools
go get golang.org/x/tools@%s
go mod tidy -compat=1.19
`, head)

	changedFiles, err := executeAndMonitorChange(ctx, r.CloudBuild, "tools", branch, script, []string{"gopls/go.mod", "gopls/go.sum"})
	if err != nil {
		return "", err
	}

	// Skip CL creation as nothing changed.
	if len(changedFiles) == 0 {
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "tools",
		Branch:  branch,
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the go.mod and go.sum.\n\nFor golang/go#%v", clTitle, issue),
	}

	ctx.Printf("creating auto-submit change under branch %q in x/tools repo.", branch)
	return r.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, changedFiles)
}

// verifyGoplsInstallation installs the gopls with the provided version and run
// smoke test.
// The input version can be a commit, a branch name, a semantic version, see
// more detail https://go.dev/ref/mod#version-queries
func (r *ReleaseGoplsTasks) verifyGoplsInstallation(ctx *wf.TaskContext, version string) error {
	if version == "" {
		return fmt.Errorf("the input version should not be empty")
	}
	const scriptFmt = `go install golang.org/x/tools/gopls@%s &> install.log
$(go env GOPATH)/bin/gopls version &> version.log
echo -n "package main

func main () {
	const a = 2
	b := a
}" > main.go
$(go env GOPATH)/bin/gopls references -d main.go:4:8 &> smoke.log
`

	ctx.Printf("verify gopls with version %s\n", version)
	build, err := r.CloudBuild.RunScript(ctx, fmt.Sprintf(scriptFmt, version), "", []string{"install.log", "version.log", "smoke.log"})
	if err != nil {
		return err
	}

	outputs, err := buildToOutputs(ctx, r.CloudBuild, build)
	if err != nil {
		return err
	}
	ctx.Printf("verify gopls installation process:\n%s\n", outputs["install.log"])
	ctx.Printf("verify gopls version:\n%s\n", outputs["version.log"])
	ctx.Printf("verify gopls functionality with gopls references smoke test:\n%s\n", outputs["smoke.log"])
	return nil
}

// tagPrerelease applies gopls pre-release tags to the given commit.
// The input semversion provides Major, Minor, and Patch info.
// The input pre-release, generated by previous steps of the workflow, provides
// Pre-release info.
func (r *ReleaseGoplsTasks) tagPrerelease(ctx *wf.TaskContext, release releaseVersion, commit, pre string) (string, error) {
	if commit == "" {
		return "", fmt.Errorf("the input commit should not be empty")
	}
	if pre == "" {
		return "", fmt.Errorf("the input pre-release version should not be empty")
	}

	// Defensively guard against re-creating tags.
	ctx.DisableRetries()

	version := fmt.Sprintf("%s-%s", release, pre)
	tag := fmt.Sprintf("gopls/%s", version)
	if err := r.Gerrit.Tag(ctx, "tools", tag, commit); err != nil {
		return "", err
	}

	ctx.Printf("tagged commit %s with tag %s", commit, tag)
	return version, nil
}

type goplsPrereleaseAnnouncement struct {
	Version string
	Branch  string
	Commit  string
	Issue   int64
}

func (r *ReleaseGoplsTasks) mailPrereleaseAnnouncement(ctx *wf.TaskContext, release releaseVersion, rc, commit string, issue int64) error {
	announce := goplsPrereleaseAnnouncement{
		Version: rc,
		Branch:  goplsReleaseBranchName(release),
		Commit:  commit,
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

type goplsReleaseAnnouncement struct {
	Version string
	Branch  string
	Commit  string
}

func (r *ReleaseGoplsTasks) mailReleaseAnnouncement(ctx *wf.TaskContext, release releaseVersion) error {
	version := fmt.Sprintf("v%v.%v.%v", release.Major, release.Minor, release.Patch)

	info, err := r.Gerrit.GetTag(ctx, "tools", fmt.Sprintf("gopls/%s", version))
	if err != nil {
		return err
	}

	announce := goplsReleaseAnnouncement{
		Version: version,
		Branch:  goplsReleaseBranchName(release),
		Commit:  info.Revision,
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

func (r *ReleaseGoplsTasks) isValidReleaseVersion(ctx *wf.TaskContext, ver string) error {
	if !semver.IsValid(ver) {
		return fmt.Errorf("the input %q version does not follow semantic version schema", ver)
	}

	versions, err := r.possibleGoplsVersions(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest Gopls version tags from x/tool: %w", err)
	}

	if !slices.Contains(versions, ver) {
		return fmt.Errorf("the input %q is not next version of any existing versions", ver)
	}

	return nil
}

// releaseVersion is a parsed semantic release version containing only major,
// minor and patch version.
type releaseVersion struct {
	Major, Minor, Patch int
}

// String returns the version string representation of the release version.
func (s releaseVersion) String() string {
	return fmt.Sprintf("v%v.%v.%v", s.Major, s.Minor, s.Patch)
}

// parseVersion parses the input version string into a release version and a
// prerelease string.
// It returns false if the input is not a valid semantic version in canonical form.
func parseVersion(v string) (_ releaseVersion, prerelease string, ok bool) {
	var parsed releaseVersion
	if !semver.IsValid(v) {
		return releaseVersion{}, "", false
	}
	v, pre, _ := strings.Cut(v, "-")
	if _, err := fmt.Sscanf(v, "v%d.%d.%d", &parsed.Major, &parsed.Minor, &parsed.Patch); err != nil {
		return releaseVersion{}, "", false
	}
	return parsed, pre, true
}

// prereleaseNumber extracts the integer component from a pre-release version
// string in the format "${STRING}.${INT}".
func prereleaseNumber(prerelease string) (int, error) {
	parts := strings.Split(prerelease, ".")
	if len(parts) == 1 {
		return 0, fmt.Errorf(`pre-release version does not contain any "."`)
	}

	if len(parts) > 2 {
		return 0, fmt.Errorf(`pre-release version contains %v "."`, len(parts)-1)
	}

	pre, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("failed to convert pre-release version to int %q: %w", pre, err)
	}

	if pre <= 0 {
		return 0, fmt.Errorf("the pre-release version should be larger than 0: %v", pre)
	}

	return pre, nil
}

// possibleGoplsVersions identifies suitable versions for the upcoming release
// based on the current tags in the repo.
func (r *ReleaseGoplsTasks) possibleGoplsVersions(ctx *wf.TaskContext) ([]string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "tools")
	if err != nil {
		return nil, err
	}

	var releaseVersions []releaseVersion
	seen := make(map[releaseVersion]bool)
	for _, tag := range tags {
		v, ok := strings.CutPrefix(tag, "gopls/")
		if !ok {
			continue
		}

		release, prerelease, ok := parseVersion(v)
		if !ok || prerelease != "" {
			continue
		}
		releaseVersions = append(releaseVersions, release)
		seen[release] = true
	}

	var possible []string
	for _, v := range releaseVersions {
		for _, next := range []releaseVersion{
			{v.Major + 1, 0, 0},             // next major
			{v.Major, v.Minor + 1, 0},       // next minor
			{v.Major, v.Minor, v.Patch + 1}, // next patch
		} {
			if _, ok := seen[next]; !ok {
				possible = append(possible, next.String())
				seen[next] = true
			}
		}
	}

	semver.Sort(possible)
	return possible, nil
}

// NewReleaseDefinition create a new workflow definition for gopls release.
func (r *ReleaseGoplsTasks) NewReleaseDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	// versionBumpStrategy specifies the desired release type: next minor, next
	// patch or use explicit version.
	// This should be the default choice for most releases.
	versionBumpStrategy := wf.Param(wd, nextVersionParam)
	// inputVersion allows manual override of the version, bypassing the version
	// bump strategy.
	// Use with caution.
	inputVersion := wf.Param(wd, wf.ParamDef[string]{Name: "explicit version (optional)"})
	coordinators := wf.Param(wd, releaseCoordinatorsParam)

	release := wf.Task2(wd, "determine the release version", r.determineReleaseVersion, inputVersion, versionBumpStrategy)
	prerelease := wf.Task1(wd, "find the latest pre-release version", r.latestPrerelease, release)
	approved := wf.Action2(wd, "wait for release coordinator approval", r.approveRelease, release, prerelease)

	tagged := wf.Action2(wd, "tag the release", r.tagRelease, release, prerelease, wf.After(approved))

	issue := wf.Task3(wd, "find release git issue", r.findOrCreateGitHubIssue, release, wf.Const([]string{}), wf.Const(false))
	_ = wf.Action1(wd, "mail announcement", r.mailReleaseAnnouncement, release, wf.After(tagged))

	changeID := wf.Task3(wd, "updating x/tools dependency in master branch in gopls sub dir", r.updateDependencyIfMinor, coordinators, release, issue, wf.After(tagged))
	_ = wf.Task1(wd, "await x/tools gopls dependency CL submission in gopls sub dir", clAwaiter{r.Gerrit}.awaitSubmission, changeID)

	vscodeGoChange := wf.Task4(wd, "update gopls settings in vscode-go", r.updateVSCodeGoGoplsSetting, coordinators, issue, release, wf.Const(""), wf.After(tagged))
	_ = wf.Task1(wd, "await gopls settings update CLs submission in vscode-go", clAwaiter{r.Gerrit}.awaitSubmission, vscodeGoChange)

	return wd
}

func (r *ReleaseGoplsTasks) latestPrerelease(ctx *wf.TaskContext, release releaseVersion) (string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "tools")
	if err != nil {
		return "", err
	}

	var versions []string
	for _, tag := range tags {
		if v, ok := strings.CutPrefix(tag, "gopls/"); ok {
			versions = append(versions, v)
		}
	}

	_, prerelease := latestVersion(versions, isSameReleaseVersion(release), isPrereleaseMatchRegex(`^pre\.\d+$`))
	if prerelease == "" {
		return "", fmt.Errorf("could not find any release candidate for %s", release)
	}

	return prerelease, nil
}

// updateVSCodeGoGoplsSetting updates the gopls setting in the vscode-go project
// master branch.
func (r *ReleaseGoplsTasks) updateVSCodeGoGoplsSetting(ctx *wf.TaskContext, reviewers []string, issue int64, release releaseVersion, prerelease string) (string, error) {
	version := release.String()
	if prerelease != "" {
		version = version + "-" + prerelease
	}

	tags, err := r.Gerrit.ListTags(ctx, "tools")
	if err != nil {
		return "", err
	}

	var versions []string
	for _, tag := range tags {
		if v, ok := strings.CutPrefix(tag, "gopls/"); ok {
			versions = append(versions, v)
		}
	}

	// Only updating vscode-go repo if the current gopls is the newest. This
	// check is to make sure the settings in vscode-go does not get roll back
	// from higher version to lower version due to a lower version gopls
	// explicit releases (for security reasons).
	// E.g. when releasing v0.16.5, skip update if v0.17.0 already exists.
	latest, latestPre := latestVersion(versions)
	if latest.Major > release.Major ||
		(latest.Major == release.Major && latest.Minor > release.Minor) ||
		(latest.Major == release.Major && latest.Minor == release.Minor && latest.Patch > release.Patch) {
		latestString := latest.String()
		if latestPre != "" {
			latestString += "-" + latestPre
		}
		ctx.Printf("the newest release %s is higher than the current release %s, skip updating vscode-go", latestPre, version)
		return "", nil
	}

	// TODO(hxjiang): also update the change log if not already.
	// Example: https://go-review.git.corp.google.com/c/vscode-go/+/627276
	clTitle := fmt.Sprintf(`extension: update gopls %s settings`, version)
	openCL, err := openCL(ctx, r.Gerrit, "vscode-go", "master", clTitle)
	if err != nil {
		return "", fmt.Errorf("failed to find the open CL of title %q in master branch: %w", clTitle, err)
	}
	if openCL != "" {
		ctx.Printf("not creating CL: found existing CL %s", openCL)
		return openCL, nil
	}

	files := []string{
		"extension/src/goToolsInformation.ts", // gopls version info
		"extension/package.json",              // gopls setting info
		"docs/settings.md",                    // gopls setting info
	}
	steps := func(resultURL string) []*cloudbuildpb.BuildStep {
		updateScript := cloudBuildClientScriptPrefix
		for _, file := range files {
			updateScript += fmt.Sprintf("cp %s %s.before\n", file, file)
		}

		// `go run generate.go -tools` is environment-independent because it
		// fetches tools from the module proxy.
		// `go run generate.go -gopls` is environment-dependent:
		// - It requires a `gopls` executable of the correct version.
		// - It depends on the `jq` tool being available in the environment.
		// TODO(hxjiang): Explore making `-gopls` environment-independent.
		// This would allow anyone to run it without a specific local setup.
		// Consider using a Docker container to achieve this without
		// affecting the local `gopls`.
		updateScript += `
export PATH="$PATH:$(go env GOPATH)/bin"
export PATH="$PATH:/workspace/tools"
go install golang.org/x/tools/gopls@%s
go run -C extension tools/generate.go -w -tools -gopls
`
		for _, file := range files {
			updateScript += fmt.Sprintf("gsutil cp %s %s/%s\n", file, resultURL, file)
			updateScript += fmt.Sprintf("gsutil cp %s.before %s/%s.before\n", file, resultURL, file)
		}
		return []*cloudbuildpb.BuildStep{
			{
				Name:   "bash",
				Script: cloudBuildClientDownloadGoScript,
			},
			{
				Name: "bash",
				Script: `set -eux
wget -O jq https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-linux-amd64
chmod +x jq
mkdir /workspace/tools
mv jq /workspace/tools`,
			},
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"clone", "https://go.googlesource.com/vscode-go", "vscode-go"},
			},
			{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"checkout", "master"},
				Dir:  "vscode-go",
			},
			{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: fmt.Sprintf(updateScript, version),
				Dir:    "vscode-go",
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

	changed := map[string]string{}
	for _, file := range files {
		if outputs[file] != outputs[file+".before"] {
			changed[file] = outputs[file]
		}
	}

	// Skip CL creation as nothing changed.
	if len(changed) == 0 {
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "vscode-go",
		Branch:  "master",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the gopls version and settings.\n\nFor golang/go#%v", clTitle, issue),
	}

	cl, err := r.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, changed)
	if err != nil {
		return "", err
	}
	ctx.Printf("created auto-submit change %s under master branch in vscode-go.", cl)
	return cl, nil
}

// tagRelease locates the commit associated with the pre-release version and
// applies the official release tag in form of "gopls/vX.Y.Z" to the same commit.
func (r *ReleaseGoplsTasks) tagRelease(ctx *wf.TaskContext, release releaseVersion, prerelease string) error {
	info, err := r.Gerrit.GetTag(ctx, "tools", fmt.Sprintf("gopls/%s-%s", release, prerelease))
	if err != nil {
		return err
	}

	// Defensively guard against re-creating tags.
	ctx.DisableRetries()

	releaseTag := fmt.Sprintf("gopls/%s", release)
	if err := r.Gerrit.Tag(ctx, "tools", releaseTag, info.Revision); err != nil {
		return err
	}

	ctx.Printf("tagged commit %s with tag %s", info.Revision, releaseTag)
	return nil
}

// updateDependencyIfMinor update the dependency of x/tools repo in master
// branch.
//
// Returns the change ID.
func (r *ReleaseGoplsTasks) updateDependencyIfMinor(ctx *wf.TaskContext, reviewers []string, release releaseVersion, issue int64) (string, error) {
	if release.Patch != 0 {
		return "", nil
	}

	clTitle := fmt.Sprintf("gopls/go.mod: update dependencies following the %s release", release)
	openCL, err := openCL(ctx, r.Gerrit, "tools", "master", clTitle)
	if err != nil {
		return "", fmt.Errorf("failed to find the open CL of title %q in master branch: %w", clTitle, err)
	}
	if openCL != "" {
		ctx.Printf("not creating CL: found existing CL %s", openCL)
		return openCL, nil
	}

	// TODO(hxjiang): Remove -compat flag once gopls no longer supports building
	// with older Go versions.
	const script = `cd gopls
pwd
go get -u all
go mod tidy -compat=1.19
`
	changed, err := executeAndMonitorChange(ctx, r.CloudBuild, "tools", "master", script, []string{"gopls/go.mod", "gopls/go.sum"})
	if err != nil {
		return "", err
	}

	// Skip CL creation as nothing changed.
	if len(changed) == 0 {
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "tools",
		Branch:  "master",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the go.mod and go.sum.\n\nFor golang/go#%v", clTitle, issue),
	}

	ctx.Printf("creating auto-submit change under master branch in x/tools repo.")
	return r.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, changed)
}

// executeAndMonitorChange runs the specified script on the designated branch,
// tracking changes to the provided files.
//
// Returns a map where keys are the filenames of modified files and values are
// their corresponding content after script execution.
func executeAndMonitorChange(ctx *wf.TaskContext, cloudBuild CloudBuildClient, project, branch, script string, watchFiles []string) (map[string]string, error) {
	// Checkout to the provided branch.
	fullScript := fmt.Sprintf(`git checkout %s
git rev-parse --abbrev-ref HEAD
git rev-parse --ref HEAD
`, branch)
	// Make a copy of all file that need to watch.
	// If the file does not exist, create a empty file and a empty before file.
	for _, file := range watchFiles {
		if strings.Contains(file, "'") {
			return nil, fmt.Errorf("file name %q contains '", file)
		}
		fullScript += fmt.Sprintf(`if [ -f '%[1]s' ]; then
    cp '%[1]s' '%[1]s.before'
else
    touch '%[1]s' '%[1]s.before'
fi
`, file)
	}
	// Execute the script provided.
	fullScript += script

	// Output files before the script execution and after the script execution.
	outputFiles := []string{}
	for _, file := range watchFiles {
		outputFiles = append(outputFiles, file+".before")
		outputFiles = append(outputFiles, file)
	}
	build, err := cloudBuild.RunScript(ctx, fullScript, project, outputFiles)
	if err != nil {
		return nil, err
	}

	outputs, err := buildToOutputs(ctx, cloudBuild, build)
	if err != nil {
		return nil, err
	}

	changed := map[string]string{}
	for i := 0; i < len(outputFiles); i += 2 {
		if before, after := outputs[outputFiles[i]], outputs[outputFiles[i+1]]; before != after {
			changed[outputFiles[i+1]] = after
		}
	}

	return changed, nil
}

// A clAwaiter closes over a GerritClient to provide a reusable workflow task
// for awaiting the submission of a Gerrit change.
type clAwaiter struct {
	GerritClient
}

// awaitSubmission waits for the specified change to be submitted, then returns
// the corresponding commit hash.
func (c clAwaiter) awaitSubmission(ctx *wf.TaskContext, changeID string) (string, error) {
	if changeID == "" {
		ctx.Printf("not awaiting: no CL was created")
		return "", nil
	}

	ctx.Printf("awaiting review/submit of %v", ChangeLink(changeID))
	return AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return c.Submitted(ctx, changeID, "")
	})
}

// awaitSubmissions waits for the specified changes to be submitted, then
// returns a slice of commit hashes corresponding to the input change IDs,
// maintaining the original input order.
func (c clAwaiter) awaitSubmissions(ctx *wf.TaskContext, changeIDs []string) ([]string, error) {
	if len(changeIDs) == 0 {
		ctx.Printf("not awaiting: no CL was created")
		return nil, nil
	}

	var commits []string
	for _, changeID := range changeIDs {
		commit, err := c.awaitSubmission(ctx, changeID)
		if err != nil {
			return nil, err
		}
		commits = append(commits, commit)
	}

	return commits, nil
}
