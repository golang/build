// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"text/template"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
)

type PrivXPatch struct {
	Git           *Git
	PublicGerrit  GerritClient
	PrivateGerrit GerritClient
	// PublicRepoURL returns a git clone URL for repo
	PublicRepoURL func(repo string) string

	ApproveAction      func(*wf.TaskContext) error
	SendMail           func(MailHeader, MailContent) error
	AnnounceMailHeader MailHeader
}

func (x *PrivXPatch) NewDefinition(tagx *TagXReposTasks) *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam, groups.SecurityTeam}})
	// TODO: this should be simpler, CL number + patchset?
	clNumber := wf.Param(wd, wf.ParamDef[string]{Name: "go-internal CL number", Example: "536316"})
	reviewers := wf.Param(wd, reviewersParam)
	repoName := wf.Param(wd, wf.ParamDef[string]{Name: "Repository name", Example: "net"})
	// TODO: probably always want to skip, might make sense to not include this
	skipPostSubmit := wf.Param(wd, wf.ParamDef[bool]{Name: "Skip post submit result (optional)", ParamType: wf.Bool})
	cve := wf.Param(wd, wf.ParamDef[string]{Name: "CVE"})
	githubIssue := wf.Param(wd, wf.ParamDef[string]{Name: "GitHub issue", Doc: "A link to the GitHub issue for the report.", Example: "https://go.dev/issue/70779"})
	relNote := wf.Param(wd, wf.ParamDef[string]{Name: "Release note", ParamType: wf.LongString})
	acknowledgement := wf.Param(wd, wf.ParamDef[string]{Name: "Acknowledgement"})

	repos := wf.Task0(wd, "Load all repositories", tagx.SelectRepos)

	repos = wf.Task4(wd, "Publish change", func(ctx *wf.TaskContext, clNumber string, reviewers []string, repos []TagRepo, repoName string) ([]TagRepo, error) {
		if !slices.ContainsFunc(repos, func(r TagRepo) bool { return r.Name == repoName }) {
			return nil, fmt.Errorf("no repository %q", repoName)
		}

		changeInfo, err := x.PrivateGerrit.GetChange(ctx, clNumber, gerrit.QueryChangesOpt{Fields: []string{"CURRENT_REVISION"}})
		if err != nil {
			return nil, err
		}
		if changeInfo.Project != repoName {
			return nil, fmt.Errorf("CL is for unexpected project, got: %s, want %s", changeInfo.Project, repoName)
		}
		if changeInfo.Status != gerrit.ChangeStatusMerged {
			return nil, fmt.Errorf("CL %s not merged, status is %s", clNumber, changeInfo.Status)
		}
		rev, ok := changeInfo.Revisions[changeInfo.CurrentRevision]
		if !ok {
			return nil, errors.New("current revision not found")
		}
		fetch, ok := rev.Fetch["http"]
		if !ok {
			return nil, errors.New("fetch info not found")
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
			return nil, err
		}
		ctx.Printf("cloned repo into %s", repo.dir)

		ctx.Printf("fetching %s from %s", ref, origin)
		if _, err := repo.RunCommand(ctx.Context, "fetch", origin, ref); err != nil {
			return nil, err
		}
		ctx.Printf("fetched")
		if _, err := repo.RunCommand(ctx.Context, "cherry-pick", "FETCH_HEAD"); err != nil {
			return nil, err
		}
		ctx.Printf("cherry-picked")
		refspec := "HEAD:refs/for/master%l=Auto-Submit,l=Commit-Queue+1"
		reviewerEmails, err := coordinatorEmails(reviewers)
		if err != nil {
			return nil, err
		}
		for _, reviewer := range reviewerEmails {
			refspec += ",r=" + reviewer
		}

		// Beyond this point we don't want to retry any of the following steps.
		ctx.DisableRetries()

		ctx.Printf("pushing to %s", x.PublicRepoURL(repoName))
		// We are unable to use repo.RunCommand here, because of strange i/o
		// changes that git made. The messages sent by the remote are printed by
		// git to stderr, and no matter what combination of options you pass it
		// (--verbose, --porcelain, etc), you cannot reasonably convince it to
		// print those messages to stdout. Because of this we need to use the
		// underlying repo.git.runGitStreamed method, so that we can inspect
		// stderr in order to extract the new CL number that gerrit sends us.
		var stdout, stderr bytes.Buffer
		err = repo.git.runGitStreamed(ctx.Context, &stdout, &stderr, repo.dir, "push", x.PublicRepoURL(repoName), refspec)
		if err != nil {
			return nil, fmt.Errorf("git push failed: %v, stdout: %q stderr: %q", err, stdout.String(), stderr.String())
		}

		// Extract the CL number from the output using a quick and dirty regex.
		re, err := regexp.Compile(fmt.Sprintf(`https:\/\/go-review.googlesource.com\/c\/%s\/\+\/(\d+)`, regexp.QuoteMeta(repoName)))
		if err != nil {
			return nil, fmt.Errorf("failed to compile regex: %s", err)
		}
		matches := re.FindSubmatch(stderr.Bytes())
		if len(matches) != 2 {
			return nil, errors.New("unable to find CL number")
		}
		changeID := string(matches[1])

		ctx.Printf("Awaiting review/submit of %v", changeID)
		_, err = AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
			return x.PublicGerrit.Submitted(ctx, changeID, "")
		})
		if err != nil {
			return nil, err
		}
		return repos, nil
	}, clNumber, reviewers, repos, repoName)

	tagged := wf.Expand4(wd, "Create single-repo plan", tagx.BuildSingleRepoPlan, repos, repoName, skipPostSubmit, reviewers)

	okayToAnnoucne := wf.Action0(wd, "Wait to Announce", x.ApproveAction, wf.After(tagged))

	wf.Task5(wd, "Mail announcement", func(ctx *wf.TaskContext, tagged TagRepo, cve string, githubIssue string, relNote string, acknowledgement string) (string, error) {
		var buf bytes.Buffer
		if err := privXPatchAnnouncementTmpl.Execute(&buf, map[string]string{
			"Module":          tagged.ModPath,
			"Version":         tagged.NewerVersion,
			"RelNote":         relNote,
			"Acknowledgement": acknowledgement,
			"CVE":             cve,
			"GithubIssue":     githubIssue,
		}); err != nil {
			return "", err
		}
		m, err := mail.ReadMessage(&buf)
		if err != nil {
			return "", err
		}
		html, text, err := renderMarkdown(m.Body)
		if err != nil {
			return "", err
		}

		mc := MailContent{m.Header.Get("Subject"), html, text}

		ctx.Printf("announcement subject: %s\n\n", mc.Subject)
		ctx.Printf("announcement body HTML:\n%s\n", mc.BodyHTML)
		ctx.Printf("announcement body text:\n%s", mc.BodyText)

		ctx.DisableRetries()
		err = x.SendMail(x.AnnounceMailHeader, mc)
		if err != nil {
			return "", err
		}

		return "", nil
	}, tagged, cve, githubIssue, relNote, acknowledgement, wf.After(okayToAnnoucne))

	wf.Output(wd, "done", tagged)
	return wd
}

var privXPatchAnnouncementTmpl = template.Must(template.New("").Parse(`Subject: [security] Vulnerability in {{.Module}}

Hello gophers,

We have tagged version {{.Version}} of {{.Module}} in order to address a security issue.

{{.RelNote}}

Thanks to {{.Acknowledgement}} for reporting this issue.

This is {{.CVE}} and Go issue {{.GithubIssue}}.

Cheers,
Go Security team`))
