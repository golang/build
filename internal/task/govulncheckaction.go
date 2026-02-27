// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v74/github"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
)

// ReleaseGovulncheckActionTasks provides workflow definitions and tasks for
// tagging the govulncheck-action repository.
type ReleaseGovulncheckActionTasks struct {
	Gerrit        DestructiveGerritClient // destructive needed to overwrite moving major tag (e.g., "v1")
	GitHub        GitHubClientInterface
	ApproveAction func(*wf.TaskContext) error
}

// NewDefinition returns a workflow definition for releasing a new version of
// the govulncheck-action repository.
func (x *ReleaseGovulncheckActionTasks) NewDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.SecurityTeam}})
	version := wf.Param(wd, wf.ParamDef[string]{
		Name:    "Version",
		Example: "v1.0.0",
		Check:   CheckSemver,
	})

	latest := wf.Task0(wd, "find latest version", x.findLatestVersion)
	validated := wf.Action2(wd, "validate version", x.validateVersion, version, latest)
	commit := wf.Task2(wd, "read branch head", x.Gerrit.ReadBranchHead, wf.Const("govulncheck-action"), wf.Const("master"))
	approved := wf.Action3(wd, "wait for approval", x.approveTagging, version, commit, latest, wf.After(validated))
	tag := wf.Action2(wd, "tag repository", x.tagRepository, version, commit, wf.After(approved))
	majorTag := wf.Action2(wd, "update major tag", x.updateMajorTag, version, commit, wf.After(tag))
	awaitTag := wf.Action1(wd, "wait for GitHub tag sync", x.awaitGitHubTag, version, wf.After(majorTag))
	githubRelease := wf.Action1(wd, "create GitHub release", x.createGitHubRelease, version, wf.After(awaitTag))

	done := wf.Task1(wd, "await GitHub release", func(_ context.Context, v string) (string, error) {
		return v, nil
	}, version, wf.After(githubRelease))
	wf.Output(wd, "tagged version", done)
	return wd
}

func (x *ReleaseGovulncheckActionTasks) validateVersion(ctx *wf.TaskContext, new, latest string) error {
	if latest == "" {
		return nil
	}
	if semver.Compare(new, latest) <= 0 {
		return fmt.Errorf("proposed version %q is not greater than latest version %q", new, latest)
	}
	majorNew, minorNew, patchNew, err := parseSemver(new)
	if err != nil {
		return err
	}
	majorLatest, minorLatest, patchLatest, err := parseSemver(latest)
	if err != nil {
		return err
	}

	if majorNew == majorLatest+1 {
		if minorNew != 0 || patchNew != 0 {
			return fmt.Errorf("proposed major jump %q must reset minor and patch to 0", new)
		}
		return nil
	}

	if majorNew == majorLatest {
		if minorNew == minorLatest+1 {
			if patchNew != 0 {
				return fmt.Errorf("proposed minor jump %q must reset patch to 0", new)
			}
			return nil
		}
		if minorNew == minorLatest {
			if patchNew != patchLatest+1 {
				return fmt.Errorf("proposed patch %q is not exactly %d.%d.%d", new, majorLatest, minorLatest, patchLatest+1)
			}
			return nil
		}
		return fmt.Errorf("proposed minor version %d is not valid (latest minor is %d)", minorNew, minorLatest)
	}

	return fmt.Errorf("proposed major version %q is not valid (latest major is %q)", semver.Major(new), semver.Major(latest))
}

func parseSemver(v string) (major, minor, patch int, err error) {
	// Trim any pre-release or build metadata for parsing base components
	base := v
	if i := strings.IndexAny(v, "-+"); i != -1 {
		base = v[:i]
	}
	if _, err = fmt.Sscanf(base, "v%d.%d.%d", &major, &minor, &patch); err != nil {
		return 0, 0, 0, fmt.Errorf("invalid semver %q: %v", v, err)
	}
	return major, minor, patch, nil
}

func parseMajor(v string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(v, "v%d", &n); err != nil {
		return 0, fmt.Errorf("invalid major version %q: %v", v, err)
	}
	return n, nil
}

func (x *ReleaseGovulncheckActionTasks) findLatestVersion(ctx *wf.TaskContext) (string, error) {
	tags, err := x.Gerrit.ListTags(ctx, "govulncheck-action")
	if err != nil {
		return "", err
	}
	latest := ""
	for _, t := range tags {
		if !semver.IsValid(t) {
			continue
		}
		if latest == "" || semver.Compare(t, latest) > 0 {
			latest = t
		}
	}
	if latest == "" {
		ctx.Printf("No existing tags found.")
	} else {
		ctx.Printf("Latest version found: %s", latest)
	}
	return latest, nil
}

func (x *ReleaseGovulncheckActionTasks) approveTagging(ctx *wf.TaskContext, version string, commit string, latest string) error {
	if latest != "" {
		ctx.Printf("Latest version tag: %s", latest)
	} else {
		ctx.Printf("No previous version found.")
	}
	ctx.Printf("Tagging govulncheck-action as %s at commit %s.\n", version, commit)
	ctx.Printf("This action requires approval.")
	return x.ApproveAction(ctx)
}

func (x *ReleaseGovulncheckActionTasks) tagRepository(ctx *wf.TaskContext, version string, commit string) error {
	ctx.Printf("Tagging govulncheck-action as %s at commit %s", version, commit)
	ctx.DisableRetries()
	return x.Gerrit.Tag(ctx, "govulncheck-action", version, commit)
}

func (x *ReleaseGovulncheckActionTasks) updateMajorTag(ctx *wf.TaskContext, version string, commit string) error {
	major := semver.Major(version)
	if major == "" {
		return fmt.Errorf("invalid version %q: no major version", version)
	}
	ctx.Printf("Updating major tag %s to point to %s", major, commit)
	ctx.DisableRetries() // Manual retries beyond this point.
	return x.Gerrit.ForceTag(ctx, "govulncheck-action", major, commit)
}

func (x *ReleaseGovulncheckActionTasks) awaitGitHubTag(ctx *wf.TaskContext, tag string) error {
	ctx.Printf("Waiting for tag %s to be mirrored to GitHub", tag)
	tctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	_, err := AwaitCondition(ctx, 15*time.Second, func() (string, bool, error) {
		exists, err := x.GitHub.TagExists(tctx, "golang", "govulncheck-action", tag)
		return "", exists, err
	})
	return err
}

func (x *ReleaseGovulncheckActionTasks) createGitHubRelease(ctx *wf.TaskContext, version string) error {
	ctx.DisableRetries()
	ctx.Printf("Creating GitHub release for %s", version)
	_, err := x.GitHub.CreateRelease(ctx, "golang", "govulncheck-action", &github.RepositoryRelease{
		TagName:              github.Ptr(version),
		Name:                 github.Ptr(version),
		GenerateReleaseNotes: github.Ptr(true),
		Draft:                github.Ptr(false),
	})
	return err
}

// CheckSemver reports whether v is a valid semantic version.
func CheckSemver(v string) error {
	if !semver.IsValid(v) {
		return fmt.Errorf("invalid semver: %q", v)
	}
	return nil
}
