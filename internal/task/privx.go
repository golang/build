// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-github/v74/github"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relmeta"
	"golang.org/x/mod/semver"
	"golang.org/x/vulndb/report"
)

type PrivXPatch struct {
	Git           *Git
	PublicGerrit  GerritClient
	PrivateGerrit GerritClient

	GitHub             GitHubClientInterface
	ApproveAction      func(*wf.TaskContext) error
	SendMail           func(*wf.TaskContext, MailHeader, MailContent) error
	AnnounceMailHeader MailHeader
	AwaitAnnounceMail  func(*wf.TaskContext, SentMail) (string, error)
}

func (x *PrivXPatch) NewDefinition(tagx *TagXReposTasks) *wf.Definition {
	var (
		wd = wf.New(wf.ACL{Groups: []string{groups.SecurityTeam}})
		// TODO(nealpatel): SecurityMilestoneParameter says "Go release" which
		// is technically incorrect documentation for the parameter here.
		milestoneNum = wf.Param(wd, SecurityMilestoneParameter)
		targetRepo   = wf.Param(wd, wf.ParamDef[string]{Name: "Repository name", Example: "net"})
		// TODO: probably always want to skip, might make sense to not include this
		skipPostSubmit    = wf.Param(wd, wf.ParamDef[bool]{Name: "Skip post submit result (optional)", ParamType: wf.Bool})
		reviewers         = wf.Param(wd, reviewersParam) // We don't fill this.
		securityReviewers = wf.Param(wd, SecurityReviewersParameter)
	)

	availableRepos := wf.Task0(wd, "Load all repositories", tagx.SelectRepos)

	rm := wf.Task1(wd, "Pull release milestone", x.PullMilestone, milestoneNum)
	patches := wf.Task3(wd, "Get changes for target x repo", x.FilterPatches, rm, targetRepo, availableRepos)
	checkpoint := wf.Task3(wd, "Create checkpoint branch", CreateCheckpoint, wf.Const(x.PrivateGerrit), targetRepo, wf.Const("public"))
	patches = wf.Task3(wd, "Move and rebase all changes per x repo", MoveAndRebaseAll, wf.Const(x.PrivateGerrit), checkpoint, patches)
	patches = wf.Task3(wd, "Waiting for submissions", SubmitPrivateChanges, wf.Const(x.PrivateGerrit), targetRepo, patches)
	securityCommit := wf.Task2(wd, "Read checkpoint head", x.ReadCheckpointHead, targetRepo, checkpoint, wf.After(patches))
	// block for manual review before pushing changes to public
	okayToDisclose := wf.Action0(wd, "Wait to disclose", x.ApproveAction, wf.After(securityCommit)) // TODO(nealpatel): Add warning text
	disclosed := wf.Task3(wd, "Publish changes", x.PublishChanges, targetRepo, checkpoint, securityCommit, wf.After(okayToDisclose))
	submitted := wf.Action2(wd, "Wait for submission of published changes", AwaitSubmitted, wf.Const(x.PublicGerrit), disclosed)
	tagged := wf.Expand4(wd, "Create single-repo plan", tagx.BuildSingleRepoPlan, availableRepos, targetRepo, skipPostSubmit, reviewers, wf.After(submitted))
	vulnerableAt := wf.Task1(wd, "Resolve vulnerable version", x.ResolveVulnerableVersion, tagged)

	// wait for manual approval of the announcement message
	okayToAnnounce := wf.Action0(wd, "Wait to Announce", x.ApproveAction, wf.After(tagged))
	sentMail := wf.Task2(wd, "Mail announcement", x.MailAnnouncement, tagged, rm, wf.After(okayToAnnounce))
	announcementURL := wf.Task1(wd, "await-announcement", x.AwaitAnnounceMail, sentMail)
	wf.Output(wd, "Announcement URL", announcementURL)

	// post-announcement tasks
	updated := wf.Action2(wd, "Update GitHub issues", UpdateGitHubIssues, wf.Const(x.GitHub), rm, wf.After(announcementURL))
	converted := wf.Task6(wd, "Convert internal changelists", ConvertPatchChangelists, wf.Const(x.PrivateGerrit), wf.Const(x.PublicGerrit), targetRepo, rm, patches, securityReviewers, wf.After(announcementURL))
	changeID := wf.Task5(wd, "Create vuln reports", x.CreateVulnReports, converted, vulnerableAt, tagged, announcementURL, securityReviewers, wf.After(updated))
	wf.Output(wd, "File VulnDB Reports", changeID)

	wf.Output(wd, "done", tagged)
	return wd
}

