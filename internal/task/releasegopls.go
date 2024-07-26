// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"
	"slices"
	"strings"

	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
)

// ReleaseGoplsTasks implements a new workflow definition include all the tasks
// to release a gopls.
type ReleaseGoplsTasks struct {
	Gerrit GerritClient
}

// NewDefinition create a new workflow definition for releasing gopls.
func (r *ReleaseGoplsTasks) NewDefinition() *wf.Definition {
	wd := wf.New()

	// TODO(hxjiang): provide potential release versions in the relui where the
	// coordinator can choose which version to release instead of manual input.
	version := wf.Param(wd, wf.ParamDef[string]{Name: "version"})
	isValid := wf.Task1(wd, "validating input version", r.isValidVersion, version)
	wf.Output(wd, "valid", isValid)
	return wd
}

func (r *ReleaseGoplsTasks) isValidVersion(ctx *wf.TaskContext, ver string) (bool, error) {
	if !semver.IsValid(ver) {
		return false, nil
	}

	versions, err := r.possibleGoplsVersions(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get latest Gopls version tags from x/tool: %w", err)
	}

	return slices.Contains(versions, ver), nil
}

// semversion is a parsed semantic version.
type semversion struct {
	major, minor, patch int
	pre                 string
}

// parseSemver attempts to parse semver components out of the provided semver
// v. If v is not valid semver in canonical form, parseSemver returns false.
func parseSemver(v string) (_ semversion, ok bool) {
	var parsed semversion
	v, parsed.pre, _ = strings.Cut(v, "-")
	if _, err := fmt.Sscanf(v, "v%d.%d.%d", &parsed.major, &parsed.minor, &parsed.patch); err == nil {
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

		if majorMinorPatch[semv.major] == nil {
			majorMinorPatch[semv.major] = map[int]map[int]bool{}
		}
		if majorMinorPatch[semv.major][semv.minor] == nil {
			majorMinorPatch[semv.major][semv.minor] = map[int]bool{}
		}
		majorMinorPatch[semv.major][semv.minor][semv.patch] = true
	}

	var possible []string
	seen := map[string]bool{}
	for _, v := range semVersions {
		nextMajor := fmt.Sprintf("v%d.%d.%d", v.major+1, 0, 0)
		if _, ok := majorMinorPatch[v.major+1]; !ok && !seen[nextMajor] {
			seen[nextMajor] = true
			possible = append(possible, nextMajor)
		}

		nextMinor := fmt.Sprintf("v%d.%d.%d", v.major, v.minor+1, 0)
		if _, ok := majorMinorPatch[v.major][v.minor+1]; !ok && !seen[nextMinor] {
			seen[nextMinor] = true
			possible = append(possible, nextMinor)
		}

		nextPatch := fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch+1)
		if _, ok := majorMinorPatch[v.major][v.minor][v.patch+1]; !ok && !seen[nextPatch] {
			seen[nextPatch] = true
			possible = append(possible, nextPatch)
		}
	}

	semver.Sort(possible)
	return possible, nil
}
