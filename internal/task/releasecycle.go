// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/google/go-github/v48/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/gitfs"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relnote"
	"rsc.io/markdown"
)

// ReleaseCycleTasks implements tasks related to the Go release cycle (go.dev/s/release).
type ReleaseCycleTasks struct {
	Gerrit GerritClient
	GitHub GitHubClientInterface
}

// PromotedAPI holds information about APIs promoted to an api/go1.N.txt file.
type PromotedAPI struct {
	// APIs holds promoted API lines, like
	// "pkg math/big, method (*Rat) FloatPrec() (int, bool) #50489",
	// "pkg iter, func Pull[$0 interface{}](Seq[$0]) (func() ($0, bool), func()) #61897",
	// with no empty lines and no newlines, and in sorted order.
	APIs []string

	// PromotionCL holds the CL number that promoted api/next to api/go1.N.txt.
	PromotionCL int
}

// PromoteNextAPI promotes api under api/next to api/go1.{version}.txt
// by mailing a Gerrit CL that does this and waiting for it to be submitted,
// then returns the promoted API.
//
// version is a value like 24 representing that Go 1.24 is the major version which
// is entering release freeze, in anticipation of pre-release versions and eventual release versions.
func (t ReleaseCycleTasks) PromoteNextAPI(ctx *wf.TaskContext, version int, reviewers []string) (PromotedAPI, error) {
	// Read branch head.
	commit, err := t.Gerrit.ReadBranchHead(ctx, "go", "master")
	if err != nil {
		return PromotedAPI{}, err
	}
	ctx.Printf("Using commit %q as the branch head.", commit)

	// Read api/next files at commit.
	var files = make(map[string]string)
	des, err := t.Gerrit.ReadDir(ctx, "go", commit, "api/next")
	if err != nil {
		return PromotedAPI{}, err
	}
	var promoted PromotedAPI
	for _, de := range des {
		if ext := path.Ext(de.Name); ext != ".txt" {
			return PromotedAPI{}, fmt.Errorf("file %q in api/next has a non-.txt extension %q", de.Name, ext)
		}
		b, err := t.Gerrit.ReadFile(ctx, "go", commit, path.Join("api/next", de.Name))
		if err != nil {
			return PromotedAPI{}, err
		}
		// TODO(dmitshur): After Go 1.24, consider simplifying bytes.CutSuffix(…, []byte("\n")) + bytes.Split(…, []byte("\n")) in favor of iterating over bytes.Lines or so.
		b, ok := bytes.CutSuffix(b, []byte("\n"))
		if !ok {
			return PromotedAPI{}, fmt.Errorf("API file %s doesn't have a trailing newline", de.Name)
		}
		for _, l := range bytes.Split(b, []byte("\n")) {
			if len(l) == 0 {
				return PromotedAPI{}, fmt.Errorf("API file %s has a blank line", de.Name)
			}
			promoted.APIs = append(promoted.APIs, string(l))
		}

		files[path.Join("api/next", de.Name)] = "" // Delete the file.
	}
	slices.Sort(promoted.APIs)
	var buf strings.Builder
	for _, api := range promoted.APIs {
		fmt.Fprintln(&buf, api)
	}
	files[path.Join("api", fmt.Sprintf("go1.%d.txt", version))] = buf.String()

	// Beyond this point we want retries to be done manually, not automatically.
	ctx.DisableRetries()

	// Create the promotion CL and await its submission.
	changeID, err := t.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "go", Branch: "master",
		Subject: fmt.Sprintf("api: promote next to go1.%d", version),
	}, reviewers, files)
	if err != nil {
		return PromotedAPI{}, err
	}
	ctx.Printf("Awaiting review/submit of %v.", ChangeLink(changeID))
	if _, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return t.Gerrit.Submitted(ctx, changeID, "")
	}); err != nil {
		return PromotedAPI{}, err
	}
	ctx.Printf("The api/next fragments were promoted to api/go1.%d.txt in %v.", version, ChangeLink(changeID))
	clNumber, err := strconv.Atoi(strings.TrimPrefix(changeID, "go~"))
	if err != nil {
		return PromotedAPI{}, err
	}
	promoted.PromotionCL = clNumber

	return promoted, nil
}

