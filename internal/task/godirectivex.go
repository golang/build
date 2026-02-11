// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"
	goversion "go/version"
	"io/fs"
	pathpkg "path"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/gitfs"
	wf "golang.org/x/build/internal/workflow"
	repospkg "golang.org/x/build/repos"
	"golang.org/x/mod/modfile"
)

type GoDirectiveXReposTasks struct {
	ForceRepos []string // Optional slice to override SelectRepos behavior, if non-nil, intended for tests.

	Gerrit     GerritClient
	CloudBuild CloudBuildClient
}

func (x GoDirectiveXReposTasks) SelectRepos(ctx *wf.TaskContext) ([]string, error) {
	if x.ForceRepos != nil {
		return x.ForceRepos, nil
	}
	var repos []string
	for importPath, r := range repospkg.ByImportPath {
		if !strings.HasPrefix(importPath, "golang.org/x/") {
			ctx.Printf("Skipping %s because it's not a golang.org/x repo.", importPath)
			continue
		} else if !r.AutoMaintainGoDirective {
			ctx.Printf("Skipping %s because its go directive maintenance is disabled.", importPath)
			continue
		}
		repos = append(repos, r.GoGerritProject)
	}
	return repos, nil
}

// BuildPlan adds the tasks needed to maintain repos to wd.
func (x GoDirectiveXReposTasks) BuildPlan(wd *wf.Definition, repos []string, goVer int, reviewers []string) (wf.Value[[]string], error) {
	var changeIDs []wf.Value[string]
	for _, r := range repos {
		changeIDs = append(changeIDs, wf.Task3(wd, "Maintain x/"+r+" go directive, mail CL", x.MaintainGoDirectiveAndMailCL, wf.Const(r), wf.Const(goVer), wf.Const(reviewers)))
	}
	urls := wf.Task1(wd, "Await submission of x/ repo go directive CLs", x.AwaitSubmissions, wf.Slice(changeIDs...))
	return urls, nil
}

// MaintainGoDirectiveAndMailCL mails a CL that performs go directive maintenance for
// the specified repository. repo must be a Gerrit project holding a golang.org/x module.
// goVer is a number like 24, when Go 1.24.0 is the most recently released major Go release.
//
// See go.dev/issue/69095 and go.dev/design/69095-x-repo-continuous-go for details.
func (x GoDirectiveXReposTasks) MaintainGoDirectiveAndMailCL(ctx *wf.TaskContext, repo string, goVer int, reviewers []string) (changeID string, _ error) {
	prevGoVer := goVer - 1 // See https://go.dev/design/69095-x-repo-continuous-go#why-1_n_1_0.

	// Maintain the go directive in the root module and nested modules.
	// Dynamically find the modules and create the git-generate script.
	gitRepo, err := gitfs.NewRepo(x.Gerrit.GitilesURL() + "/" + repo)
	if err != nil {
		return "", err
	}
	const branch = "master"
	head, err := gitRepo.Resolve("refs/heads/" + branch)
	if err != nil {
		return "", err
	}
	ctx.Printf("Using commit %q as the branch %q head.", head, branch)
	rootFS, err := gitRepo.CloneHash(head)
	if err != nil {
		return "", err
	}
	var (
		script strings.Builder
		needCL bool
	)
	if err := fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != "." && d.IsDir() && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") || d.Name() == "testdata") {
			// Skip directories that begin with ".", "_", or are named "testdata".
			return fs.SkipDir
		}
		if d.Name() == "go.mod" && !d.IsDir() { // A go.mod file.
			dir := pathpkg.Dir(path)

			b, err := fs.ReadFile(rootFS, path)
			if err != nil {
				return err
			}
			f, err := modfile.Parse(path, b, nil)
			if err != nil {
				return err
			}

			if !strings.HasPrefix(f.Module.Mod.Path, "golang.org/x/") {
				// This shouldn't happen because repo is expected to be a Gerrit project for a golang.org/x module.
				return fmt.Errorf("unexpectedly ran into a non-'golang.org/x' module %q defined in %s of project %s", f.Module.Mod.Path, path, repo)
			}
			if f.Go != nil && goversion.Compare("go"+f.Go.Version, fmt.Sprintf("go1.%d.0", prevGoVer)) >= 0 {
				fmt.Fprintf(&script, "(cd %v && echo 'skipping because it already has go%s >= go1.%d.0, nothing to do')\n", dir, f.Go.Version, prevGoVer)
				ctx.Printf("Skipping module %s because it already has go%s >= go1.%d.0, nothing to do.", f.Module.Mod.Path, f.Go.Version, prevGoVer)
				return nil
			}

			fmt.Fprintf(&script, "(cd %v && go get go@1.%d.0 && go mod tidy)\n", dir, prevGoVer)
			needCL = true
		}
		return nil
	}); err != nil {
		return "", err
	}
	ctx.Printf("git-generate script:\n%s<EOF>", script.String())
	if !needCL {
		ctx.Printf("Skipping CL since all modules are up to date, nothing to do.")
		return "", nil
	}

	// Generate and mail the CL that will update files.
	ctx.DisableRetries()
	return x.CloudBuild.GenerateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: repo,
		Branch:  branch,
		Subject: fmt.Sprintf(`all: upgrade go directive to at least 1.%d.0 [generated]

By now Go 1.%d.0 has been released, and Go 1.%d is no longer supported
per the Go Release Policy (see https://go.dev/doc/devel/release#policy).

See https://go.dev/doc/godebug#go-1%[1]d for GODEBUG setting changes
relevant to Go 1.%[1]d.

For golang/go#69095.

[git-generate]
%[4]s`, prevGoVer, goVer, goVer-2, script.String()),
	}, reviewers)
}

// AwaitSubmissions waits for the CLs with the given change IDs to be all submitted.
// The empty string change ID means no CL, and gets skipped. It returns change URLs.
func (x GoDirectiveXReposTasks) AwaitSubmissions(ctx *wf.TaskContext, changeIDs []string) (urls []string, _ error) {
	for _, cl := range changeIDs {
		url, err := x.awaitSubmission(ctx, cl)
		if err != nil {
			return nil, err
		}
		if url == "" {
			continue
		}
		urls = append(urls, url)
	}
	return urls, nil
}

// awaitSubmission waits for the CL with the given change ID to be submitted.
// The return value is the URL of the CL, or "" if changeID is "".
func (x GoDirectiveXReposTasks) awaitSubmission(ctx *wf.TaskContext, changeID string) (url string, _ error) {
	if changeID == "" {
		ctx.Printf("No CL was necessary")
		return "", nil
	}

	ctx.Printf("Awaiting review/submit of %v", ChangeLink(changeID))
	return AwaitCondition(ctx, time.Minute, func() (string, bool, error) {
		_, ok, err := x.Gerrit.Submitted(ctx, changeID, "")
		return ChangeLink(changeID), ok, err
	})
}
