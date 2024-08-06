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

	"github.com/google/go-github/v48/github"
	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
)

// ReleaseGoplsTasks implements a new workflow definition include all the tasks
// to release a gopls.
type ReleaseGoplsTasks struct {
	Github     GitHubClientInterface
	Gerrit     GerritClient
	CloudBuild CloudBuildClient
}

// NewDefinition create a new workflow definition for releasing gopls.
func (r *ReleaseGoplsTasks) NewDefinition() *wf.Definition {
	wd := wf.New()

	// TODO(hxjiang): provide potential release versions in the relui where the
	// coordinator can choose which version to release instead of manual input.
	version := wf.Param(wd, wf.ParamDef[string]{Name: "version"})
	reviewers := wf.Param(wd, reviewersParam)

	semversion := wf.Task1(wd, "validating input version", r.isValidVersion, version)
	prerelease := wf.Task1(wd, "find the pre-release version", r.nextPrerelease, semversion)

	issue := wf.Task1(wd, "create release git issue", r.createReleaseIssue, semversion)
	branchCreated := wf.Action1(wd, "creating new branch if minor release", r.createBranchIfMinor, semversion, wf.After(issue))

	configChangeID := wf.Task3(wd, "updating branch's codereview.cfg", r.updateCodeReviewConfig, semversion, reviewers, issue, wf.After(branchCreated))
	configCommit := wf.Task1(wd, "await config CL submission", r.AwaitSubmission, configChangeID)

	dependencyChangeID := wf.Task4(wd, "updating gopls' x/tools dependency", r.updateXToolsDependency, semversion, prerelease, reviewers, issue, wf.After(configCommit))
	dependencyCommit := wf.Task1(wd, "await gopls' x/tools dependency CL submission", r.AwaitSubmission, dependencyChangeID)

	verified := wf.Action1(wd, "verify installing latest gopls in release branch using go install", r.verifyGoplsInstallation, dependencyCommit)
	_ = wf.Task3(wd, "tag pre-release", r.tagPrerelease, semversion, prerelease, dependencyCommit, wf.After(verified))
	return wd
}

// createReleaseIssue attempts to locate the release issue associated with the
// given milestone. If no such issue exists, a new one is created.
//
// Returns the ID of the release issue (either newly created or pre-existing).
// Returns error if the release milestone does not exist or is closed.
func (r *ReleaseGoplsTasks) createReleaseIssue(ctx *wf.TaskContext, semv semversion) (int64, error) {
	versionString := fmt.Sprintf("v%v.%v.%v", semv.Major, semv.Minor, semv.Patch)
	milestoneName := fmt.Sprintf("gopls/%s", versionString)
	// All milestones and issues resides under go repo.
	milestoneID, err := r.Github.FetchMilestone(ctx, "golang", "go", milestoneName, false)
	if err != nil {
		return -1, err
	}
	ctx.Printf("found release milestone %v", milestoneID)
	issues, err := r.Github.FetchMilestoneIssues(ctx, "golang", "go", milestoneID)
	if err != nil {
		return -1, err
	}

	title := fmt.Sprintf("x/tools/gopls: release version %s", versionString)
	for id := range issues {
		issue, _, err := r.Github.GetIssue(ctx, "golang", "go", id)
		if err != nil {
			return -1, err
		}
		if title == issue.GetTitle() {
			ctx.Printf("found existing releasing issue %v", id)
			return int64(id), nil
		}
	}

	content := fmt.Sprintf(`This issue tracks progress toward releasing gopls@%s

- [ ] create or update %s
- [ ] update go.mod/go.sum (remove x/tools replace, update x/tools version)
- [ ] tag gopls/%s-pre.1
- [ ] update Github milestone
- [ ] write release notes
- [ ] smoke test features
- [ ] tag gopls/%s
- [ ] (if vX.Y.0 release): update dependencies in master for the next release
`, versionString, goplsReleaseBranchName(semv), versionString, versionString)
	// TODO(hxjiang): accept a new parameter release coordinator.
	assignee := "h9jiang"
	issue, _, err := r.Github.CreateIssue(ctx, "golang", "go", &github.IssueRequest{
		Title:     &title,
		Body:      &content,
		Labels:    &[]string{"gopls", "Tools"},
		Assignee:  &assignee,
		Milestone: &milestoneID,
	})
	if err != nil {
		return -1, fmt.Errorf("failed to create release tracking issue for %q: %w", versionString, err)
	}
	ctx.Printf("created releasing issue %v", *issue.Number)
	return int64(*issue.Number), nil
}

// goplsReleaseBranchName returns the branch name for given input release version.
func goplsReleaseBranchName(semv semversion) string {
	return fmt.Sprintf("gopls-release-branch.%v.%v", semv.Major, semv.Minor)
}

