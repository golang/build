// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v74/github"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relmeta"
	"golang.org/x/mod/semver"
	"golang.org/x/sync/errgroup"
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
	checkpoint := wf.Task1(wd, "Create checkpoint branch", x.CreateCheckpoint, targetRepo)
	patches = wf.Task2(wd, "Move and rebase all changes per x repo", x.MoveAndRebaseAll, checkpoint, patches)
	patches = wf.Task1(wd, "Waiting for submissions", x.AwaitSubmissions, patches)
	securityCommit := wf.Task2(wd, "Read checkpoint head", x.ReadCheckpointHead, targetRepo, checkpoint, wf.After(patches))
	// block for manual review before pushing changes to public
	okayToDisclose := wf.Action0(wd, "Wait to disclose", x.ApproveAction, wf.After(securityCommit)) // TODO(nealpatel): Add warning text
	disclosed := wf.Task3(wd, "Publish changes", x.PublishChanges, targetRepo, checkpoint, securityCommit, wf.After(okayToDisclose))
	submitted := wf.Action1(wd, "Wait for submission of published changes", x.AwaitPublicSubmissions, disclosed)
	tagged := wf.Expand4(wd, "Create single-repo plan", tagx.BuildSingleRepoPlan, availableRepos, targetRepo, skipPostSubmit, reviewers, wf.After(submitted))
	vulnerableAt := wf.Task1(wd, "Resolve vulnerable version", x.ResolveVulnerableVersion, tagged)

	// wait for manual approval of the announcement message
	okayToAnnounce := wf.Action0(wd, "Wait to Announce", x.ApproveAction, wf.After(tagged))
	sentMail := wf.Task2(wd, "Mail announcement", x.MailAnnouncement, tagged, rm, wf.After(okayToAnnounce))
	announcementURL := wf.Task1(wd, "await-announcement", x.AwaitAnnounceMail, sentMail)
	wf.Output(wd, "Announcement URL", announcementURL)

	// post-announcement tasks
	updated := wf.Action1(wd, "Update GitHub issues", x.UpdateGitHubIssues, rm, wf.After(announcementURL))
	converted := wf.Task4(wd, "Convert internal changelists", x.ConvertInternalChangelists, targetRepo, milestoneNum, patches, securityReviewers, wf.After(announcementURL))
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

func (x *PrivXPatch) FilterPatches(ctx *wf.TaskContext, rm *relmeta.ReleaseMilestone, target string, found []TagRepo) (patches []*ref, _ error) {
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

		var cls []*gerrit.ChangeInfo
		for _, clLink := range p.Changelists {
			if p.Track == relmeta.Public {
				continue
			}
			clNum := clLink[strings.LastIndex(clLink, "/")+1:]
			ci, err := x.PrivateGerrit.GetChange(ctx, clNum, gerrit.QueryChangesOpt{Fields: []string{"CURRENT_REVISION", "SUBMITTABLE"}})
			if err != nil {
				return nil, err
			}
			if !strings.Contains(p.Package, ci.Project) {
				return nil, fmt.Errorf("CL is for unexpected project, got: %s, want %s", ci.Project, p.Package)
			}
			if !ci.Submittable {
				return nil, fmt.Errorf("change %s is not submittable", internalXRepoChangeURL(target, clNum))
			}
			ra, err := x.PrivateGerrit.GetRevisionActions(ctx, clNum, "current")
			if err != nil {
				return nil, err
			}
			if ra["submit"] == nil || !ra["submit"].Enabled {
				return nil, fmt.Errorf("change %s is not submittable", internalXRepoChangeURL(target, clNum))
			}
			// TODO: Add regex for CVE / GH
			// TODO(nealpatel): Edge case; order matters for stacked changes.
			cls = append(cls, ci)
		}
		patches = append(patches, &ref{Patch: p, Changes: cls})
	}

	return patches, nil
}

func internalXRepoChangeURL[T int | string](xrepo string, clNum T) string {
	return fmt.Sprintf("https://go-internal-review.git.corp.google.com/c/%s/+/%v", xrepo, clNum)
}

type ref struct {
	Patch   *relmeta.SecurityPatch
	Changes []*gerrit.ChangeInfo
}