func (x *PrivXPatch) PullMilestone(ctx *wf.TaskContext, milestone string) (*relmeta.ReleaseMilestone, error) {
	// TODO(nealpatel): Is this ceremony?
	rm, err := FetchReleaseMilestone(ctx, x.PrivateGerrit, milestone)
	return &rm, err
}

func (x *PrivXPatch) FilterPatches(ctx *wf.TaskContext, rm *relmeta.ReleaseMilestone, target string, found []TagRepo) (patches []*PatchChanges, _ error) {
	var sps []*relmeta.SecurityPatch
	for _, p := range rm.Patches {
		repo, err := repoName(p.Package)
		if err != nil {
			return nil, err
		}
		if repo != target {
			continue
		}
		if !slices.ContainsFunc(found, func(r TagRepo) bool { return r.Name == repo }) {
			return nil, fmt.Errorf("no repository %q", repo)
		}
		sps = append(sps, p)
	}
	return CheckPrivateChanges(ctx, x.PrivateGerrit, target, sps)
}

// repoName returns the repo implied by the
// modPkg; for example, 'golang.org/x/crypto',
// returns 'crypto' as the repo.
func repoName(modPkg string) (string, error) {
	// TODO(nealpatel): This is brittle. Surely, something more idiomatic.
	pkg, found := strings.CutPrefix(modPkg, "golang.org/x/")
	if !found {
		return "", fmt.Errorf("malformed package: %q", modPkg)
	}
	repo, _, _ := strings.Cut(pkg, "/")
	if repo == "" {
		return "", fmt.Errorf("malformed package: %q", modPkg)
	}
	return repo, nil
}

func (x *PrivXPatch) ReadCheckpointHead(ctx *wf.TaskContext, repoName string, cp Checkpoint) (string, error) {
	return x.PrivateGerrit.ReadBranchHead(ctx, repoName, cp.Branch)
}