func (t ReleaseCycleTasks) OpenAPIAuditIssue(ctx *wf.TaskContext, version int, nextAPI PromotedAPI) (openedIssue int, _ error) {
	// TODO(go.dev/issue/70655): Determine programmatically.
	const (
		devVerMilestone = 322 // Go1.24 milestone (https://github.com/golang/go/milestone/322).
	)

	// Parse individual lines into sorted groups by package.
	parseLine := func(line string) (pkg, feature string, proposal int, _ error) {
		pkgAndFeature, proposalStr, ok := strings.Cut(line, " #")
		if !ok {
			return "", "", 0, fmt.Errorf("no ' #'")
		}
		proposal, err := strconv.Atoi(proposalStr)
		if err != nil {
			return "", "", 0, fmt.Errorf("parsing %q to an int: %v", proposalStr, err)
		} else if proposal <= 0 {
			return "", "", 0, fmt.Errorf("non-positive proposal %d", proposal)
		}
		pkgAndFeature, ok = strings.CutPrefix(pkgAndFeature, "pkg ")
		if !ok {
			return "", "", 0, fmt.Errorf("no 'pkg ' prefix")
		}
		pkg, feature, ok = strings.Cut(pkgAndFeature, ", ")
		if !ok {
			return "", "", 0, fmt.Errorf("no ', ' after package")
		}
		if pkg == "" {
			return "", "", 0, fmt.Errorf("package is empty string")
		} else if feature == "" {
			return "", "", 0, fmt.Errorf("feature is empty string")
		}
		return pkg, feature, proposal, nil
	}
	type APIAndProposal struct {
		API      string
		Proposal int
	}
	var byPackage = make(map[string][]APIAndProposal)
	for _, line := range nextAPI.APIs {
		pkg, feature, proposal, err := parseLine(line)
		if err != nil {
			return 0, fmt.Errorf("line %q has a problem: %v", line, err)
		}
		byPackage[pkg] = append(byPackage[pkg], APIAndProposal{feature, proposal})
	}
	type PackageAndAPI struct {
		Package string
		APIs    []APIAndProposal
	}
	var nextAPIByPackage []PackageAndAPI
	for pkg, features := range byPackage {
		slices.SortFunc(features, func(a, b APIAndProposal) int { return strings.Compare(a.API, b.API) })
		nextAPIByPackage = append(nextAPIByPackage, PackageAndAPI{
			Package: pkg,
			APIs:    features,
		})
	}
	slices.SortFunc(nextAPIByPackage, func(a, b PackageAndAPI) int { return strings.Compare(a.Package, b.Package) })

	// Beyond this point we want retries to be done manually, not automatically.
	ctx.DisableRetries()

	// Create the API audit issue.
	tmpl := texttemplate.Must(texttemplate.New("").
		Parse(`This is a tracking issue for doing an audit of API additions for Go 1.{{.Version}} as of [CL {{.PromotionCL}}](https://go.dev/cl/{{.PromotionCL}}).

## New API changes for Go 1.{{.Version}}
{{range .NextAPIByPackage}}
### {{.Package}}
{{range .APIs}}
- ` + "`" + `{{.API}}` + "`" + ` #{{.Proposal}}{{end}}
{{end}}
CC @aclements, @ianlancetaylor, @golang/release.`))
	title := fmt.Sprintf("api: audit for Go 1.%d", version)
	var body bytes.Buffer
	if err := tmpl.Execute(&body, map[string]any{
		"Version":          version,
		"PromotionCL":      nextAPI.PromotionCL,
		"NextAPIByPackage": nextAPIByPackage,
	}); err != nil {
		return 0, err
	}
	issue, _, err := t.GitHub.CreateIssue(ctx, "golang", "go", &github.IssueRequest{
		Title:     github.String(title),
		Body:      github.String(body.String()),
		Labels:    &[]string{"NeedsDecision", "release-blocker", "ExpertNeeded"},
		Milestone: github.Int(devVerMilestone),
	})
	if err != nil {
		return 0, err
	}

	return issue.GetNumber(), nil
}

// NextRelnote holds information about merged release notes.
type NextRelnote struct {
	AddMergedToWebsiteCL int // CL number that added merged release notes to x/website.
}

// MergeNextRelnoteAndAddToWebsite merges the release fragments found in
// doc/next of the main Go repository, and adds the merged release notes
// to _content/doc of the x/website repository.
func (t ReleaseCycleTasks) MergeNextRelnoteAndAddToWebsite(ctx *wf.TaskContext, version int, reviewers []string) (NextRelnote, error) {
	// TODO(go.dev/issue/70655): Determine programmatically.
	const (
		releaseNotesIssue = 68545 // "doc: write release notes for Go 1.24"
	)

	// Read branch head.
	goRepo, err := gitfs.NewRepo(t.Gerrit.GitilesURL() + "/" + "go")
	if err != nil {
		return NextRelnote{}, err
	}
	commit, err := goRepo.Resolve("refs/heads/master")
	if err != nil {
		return NextRelnote{}, err
	}
	ctx.Printf("Using commit %q as the branch head.", commit)

	// Collect all doc/next files to merge.
	root, err := goRepo.CloneHash(commit)
	if err != nil {
		return NextRelnote{}, err
	}
	next, err := fs.Sub(root, "doc/next")
	if err != nil {
		return NextRelnote{}, err
	}
	doc, err := relnote.Merge(next)
	if err != nil {
		return NextRelnote{}, fmt.Errorf("relnote.Merge: %v", err)
	}
	mergedRelnote := fmt.Sprintf(`---
title: Go 1.%d Release Notes
template: false
---

`, version) + markdown.ToMarkdown(doc)

	// Beyond this point we want retries to be done manually, not automatically.
	ctx.DisableRetries()

	// Create the add-merged CL and await its submission.
	changeID, err := t.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "website", Branch: "master",
		Subject: fmt.Sprintf(`_content/doc: add merged go1.%d.md

Using doc/next content as of %s (commit %s).

For golang/go#%d.`, version, time.Now().Format(time.DateOnly), commit, releaseNotesIssue),
	}, reviewers, map[string]string{
		fmt.Sprintf("_content/doc/go1.%d.md", version): mergedRelnote,
	})
	if err != nil {
		return NextRelnote{}, err
	}
	ctx.Printf("Awaiting review/submit of %v.", ChangeLink(changeID))
	if _, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return t.Gerrit.Submitted(ctx, changeID, "")
	}); err != nil {
		return NextRelnote{}, err
	}
	clNumber, err := strconv.Atoi(strings.TrimPrefix(changeID, "website~"))
	if err != nil {
		return NextRelnote{}, err
	}

	return NextRelnote{AddMergedToWebsiteCL: clNumber}, nil
}