type checkpointInfo struct {
	Branch       string
	StartingHead string
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

func (x *PrivXPatch) CreateCheckpoint(ctx *wf.TaskContext, repoName string) (checkpointInfo, error) {
	publicHead, err := x.PrivateGerrit.ReadBranchHead(ctx, repoName, "public")
	if err != nil {
		return checkpointInfo{}, err
	}
	// Append the formatted timestamp to make any restarts idempotent.
	checkpointName := fmt.Sprintf("public-%s", time.Now().UTC().Format("20060102-150405"))
	if _, err := x.PrivateGerrit.CreateBranch(ctx, repoName, checkpointName, gerrit.BranchInput{Revision: publicHead}); err != nil {
		return checkpointInfo{}, err
	}
	return checkpointInfo{Branch: checkpointName, StartingHead: publicHead}, nil
}

func (x *PrivXPatch) ReadCheckpointHead(ctx *wf.TaskContext, repoName string, cp checkpointInfo) (string, error) {
	return x.PrivateGerrit.ReadBranchHead(ctx, repoName, cp.Branch)
}

func (x *PrivXPatch) MoveAndRebaseAll(ctx *wf.TaskContext, cp checkpointInfo, patches []*ref) ([]*ref, error) {
	for _, p := range patches {
		for i, ci := range p.Changes {
			movedCI, err := x.PrivateGerrit.MoveChange(ctx, ci.ID, cp.Branch)
			if err != nil {
				// In case we need to re-run the Move step, tolerate the case where the change
				// is already on the branch.
				var httpErr *gerrit.HTTPError
				if !errors.As(err, &httpErr) || httpErr.Res.StatusCode != http.StatusConflict || string(httpErr.Body) != "Change is already destined for the specified branch\n" {
					return nil, err
				}
			} else {
				ci = &movedCI
			}
			rebasedCI, err := x.PrivateGerrit.RebaseChange(ctx, ci.ID, "")
			if err != nil {
				// Don't fail if the branch is already up to date.
				var httpErr *gerrit.HTTPError
				if !errors.As(err, &httpErr) || httpErr.Res.StatusCode != http.StatusConflict || string(httpErr.Body) != "Change is already up to date.\n" {
					return nil, err
				}
			} else {
				ci = &rebasedCI
			}
			p.Changes[i] = ci
		}
	}
	return patches, nil
}

func (x *PrivXPatch) AwaitSubmissions(ctx *wf.TaskContext, patches []*ref) ([]*ref, error) {
	var g errgroup.Group
	for _, p := range patches {
		for _, cl := range p.Changes {
			g.Go(func() error {
				ctx.Printf("Awaiting review/submit of %v", cl.ID)
				_, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
					// The ChangeInfo object returned by RebaseChange doesn't contain
					// information about submittability, so we need to refetch it using
					// GetChange.
					ci, err := x.PrivateGerrit.GetChange(ctx, cl.ID, gerrit.QueryChangesOpt{Fields: []string{"SUBMITTABLE"}})
					if err != nil {
						return "", false, err
					}
					// TODO(nealpatel): Make more robust/obvious.
					if strings.ToLower(ci.Status) == "merged" {
						return "", true, nil
					}
					if !ci.Submittable {
						return "", false, nil
					}
					_, err = x.PrivateGerrit.SubmitChange(ctx, ci.ID)
					if err != nil {
						return "", false, err
					}
					return "", true, nil
				})
				return err
			})
		}
	}
	return patches, g.Wait()
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

func (x *PrivXPatch) PublishChanges(ctx *wf.TaskContext, repoName string, cp checkpointInfo, securityCommit string) ([]string, error) {
	if publicHead, err := x.PublicGerrit.ReadBranchHead(ctx, repoName, "master"); err != nil {
		return nil, fmt.Errorf("reading public branch head (safe to retry this step): %w", err)
	} else if publicHead != cp.StartingHead {
		return nil, fmt.Errorf("head of public master is %q, but was %q when the checkpoint was created; retrying this step alone will not help; restart the workflow to re-coalesce against the current head", publicHead, cp.StartingHead)
	}
	if head, err := x.PrivateGerrit.ReadBranchHead(ctx, repoName, cp.Branch); err != nil {
		return nil, fmt.Errorf("reading private branch head (safe to retry this step): %w", err)
	} else if head != securityCommit {
		return nil, fmt.Errorf("head of private %q branch is %q, but was %q after submissions; retrying this step alone will not help; restart the workflow to re-coalesce against the current head", cp.Branch, head, securityCommit)
	}
	return PublicizePrivateChanges(ctx, PublicizeParams{
		Git:            x.Git,
		Public:         x.PublicGerrit,
		Project:        repoName,
		TargetBranch:   "master",
		StartingHead:   cp.StartingHead,
		PrivateOrigin:  x.PrivateGerrit.GitRepoURL(repoName),
		PrivateRef:     "refs/heads/" + cp.Branch,
		SecurityCommit: securityCommit,
		Labels:         []string{"Auto-Submit+1", "Commit-Queue+1"},
	})
}

func (x *PrivXPatch) AwaitPublicSubmissions(ctx *wf.TaskContext, changeIDs []string) error {
	var g errgroup.Group
	for _, cl := range changeIDs {
		g.Go(func() error {
			ctx.Printf("Awaiting review/submit of %v", cl)
			_, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
				return x.PublicGerrit.Submitted(ctx, cl, "")
			})
			return err
		})
	}
	return g.Wait()
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

func (x *PrivXPatch) ConvertInternalChangelists(ctx *wf.TaskContext, repoName, milestoneNum string, patches []*ref, reviewers []string) (*relmeta.ReleaseMilestone, error) {
	var sps []*relmeta.SecurityPatch
	for _, p := range patches {
		sps = append(sps, p.Patch)
	}
	external, err := ResolveExternalChangelists(ctx, x.PrivateGerrit, x.PublicGerrit, repoName, sps)
	if err != nil {
		return nil, err
	}
	rm, err := ConvertInternalChangelists(ctx, x.PrivateGerrit, milestoneNum, external, reviewers)
	if err != nil {
		return nil, err
	}
	return &rm, nil
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

func (x *PrivXPatch) UpdateGitHubIssues(ctx *wf.TaskContext, rm *relmeta.ReleaseMilestone) error {
	return UpdateGitHubIssues(ctx, x.GitHub, rm)
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
