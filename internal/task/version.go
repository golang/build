// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

// VersionTasks contains tasks related to versioning the release.
type VersionTasks struct {
	Gerrit     GerritClient
	CloudBuild CloudBuildClient
	GoProject  string
	GoDirectiveXReposTasks
	UpdateProxyTestRepoTasks
}

// GetCurrentMajor returns the most recent major Go version, and the time at
// which its tag was created.
func (t *VersionTasks) GetCurrentMajor(ctx context.Context) (int, time.Time, error) {
	_, currentMajor, currentMajorTag, err := t.tagInfo(ctx)
	if err != nil {
		return 0, time.Time{}, err
	}
	info, err := t.Gerrit.GetTag(ctx, t.GoProject, currentMajorTag)
	if err != nil {
		return 0, time.Time{}, err
	}
	return currentMajor, info.Created.Time(), nil
}

func (t *VersionTasks) tagInfo(ctx context.Context) (tags map[string]bool, currentMajor int, currentMajorTag string, _ error) {
	tagList, err := t.Gerrit.ListTags(ctx, t.GoProject)
	if err != nil {
		return nil, 0, "", err
	}
	tags = map[string]bool{}
	for _, tag := range tagList {
		tags[tag] = true
	}
	// Find the most recently released major version.
	// Going down from a high number is convenient for testing.
	for currentMajor := 100; currentMajor > 0; currentMajor-- {
		base := fmt.Sprintf("go1.%d", currentMajor)
		// Handle either go1.20 or go1.21.0.
		for _, tag := range []string{base, base + ".0"} {
			if tags[tag] {
				return tags, currentMajor, tag, nil
			}
		}
	}
	return nil, 0, "", fmt.Errorf("couldn't find the most recently released major version out of %d tags", len(tagList))
}

// GetNextMinorVersions returns the next minor for each of the given major series.
// It uses the same format as Go tags (for example, "go1.23.4").
func (t *VersionTasks) GetNextMinorVersions(ctx context.Context, majors []int) ([]string, error) {
	var next []string
	for _, major := range majors {
		n, err := t.GetNextVersion(ctx, major, KindMinor)
		if err != nil {
			return nil, err
		}
		next = append(next, n)
	}
	return next, nil
}

// GetNextVersion returns the next for the given major series and kind of release.
// It uses the same format as Go tags (for example, "go1.23.4").
func (t *VersionTasks) GetNextVersion(ctx context.Context, major int, kind ReleaseKind) (string, error) {
	tags, _, _, err := t.tagInfo(ctx)
	if err != nil {
		return "", err
	}
	findUnused := func(v string) (string, error) {
		for {
			if !tags[v] {
				return v, nil
			}
			v, err = nextVersion(v)
			if err != nil {
				return "", err
			}
		}
	}
	switch kind {
	case KindMinor:
		return findUnused(fmt.Sprintf("go1.%d.1", major))
	case KindBeta:
		return findUnused(fmt.Sprintf("go1.%dbeta1", major))
	case KindRC:
		return findUnused(fmt.Sprintf("go1.%drc1", major))
	case KindMajor:
		return fmt.Sprintf("go1.%d.0", major), nil
	default:
		return "", fmt.Errorf("unknown release kind %v", kind)
	}
}