func (x *PrivXPatch) ResolveVulnerableVersion(ctx *wf.TaskContext, tagged TagRepo) (*report.Version, error) {
	tags, err := x.PublicGerrit.ListTags(ctx, tagged.Name)
	if err != nil {
		return nil, fmt.Errorf("listing tags for %s: %w", tagged.Name, err)
	}
	cutVersion := tagged.NewerVersion
	if !semver.IsValid(cutVersion) {
		return nil, fmt.Errorf("invalid tagged version: %q", cutVersion)
	}
	var versions []string
	for _, t := range tags {
		if semver.IsValid(t) && semver.Compare(t, cutVersion) < 0 {
			versions = append(versions, t)
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no version tag preceding %s for %s", cutVersion, tagged.Name)
	}
	semver.Sort(versions)
	predecessor := versions[len(versions)-1]
	return report.VulnerableAt(predecessor[1:]), nil
}

func (x *PrivXPatch) PublishChanges(ctx *wf.TaskContext, repoName string, cp Checkpoint, securityCommit string) ([]string, error) {
	return PublicizePrivateChanges(ctx, PublicizeParams{
		Git:            x.Git,
		Public:         x.PublicGerrit,
		Project:        repoName,
		TargetBranch:   "master",
		StartingHead:   cp.StartingHead,
		Private:        x.PrivateGerrit,
		PrivateProject: repoName,
		PrivateBranch:  cp.Branch,
		SecurityCommit: securityCommit,
		Labels:         []string{"Auto-Submit+1", "Commit-Queue+1"},
	})
}

func (x *PrivXPatch) MailAnnouncement(ctx *wf.TaskContext, tagged TagRepo, rm *relmeta.ReleaseMilestone) (SentMail, error) {
	r := golangOrgXAnnouncement{
		Module:  tagged.ModPath,
		Version: tagged.NewerVersion,
	}
	for _, p := range rm.Patches {
		if !strings.Contains(p.Package, tagged.ModPath) {
			continue
		}
		r.Security = append(r.Security, p.ReleaseNote)
	}

	mc, _, err := announcementMail(r)
	if err != nil {
		return SentMail{}, err
	}

	ctx.Printf("announcement subject: %s\n\n", mc.Subject)
	ctx.Printf("announcement body HTML:\n%s\n", mc.BodyHTML)
	ctx.Printf("announcement body text:\n%s", mc.BodyText)

	ctx.DisableRetries()
	if err := x.SendMail(ctx, x.AnnounceMailHeader, mc); err != nil {
		return SentMail{}, err
	}

	return SentMail{Subject: mc.Subject}, nil
}

func (x *PrivXPatch) CreateVulnReports(ctx *wf.TaskContext, rm *relmeta.ReleaseMilestone, vulnerableAt *report.Version, tagged TagRepo, announceURL string, reviewers []string) (string, error) {
	var reports []*report.Report
	for _, p := range rm.Patches {
		mod, err := x.vulnModuleInfo(p, tagged, vulnerableAt)
		if err != nil {
			return "", err
		}
		r, err := VulnReport(p, mod, announceURL)
		if err != nil {
			return "", err
		}
		reports = append(reports, r)
	}

	// TODO(nealpatel): At this point, we need to
	// run the linter; for x-repo this is more trivial.
	// For std, the symbol resolution is more complex.
	//
	// These will generate the cve5/osv files that
	// must be included in the diff below.
	return MailVulnReports(ctx, x.PublicGerrit, reports, reviewers)
}

// vulnModuleInfo derives the [VulnModuleInfo] for a single patch.
func (x *PrivXPatch) vulnModuleInfo(p *relmeta.SecurityPatch, tagged TagRepo, vulnerableAt *report.Version) (VulnModuleInfo, error) {
	repo, err := repoName(p.Package)
	if err != nil {
		return VulnModuleInfo{}, err
	}
	if got, want := repo, tagged.Name; got != want {
		return VulnModuleInfo{}, fmt.Errorf("package mismatch: %q vs %q", got, want)
	}
	if tagged.NewerVersion == "" {
		return VulnModuleInfo{}, fmt.Errorf("repo %q was not tagged", tagged.Name)
	}
	return VulnModuleInfo{
		Module:       tagged.ModPath,
		Versions:     report.Versions{report.Fixed(strings.TrimPrefix(tagged.NewerVersion, "v"))},
		VulnerableAt: vulnerableAt,
	}, nil
}

// UpdateGitHubIssues updates the body of each security issue in rm
// with a disclosure notice. It is a no-op when rm is nil or has no patches.
func UpdateGitHubIssues(ctx *wf.TaskContext, gh GitHubClientInterface, rm *relmeta.ReleaseMilestone) error {
	if rm == nil {
		return nil
	}
	for _, p := range rm.Patches {
		var buf bytes.Buffer
		if err := announceTmpl.ExecuteTemplate(&buf, "disclosure.md", p); err != nil {
			return err
		}
		body := buf.String()
		req := &github.IssueRequest{Body: &body}
		if _, _, err := gh.EditIssue(ctx, "golang", "go", int(p.GitHubIssueID), req); err != nil {
			return err
		}
		ctx.Printf("Updated https://go.dev/issue/%d", p.GitHubIssueID)
	}
	return nil
}