// createBranchIfMinor create the release branch if the input version is a minor
// release.
// All patch releases under the same minor version share the same release branch.
func (r *ReleaseGoplsTasks) createBranchIfMinor(ctx *wf.TaskContext, semv semversion) error {
	branch := goplsReleaseBranchName(semv)

	// Require gopls release branch existence if this is a non-minor release.
	if semv.Patch != 0 {
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
func (r *ReleaseGoplsTasks) openCL(ctx *wf.TaskContext, branch, title string) (string, error) {
	// Query for an existing pending config CL, to avoid duplication.
	query := fmt.Sprintf(`message:%q status:open owner:gobot@golang.org repo:tools branch:%q -age:7d`, title, branch)
	changes, err := r.Gerrit.QueryChanges(ctx, query)
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
func (r *ReleaseGoplsTasks) updateCodeReviewConfig(ctx *wf.TaskContext, semv semversion, reviewers []string, issue int64) (string, error) {
	const configFile = "codereview.cfg"

	branch := goplsReleaseBranchName(semv)
	clTitle := fmt.Sprintf("all: update %s for %s", configFile, branch)

	openCL, err := r.openCL(ctx, branch, clTitle)
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

// nextPrerelease inspects the tags in tools repo that match with the given
// version and find the next prerelease version.
func (r *ReleaseGoplsTasks) nextPrerelease(ctx *wf.TaskContext, semv semversion) (string, error) {
	tags, err := r.Gerrit.ListTags(ctx, "tools")
	if err != nil {
		return "", fmt.Errorf("failed to list tags for tools repo: %w", err)
	}

	max := 0
	for _, tag := range tags {
		v, ok := strings.CutPrefix(tag, "gopls/")
		if !ok {
			continue
		}
		cur, ok := parseSemver(v)
		if !ok {
			continue
		}
		if cur.Major != semv.Major || cur.Minor != semv.Minor || cur.Patch != semv.Patch {
			continue
		}
		pre, err := cur.prereleaseVersion()
		if err != nil {
			continue
		}

		if pre > max {
			max = pre
		}
	}

	return fmt.Sprintf("pre.%v", max+1), nil
}

// updateXToolsDependency ensures gopls sub module have the correct x/tools
// version as dependency.
//
// It returns the change ID, or "" if the CL was not created.
func (r *ReleaseGoplsTasks) updateXToolsDependency(ctx *wf.TaskContext, semv semversion, pre string, reviewers []string, issue int64) (string, error) {
	if pre == "" {
		return "", fmt.Errorf("the input pre-release version should not be empty")
	}

	branch := goplsReleaseBranchName(semv)
	clTitle := fmt.Sprintf("gopls: update go.mod for v%v.%v.%v-%s", semv.Major, semv.Minor, semv.Patch, pre)
	openCL, err := r.openCL(ctx, branch, clTitle)
	if err != nil {
		return "", fmt.Errorf("failed to find the open CL of title %q in branch %q: %w", clTitle, branch, err)
	}
	if openCL != "" {
		ctx.Printf("not creating CL: found existing CL %s", openCL)
		return openCL, nil
	}

	outputFiles := []string{"gopls/go.mod.before", "gopls/go.mod", "gopls/go.sum.before", "gopls/go.sum"}
	const scriptFmt = `cp gopls/go.mod gopls/go.mod.before
cp gopls/go.sum gopls/go.sum.before
cd gopls
go mod edit -dropreplace=golang.org/x/tools
go get -u golang.org/x/tools@%s
go mod tidy -compat=1.19
`
	// TODO(hxjiang): Replacing branch with the latest commit in the release
	// branch. Module proxy might return an outdated commit when using the branch
	// name (to be confirmed with samthanawalla@).
	build, err := r.CloudBuild.RunScript(ctx, fmt.Sprintf(scriptFmt, branch), "tools", outputFiles)
	if err != nil {
		return "", err
	}

	outputs, err := buildToOutputs(ctx, r.CloudBuild, build)
	if err != nil {
		return "", err
	}

	changedFiles := map[string]string{}
	for i := 0; i < len(outputFiles); i += 2 {
		before, after := outputs[outputFiles[i]], outputs[outputFiles[i+1]]
		if before != after {
			changedFiles[outputFiles[i+1]] = after
		}
	}

	// Skip CL creation as nothing changed.
	if len(changedFiles) == 0 {
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "tools",
		Branch:  branch,
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the go.mod go.sum.\n\nFor golang/go#%v", clTitle, issue),
	}

	ctx.Printf("creating auto-submit change under branch %q in x/tools repo.", branch)
	return r.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, changedFiles)
}

func (r *ReleaseGoplsTasks) verifyGoplsInstallation(ctx *wf.TaskContext, commit string) error {
	if commit == "" {
		return fmt.Errorf("the input commit should not be empty")
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

	ctx.Printf("verify gopls with commit %s\n", commit)
	build, err := r.CloudBuild.RunScript(ctx, fmt.Sprintf(scriptFmt, commit), "", []string{"install.log", "version.log", "smoke.log"})
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

func (r *ReleaseGoplsTasks) tagPrerelease(ctx *wf.TaskContext, semv semversion, commit, pre string) (string, error) {
	if commit == "" {
		return "", fmt.Errorf("the input commit should not be empty")
	}
	if pre == "" {
		return "", fmt.Errorf("the input pre-release version should not be empty")
	}

	tag := fmt.Sprintf("gopls/v%v.%v.%v-%s", semv.Major, semv.Minor, semv.Patch, pre)
	ctx.Printf("tag commit %s with tag %s", commit, tag)
	if err := r.Gerrit.Tag(ctx, "tools", tag, commit); err != nil {
		return "", err
	}

	return tag, nil
}

// AwaitSubmission waits for the CL with the given change ID to be submitted.
//
// The return value is the submitted commit hash, or "" if changeID is "".
func (r *ReleaseGoplsTasks) AwaitSubmission(ctx *wf.TaskContext, changeID string) (string, error) {
	if changeID == "" {
		ctx.Printf("not awaiting: no CL was created")
		return "", nil
	}

	ctx.Printf("awaiting review/submit of %v", ChangeLink(changeID))
	return AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return r.Gerrit.Submitted(ctx, changeID, "")
	})
}

func (r *ReleaseGoplsTasks) isValidVersion(ctx *wf.TaskContext, ver string) (semversion, error) {
	if !semver.IsValid(ver) {
		return semversion{}, fmt.Errorf("the input %q version does not follow semantic version schema", ver)
	}

	versions, err := r.possibleGoplsVersions(ctx)
	if err != nil {
		return semversion{}, fmt.Errorf("failed to get latest Gopls version tags from x/tool: %w", err)
	}

	if !slices.Contains(versions, ver) {
		return semversion{}, fmt.Errorf("the input %q is not next version of any existing versions", ver)
	}

	semver, _ := parseSemver(ver)
	return semver, nil
}

// semversion is a parsed semantic version.
type semversion struct {
	Major, Minor, Patch int
	Pre                 string
}

// parseSemver attempts to parse semver components out of the provided semver
// v. If v is not valid semver in canonical form, parseSemver returns false.
func parseSemver(v string) (_ semversion, ok bool) {
	var parsed semversion
	v, parsed.Pre, _ = strings.Cut(v, "-")
	if _, err := fmt.Sscanf(v, "v%d.%d.%d", &parsed.Major, &parsed.Minor, &parsed.Patch); err == nil {
		ok = true
	}
	return parsed, ok
}

func (s *semversion) prereleaseVersion() (int, error) {
	parts := strings.Split(s.Pre, ".")
	if len(parts) == 1 {
		return -1, fmt.Errorf(`pre-release version does not contain any "."`)
	}

	if len(parts) > 2 {
		return -1, fmt.Errorf(`pre-release version contains %v "."`, len(parts)-1)
	}

	pre, err := strconv.Atoi(parts[1])
	if err != nil {
		return -1, fmt.Errorf("failed to convert pre-release version to int %q: %w", pre, err)
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

	var semVersions []semversion
	majorMinorPatch := map[int]map[int]map[int]bool{}
	for _, tag := range tags {
		v, ok := strings.CutPrefix(tag, "gopls/")
		if !ok {
			continue
		}

		if !semver.IsValid(v) {
			continue
		}

		// Skip for pre-release versions.
		if semver.Prerelease(v) != "" {
			continue
		}

		semv, ok := parseSemver(v)
		semVersions = append(semVersions, semv)

		if majorMinorPatch[semv.Major] == nil {
			majorMinorPatch[semv.Major] = map[int]map[int]bool{}
		}
		if majorMinorPatch[semv.Major][semv.Minor] == nil {
			majorMinorPatch[semv.Major][semv.Minor] = map[int]bool{}
		}
		majorMinorPatch[semv.Major][semv.Minor][semv.Patch] = true
	}

	var possible []string
	seen := map[string]bool{}
	for _, v := range semVersions {
		nextMajor := fmt.Sprintf("v%d.%d.%d", v.Major+1, 0, 0)
		if _, ok := majorMinorPatch[v.Major+1]; !ok && !seen[nextMajor] {
			seen[nextMajor] = true
			possible = append(possible, nextMajor)
		}

		nextMinor := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor+1, 0)
		if _, ok := majorMinorPatch[v.Major][v.Minor+1]; !ok && !seen[nextMinor] {
			seen[nextMinor] = true
			possible = append(possible, nextMinor)
		}

		nextPatch := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch+1)
		if _, ok := majorMinorPatch[v.Major][v.Minor][v.Patch+1]; !ok && !seen[nextPatch] {
			seen[nextPatch] = true
			possible = append(possible, nextPatch)
		}
	}

	semver.Sort(possible)
	return possible, nil
}