// GetDevelVersion returns the current major Go 1.x version in development.
//
// This value is determined by reading the value of the Version constant in
// the internal/goversion package of the main Go repository at HEAD commit.
func (t *VersionTasks) GetDevelVersion(ctx context.Context) (int, error) {
	mainBranch, err := t.Gerrit.ReadBranchHead(ctx, t.GoProject, "HEAD")
	if err != nil {
		return 0, err
	}
	tipCommit, err := t.Gerrit.ReadBranchHead(ctx, t.GoProject, mainBranch)
	if err != nil {
		return 0, err
	}
	// Fetch the goversion.go file, extract the declaration from the parsed AST.
	//
	// This is a pragmatic approach that relies on the trajectory of the
	// internal/goversion package being predictable and unlikely to change.
	// If that stops being true, this implementation is easy to re-write.
	const goversionPath = "src/internal/goversion/goversion.go"
	b, err := t.Gerrit.ReadFile(ctx, t.GoProject, tipCommit, goversionPath)
	if errors.Is(err, gerrit.ErrResourceNotExist) {
		return 0, fmt.Errorf("did not find goversion.go file (%v); possibly the internal/goversion package changed (as it's permitted to)", err)
	} else if err != nil {
		return 0, err
	}
	f, err := parser.ParseFile(token.NewFileSet(), goversionPath, b, 0)
	if err != nil {
		return 0, err
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].String() != "Version" || len(v.Values) != 1 {
				continue
			}
			l, ok := v.Values[0].(*ast.BasicLit)
			if !ok || l.Kind != token.INT {
				continue
			}
			return strconv.Atoi(l.Value)
		}
	}
	return 0, fmt.Errorf("did not find Version declaration in %s; possibly the internal/goversion package changed (as it's permitted to)", goversionPath)
}

func nextVersion(version string) (string, error) {
	lastNonDigit := strings.LastIndexFunc(version, func(r rune) bool {
		return r < '0' || r > '9'
	})
	if lastNonDigit == -1 || len(version) == lastNonDigit {
		return "", fmt.Errorf("malformatted Go version %q", version)
	}
	n, err := strconv.Atoi(version[lastNonDigit+1:])
	if err != nil {
		return "", fmt.Errorf("malformatted Go version %q (%v)", version, err)
	}
	return fmt.Sprintf("%s%d", version[:lastNonDigit+1], n+1), nil
}

func (t *VersionTasks) GenerateVersionFile(_ *workflow.TaskContext, version string, timestamp time.Time) (string, error) {
	return fmt.Sprintf("%v\ntime %v\n", version, timestamp.Format(time.RFC3339)), nil
}

// CreateAutoSubmitVersionCL mails an auto-submit change to update VERSION file on branch.
func (t *VersionTasks) CreateAutoSubmitVersionCL(ctx *workflow.TaskContext, branch, version string, reviewers []string, versionFile string) (string, error) {
	return t.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: t.GoProject,
		Branch:  branch,
		Subject: fmt.Sprintf("[%v] %v", branch, version),
	}, reviewers, map[string]string{
		"VERSION": versionFile,
	})
}

// AwaitCL waits for the specified CL to be submitted, and returns the new
// branch head. Callers can pass baseCommit, the current branch head, to verify
// that no CLs were submitted between when the CL was created and when it was
// merged. If changeID is blank because the intended CL was a no-op, baseCommit
// is returned immediately.
func (t *VersionTasks) AwaitCL(ctx *workflow.TaskContext, changeID, baseCommit string) (string, error) {
	if changeID == "" {
		ctx.Printf("No CL was necessary")
		return baseCommit, nil
	}

	ctx.Printf("Awaiting review/submit of %v", ChangeLink(changeID))
	return AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return t.Gerrit.Submitted(ctx, changeID, baseCommit)
	})
}

// ReadBranchHead returns the current HEAD revision of branch.
func (t *VersionTasks) ReadBranchHead(ctx *workflow.TaskContext, branch string) (string, error) {
	return t.Gerrit.ReadBranchHead(ctx, t.GoProject, branch)
}

// TagRelease tags commit as version.
func (t *VersionTasks) TagRelease(ctx *workflow.TaskContext, version, commit string) error {
	return t.Gerrit.Tag(ctx, t.GoProject, version, commit)
}

func (t *VersionTasks) CreateUpdateStdlibIndexCL(ctx *workflow.TaskContext, reviewers []string, version string) (string, error) {
	return t.CloudBuild.GenerateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "tools",
		Subject: fmt.Sprintf(`internal/stdlib: update stdlib index for %s

For golang/go#38706.

[git-generate]
go generate ./internal/stdlib
`, strings.NewReplacer("go", "Go ", "rc", " Release Candidate ").Replace(version)),
		Branch: "master",
	}, reviewers)
}
