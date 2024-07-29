// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
)

// ReleaseGoplsTasks implements a new workflow definition include all the tasks
// to release a gopls.
type ReleaseGoplsTasks struct {
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
	branchCreated := wf.Action1(wd, "creating new branch if minor release", r.createBranchIfMinor, semversion)
	changeID := wf.Task2(wd, "updating branch's codereview.cfg", r.updateCodeReviewConfig, semversion, reviewers, wf.After(branchCreated))
	_ = wf.Action1(wd, "await config CL submission", r.AwaitSubmission, changeID)
	return wd
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

// updateCodeReviewConfig ensures codereview.cfg contains the expected
// configuration.
//
// It returns the change ID, or "" if the CL was not created.
func (r *ReleaseGoplsTasks) updateCodeReviewConfig(ctx *wf.TaskContext, semv semversion, reviewers []string) (string, error) {
	const configFile = "codereview.cfg"
	const configFmt = `issuerepo: golang/go
branch: %s
parent-branch: master
`

	branch := goplsReleaseBranchName(semv)
	clTitle := fmt.Sprintf("all: update %s for %s", configFile, branch)

	// Query for an existing pending config CL, to avoid duplication.
	query := fmt.Sprintf(`message:%q status:open owner:gobot@golang.org repo:tools branch:%q -age:7d`, clTitle, branch)
	changes, err := r.Gerrit.QueryChanges(ctx, query)
	if err != nil {
		return "", err
	}
	if len(changes) > 0 {
		ctx.Printf("not creating CL: found existing CL %d", changes[0].ChangeNumber)
		return changes[0].ChangeID, nil
	}

	head, err := r.Gerrit.ReadBranchHead(ctx, "tools", branch)
	if err != nil {
		return "", err
	}

	before, err := r.Gerrit.ReadFile(ctx, "tools", head, configFile)
	if err != nil && !errors.Is(err, gerrit.ErrResourceNotExist) {
		return "", err
	}

	after := fmt.Sprintf(configFmt, branch)
	// Skip CL creation as config has not changed.
	if string(before) == after {
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "tools",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the %s.", clTitle, configFile),
		Branch:  branch,
	}

	files := map[string]string{
		configFile: string(after),
	}

	ctx.Printf("creating auto-submit change to %s under branch %q in x/tools repo.", configFile, branch)
	return r.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, files)
}

// AwaitSubmission waits for the CL with the given change ID to be submitted.
//
// The return value is the submitted commit hash, or "" if changeID is "".
func (r *ReleaseGoplsTasks) AwaitSubmission(ctx *wf.TaskContext, changeID string) error {
	if changeID == "" {
		ctx.Printf("not awaiting: no CL was created")
		return nil
	}

	ctx.Printf("awaiting review/submit of %v", ChangeLink(changeID))
	_, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return r.Gerrit.Submitted(ctx, changeID, "")
	})
	return err
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
