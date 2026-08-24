// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"text/template"
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
	// PublicRepoURL returns a git clone URL for repo
	PublicRepoURL func(repo string) string

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
		skipPostSubmit = wf.Param(wd, wf.ParamDef[bool]{Name: "Skip post submit result (optional)", ParamType: wf.Bool})
		reviewers      = wf.Param(wd, reviewersParam) // We don't fill this.
	)

	availableRepos := wf.Task0(wd, "Load all repositories", tagx.SelectRepos)

	rm := wf.Task1(wd, "Pull release milestone", x.PullMilestone, milestoneNum)
	patches := wf.Task3(wd, "Get changes for target x repo", x.FilterPatches, rm, targetRepo, availableRepos)
	branch := wf.Task1(wd, "Create checkpoint branch", x.CreateCheckpoint, targetRepo)
	patches = wf.Task2(wd, "Move and rebase all changes per x repo", x.MoveAndRebaseAll, branch, patches)
	patches = wf.Task1(wd, "Waiting for submissions", x.AwaitSubmissions, patches)
	// block for manual review before pushing changes to public
	okayToDisclose := wf.Action0(wd, "Wait to disclose", x.ApproveAction) // TODO(nealpatel): Add warning text
	patches = wf.Task2(wd, "Publish changes", x.PublishChanges, targetRepo, patches, wf.After(okayToDisclose))
	tagged := wf.Expand4(wd, "Create single-repo plan", tagx.BuildSingleRepoPlan, availableRepos, targetRepo, skipPostSubmit, reviewers, wf.After(patches))
	vulnerableAt := wf.Task1(wd, "Resolve vulnerable version", x.ResolveVulnerableVersion, tagged)

	// wait for manual approval of the announcement message
	okayToAnnounce := wf.Action0(wd, "Wait to Announce", x.ApproveAction, wf.After(tagged))
	sentMail := wf.Task2(wd, "Mail announcement", x.MailAnnouncement, tagged, rm, wf.After(okayToAnnounce))
	announcementURL := wf.Task1(wd, "await-announcement", x.AwaitAnnounceMail, sentMail)
	wf.Output(wd, "Announcement URL", announcementURL)

	// post-announcement tasks
	changeID := wf.Task4(wd, "Create vuln reports", x.CreateVulnReports, rm, vulnerableAt, tagged, announcementURL)
	wf.Output(wd, "File VulnDB Reports", changeID)
	wf.Action1(wd, "Update GitHub issues", x.UpdateGitHubIssues, rm, wf.After(changeID))

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

func (x *PrivXPatch) CreateCheckpoint(ctx *wf.TaskContext, repoName string) (string, error) {
	publicHead, err := x.PrivateGerrit.ReadBranchHead(ctx, repoName, "public")
	if err != nil {
		return "", err
	}
	// Append the formatted timestamp to make any restarts idempotent.
	checkpointName := fmt.Sprintf("public-%s", time.Now().UTC().Format("20060102-150405"))
	if _, err := x.PrivateGerrit.CreateBranch(ctx, repoName, checkpointName, gerrit.BranchInput{Revision: publicHead}); err != nil {
		return "", err
	}
	return checkpointName, nil
}