// RemoveNextRelnoteFromMainRepo removes release note fragments
// from doc/next in the main Go repository.
func (t ReleaseCycleTasks) RemoveNextRelnoteFromMainRepo(ctx *wf.TaskContext, version int, nr NextRelnote, reviewers []string) error {
	// TODO(go.dev/issue/70655): Determine programmatically.
	const (
		releaseNotesIssue = 68545 // "doc: write release notes for Go 1.24"
	)

	// Read branch head.
	goRepo, err := gitfs.NewRepo(t.Gerrit.GitilesURL() + "/" + "go")
	if err != nil {
		return err
	}
	commit, err := goRepo.Resolve("refs/heads/master")
	if err != nil {
		return err
	}
	ctx.Printf("Using commit %q as the branch head.", commit)

	// Collect all doc/next files to delete.
	root, err := goRepo.CloneHash(commit)
	if err != nil {
		return err
	}
	var files = make(map[string]string)
	err = fs.WalkDir(root, "doc/next", func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !de.IsDir() {
			files[path] = "" // Delete the file.
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Beyond this point we want retries to be done manually, not automatically.
	ctx.DisableRetries()

	// Create the remove-fragments CL and await its submission.
	changeID, err := t.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "go", Branch: "master",
		Subject: fmt.Sprintf(`doc/next: delete

The release note fragments have been merged and added
as _content/doc/go1.%d.md in x/website in CL %d.

For #%d.`, version, nr.AddMergedToWebsiteCL, releaseNotesIssue),
	}, reviewers, files)
	if err != nil {
		return err
	}
	ctx.Printf("Awaiting review/submit of %v.", ChangeLink(changeID))
	if _, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return t.Gerrit.Submitted(ctx, changeID, "")
	}); err != nil {
		return err
	}
	ctx.Printf("Release note fragments were removed in %v.", ChangeLink(changeID))

	return nil
}

// ApplyWaitReleaseCLs applies a "wait-release" hashtag to remaining open CLs that are
// adding new APIs and should wait for next release. This is done once at the start of
// the release freeze.
func (t ReleaseCycleTasks) ApplyWaitReleaseCLs(ctx *wf.TaskContext) (result struct{}, _ error) {
	clsToWait, err := t.Gerrit.QueryChanges(ctx, "repo:go status:open -is:wip dir:api/next -hashtag:wait-release")
	if err != nil {
		return struct{}{}, err
	}
	ctx.Printf("Processing %d open Gerrit CLs to be marked with wait-release hashtag.", len(clsToWait))
	for _, cl := range clsToWait {
		const dryRun = false
		if dryRun {
			ctx.Printf("[dry run] Would've waited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
			continue
		}
		err := t.Gerrit.SetHashtags(ctx, cl.ID, gerrit.HashtagsInput{Add: []string{"wait-release"}})
		if err != nil {
			return struct{}{}, err
		}
		ctx.Printf("Waited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
		time.Sleep(3 * time.Second) // Take a moment between updating CLs to avoid a high rate of modify operations.
	}
	return struct{}{}, nil
}

// UnwaitWaitReleaseCLs changes all open Gerrit CLs with hashtag "wait-release" into "ex-wait-release".
// This is done once at the opening of a release cycle, currently via a standalone workflow.
func (t ReleaseCycleTasks) UnwaitWaitReleaseCLs(ctx *wf.TaskContext) (result struct{}, _ error) {
	waitingCLs, err := t.Gerrit.QueryChanges(ctx, "status:open hashtag:wait-release")
	if err != nil {
		return struct{}{}, err
	}
	ctx.Printf("Processing %d open Gerrit CL with wait-release hashtag.", len(waitingCLs))
	for _, cl := range waitingCLs {
		const dryRun = false
		if dryRun {
			ctx.Printf("[dry run] Would've unwaited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
			continue
		}
		err := t.Gerrit.SetHashtags(ctx, cl.ID, gerrit.HashtagsInput{
			Remove: []string{"wait-release"},
			Add:    []string{"ex-wait-release"},
		})
		if err != nil {
			return struct{}{}, err
		}
		ctx.Printf("Unwaited CL %d (%.32s…).", cl.ChangeNumber, cl.Subject)
		time.Sleep(3 * time.Second) // Take a moment between updating CLs to avoid a high rate of modify operations.
	}
	return struct{}{}, nil
}
