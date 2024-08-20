// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"

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
	ApproveAction func(*wf.TaskContext) error
}

var nextVersionParam = wf.ParamDef[string]{
	Name: "next version",
	ParamType: workflow.ParamType[string]{
		HTMLElement: "select",
		HTMLSelectOptions: []string{
			"next minor",
			"next patch",
		},
	},
}

// NewPrereleaseDefinition create a new workflow definition for vscode-go pre-release.
func (r *ReleaseVSCodeGoTasks) NewPrereleaseDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ToolsTeam}})

	versionBumpStrategy := wf.Param(wd, nextVersionParam)

	version := wf.Task1(wd, "find the next pre-release version", r.nextPrereleaseVersion, versionBumpStrategy)
	_ = wf.Action1(wd, "await release coordinator's approval", r.approveVersion, version)

	return wd
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

	semv := lastReleasedVersion(tags, true)
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

func lastReleasedVersion(versions []string, onlyStable bool) semversion {
	latest := semversion{}
	for _, v := range versions {
		semv, ok := parseSemver(v)
		if !ok {
			continue
		}

		if semv.Pre != "" {
			continue
		}

		if semv.Minor%2 == 0 && onlyStable {
			if semv.Minor > latest.Minor {
				latest = semv
			}

			if semv.Minor == latest.Minor && semv.Patch > latest.Patch {
				latest = semv
			}
		}

		if semv.Minor%2 == 1 && !onlyStable {
			if semv.Minor > latest.Minor {
				latest = semv
			}

			if semv.Minor == latest.Minor && semv.Patch > latest.Patch {
				latest = semv
			}
		}
	}

	return latest
}

func (r *ReleaseVSCodeGoTasks) approveVersion(ctx *wf.TaskContext, semv semversion) error {
	ctx.Printf("The next release candidate will be v%v.%v.%v-%s", semv.Major, semv.Minor, semv.Patch, semv.Pre)
	return r.ApproveAction(ctx)
}