func (x *PrivXPatch) MoveAndRebaseAll(ctx *wf.TaskContext, branch string, patches []*ref) ([]*ref, error) {
	for _, p := range patches {
		for i, ci := range p.Changes {
			movedCI, err := x.PrivateGerrit.MoveChange(ctx, ci.ID, branch)
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

func (x *PrivXPatch) PublishChanges(ctx *wf.TaskContext, repoName string, patches []*ref) ([]*ref, error) {
	// TODO(nealpatel): Unless allowing for multiple modules, hoist to global.
	clRE := regexp.MustCompile(fmt.Sprintf(`https://go-review\.googlesource\.com/c/%s/\+/(\d+)`, regexp.QuoteMeta(repoName)))
	var disclosed []string
	for _, p := range patches {
		for i, change := range p.Changes {
			cl, err := x.publishChange(ctx, repoName, p.Patch.Changelists[i], change, clRE)
			if err != nil {
				return nil, err
			}
			disclosed = append(disclosed, cl)
		}
	}

	var g errgroup.Group
	for _, cl := range disclosed {
		g.Go(func() error {
			ctx.Printf("Awaiting review/submit of %v", cl)
			_, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
				return x.PublicGerrit.Submitted(ctx, cl, "")
			})
			return err
		})
	}

	// TODO(nealpatel): Is changeInfo supposed to be
	// stored in .Changes similarly the other workflow?
	//
	// If not, this can be an ActionN.
	return patches, g.Wait()
}

func (x *PrivXPatch) publishChange(ctx *wf.TaskContext, repoName, clLink string, change *gerrit.ChangeInfo, clRE *regexp.Regexp) (string, error) {
	changeInfo, err := x.PrivateGerrit.GetChange(ctx, change.ID, gerrit.QueryChangesOpt{Fields: []string{"CURRENT_REVISION"}})
	if err != nil {
		return "", err
	}
	if changeInfo.Status != gerrit.ChangeStatusMerged {
		return "", fmt.Errorf("CL %s not merged, status is %s", clLink, changeInfo.Status)
	}
	rev, ok := changeInfo.Revisions[changeInfo.CurrentRevision]
	if !ok {
		return "", errors.New("current revision not found")
	}
	fetch, ok := rev.Fetch["http"]
	if !ok {
		return "", errors.New("fetch info not found")
	}
	origin, ref := fetch.URL, fetch.Ref

	// We directly use Git here, rather than the Gerrit API, as there are
	// limitations to the types of patches which you can create using said
	// API. In particular patches which contain any binary content are hard
	// to replicate from one instance to another using the API alone. Rather
	// than adding workarounds for those edge cases, we just use Git
	// directly, which makes the process extremely simple.
	repo, err := x.Git.Clone(ctx, x.PublicRepoURL(repoName))
	if err != nil {
		return "", err
	}
	defer repo.Close()
	ctx.Printf("cloned repo into %s", repo.dir)

	ctx.Printf("fetching %s from %s", ref, origin)
	if _, err := repo.RunCommand(ctx, "fetch", origin, ref); err != nil {
		return "", err
	}
	ctx.Printf("fetched")
	if _, err := repo.RunCommand(ctx, "cherry-pick", "FETCH_HEAD"); err != nil {
		return "", err
	}
	ctx.Printf("cherry-picked")
	var refspec strings.Builder
	refspec.WriteString("HEAD:refs/for/master%l=Auto-Submit,l=Commit-Queue+1")
	// We don't typically specify reviews in the historical releases;
	// so this should NOT be hardcoded; instead, it should pull from
	// some ACL somewhere?
	reviewerEmails, err := coordinatorEmails([]string{})
	if err != nil {
		return "", err
	}
	for _, reviewer := range reviewerEmails {
		fmt.Fprintf(&refspec, ",r=%s", reviewer)
	}

	// Beyond this point we don't want to retry any of the following steps.
	ctx.DisableRetries()

	ctx.Printf("pushing %s to %s", refspec.String(), x.PublicRepoURL(repoName))
	gitPushOutput, err := repo.RunGitPush(ctx, x.PublicRepoURL(repoName), refspec.String())
	if err != nil {
		return "", err
	}

	matches := clRE.FindSubmatch(gitPushOutput)
	if len(matches) != 2 {
		return "", errors.New("unable to find CL number")
	}

	return string(matches[1]), err
}

func (x *PrivXPatch) MailAnnouncement(ctx *wf.TaskContext, tagged TagRepo, rm *relmeta.ReleaseMilestone) (SentMail, error) {
	var (
		relNotes    []string
		subjectNoun = "Vulnerability"
		bodyPhrase  = "address a security issue:"
	)
	for _, p := range rm.Patches {
		if !strings.Contains(p.Package, tagged.ModPath) {
			continue
		}
		relNotes = append(relNotes, p.ReleaseNote)
	}
	if len(relNotes) > 1 {
		subjectNoun = "Vulnerabilities"
		bodyPhrase = "address the following security issues:"
	}

	var buf bytes.Buffer
	if err := privXPatchAnnouncementTmpl.Execute(&buf, map[string]any{
		"Module":                tagged.ModPath,
		"Version":               tagged.NewerVersion,
		"MaybePluralizeSubject": subjectNoun,
		"MaybePluralizeBody":    bodyPhrase,
		"RelNotes":              relNotes,
	}); err != nil {
		return SentMail{}, err
	}
	m, err := mail.ReadMessage(&buf)
	if err != nil {
		return SentMail{}, err
	}
	html, text, err := renderMarkdown(m.Body)
	if err != nil {
		return SentMail{}, err
	}

	mc := MailContent{m.Header.Get("Subject"), html, text}

	ctx.Printf("announcement subject: %s\n\n", mc.Subject)
	ctx.Printf("announcement body HTML:\n%s\n", mc.BodyHTML)
	ctx.Printf("announcement body text:\n%s", mc.BodyText)

	ctx.DisableRetries()
	if err := x.SendMail(ctx, x.AnnounceMailHeader, mc); err != nil {
		return SentMail{}, err
	}

	return SentMail{Subject: mc.Subject}, nil
}

func (x *PrivXPatch) CreateVulnReports(ctx *wf.TaskContext, rm *relmeta.ReleaseMilestone, vulnerableAt *report.Version, tagged TagRepo, announceURL string) (string, error) {
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

	return MailVulnReports(ctx, x.PublicGerrit, reports)
}

// vulnModuleInfo derives the [VulnModuleInfo] for a single patch. For
// golang.org/x patches it validates that the package belongs to the
// tagged repo and uses the network-resolved vulnerableAt (the
// historical x-repo behavior). std/cmd patches do not flow through a
// TagRepo, so for them the module value and vulnerable_at are derived
// locally from the patch via [DeriveVulnModuleInfo].
func (x *PrivXPatch) vulnModuleInfo(p *relmeta.SecurityPatch, tagged TagRepo, vulnerableAt *report.Version) (VulnModuleInfo, error) {
	if strings.HasPrefix(p.Package, "golang.org/x/") {
		repo, err := repoName(p.Package)
		if err != nil {
			return VulnModuleInfo{}, err
		}
		if got, want := repo, tagged.Name; got != want {
			return VulnModuleInfo{}, fmt.Errorf("package mismatch: %q vs %q", got, want)
		}
		return VulnModuleInfo{Module: tagged.ModPath, VulnerableAt: vulnerableAt}, nil
	}
	return DeriveVulnModuleInfo(p)
}

var vulndbReviewers = []string{"neal@golang.org", "nsh@golang.org"}

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
		body := fmt.Sprintf(disclosureBody, p.ReleaseNote, p.Track, p.ID)
		req := &github.IssueRequest{Body: &body}
		if _, _, err := gh.EditIssue(ctx, "golang", "go", int(p.GitHubIssueID), req); err != nil {
			return err
		}
		ctx.Printf("Updated https://go.dev/issue/%d", p.GitHubIssueID)
	}
	return nil
}

const disclosureBody = `%s

---

This was a %s issue originally tracked in http://b/%d.`

var privXPatchAnnouncementTmpl = template.Must(template.New("").Parse(`Subject: [security] {{.MaybePluralizeSubject}} in {{.Module}}

Hello gophers,

We have tagged version {{.Version}} of {{.Module}} in order to {{.MaybePluralizeBody}}
{{range .RelNotes}}
{{.}}
{{end}}
Cheers,
Go Security team`))
