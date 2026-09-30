// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/build/relmeta"
)

type PublicizeParams struct {
	Git            *Git
	Public         GerritClient
	Project        string
	TargetBranch   string
	StartingHead   string
	Private        GerritClient
	PrivateProject string
	PrivateBranch  string
	SecurityCommit string
	Labels         []string
	Reviewers      []string
}

func PublicizePrivateChanges(ctx *wf.TaskContext, in PublicizeParams) (changeIDs []string, _ error) {
	/*
		At this point we want to fetch the security commit and then upstream it to the public instance.
		At a high-level what we're doing is:

			git push {publicOrigin} {securityCommit}:refs/for/{targetBranch}

		In practice we need to setup a temporary git repository that has securityCommit
		available. It turns out not to be hard to use cherry-pick on a commit range to rewrite
		the committer to be that of relui, so do that (it might be more clear and accurate,
		and it also means no need to grant forgeCommitter permission to relui).
		So the low-level git commands we run look like this:

			git clone -b release-branch.go1.N https://go.googlesource.com/go
			git fetch https://go-internal.googlesource.com/go internal-release-branch.go1.N.M
			git cherry-pick {startingHead}..{securityCommit}
			git push {publicOrigin} HEAD:refs/for/{targetBranch}%l=Auto-Submit+1,l=TryBot-Bypass+1,r=reviewer@golang.org

		Finally we parse out the newly created CL numbers and return those to be awaited for.
	*/
	if head, err := in.Public.ReadBranchHead(ctx, in.Project, in.TargetBranch); err != nil {
		return nil, fmt.Errorf("reading public branch head (safe to retry this step): %w", err)
	} else if head != in.StartingHead {
		return nil, fmt.Errorf("head of public %q branch is %q, want %q; retrying this step alone will not help; restart the release workflow to re-coalesce against the current head", in.TargetBranch, head, in.StartingHead)
	}
	if head, err := in.Private.ReadBranchHead(ctx, in.PrivateProject, in.PrivateBranch); err != nil {
		return nil, fmt.Errorf("reading private branch head (safe to retry this step): %w", err)
	} else if head != in.SecurityCommit {
		return nil, fmt.Errorf("head of private %q branch is %q, want %q; retrying this step alone will not help; restart the release workflow to re-coalesce against the current head", in.PrivateBranch, head, in.SecurityCommit)
	}
	publicOrigin := in.Public.GitRepoURL(in.Project)
	repo, err := in.Git.CloneBranch(ctx, publicOrigin, in.TargetBranch)
	if err != nil {
		return nil, fmt.Errorf("cloning public repo (safe to retry this step): %w", err)
	}
	defer repo.Close()
	ctx.Printf("cloned public repo")

	privateOrigin, privateRef := in.Private.GitRepoURL(in.PrivateProject), "refs/heads/"+in.PrivateBranch
	ctx.Printf("fetching %s from %s", privateRef, privateOrigin)
	if _, err := repo.RunCommand(ctx, "fetch", privateOrigin, privateRef); err != nil {
		return nil, fmt.Errorf("fetching private branch (safe to retry this step): %w", err)
	}
	ctx.Printf("fetched")
	if _, err := repo.RunCommand(ctx, "cherry-pick", in.StartingHead+".."+in.SecurityCommit); err != nil {
		return nil, fmt.Errorf("cherry-picking security fixes (safe to retry this step): %w", err)
	}
	ctx.Printf("cherry-picked")

	existingCLs, err := checkAlreadyPublicized(ctx, repo, in.Public, in.Project, in.TargetBranch, in.StartingHead)
	if err != nil {
		return nil, err
	}
	if len(existingCLs) != 0 {
		ctx.Printf("All %d security CLs already exist on public Gerrit; skipping push.", len(existingCLs))
		for _, c := range existingCLs {
			ctx.Printf("• %s", ChangeLink(c))
		}
		return existingCLs, nil
	}

	var refspec strings.Builder
	fmt.Fprintf(&refspec, "HEAD:refs/for/%s%%", in.TargetBranch)
	for i, l := range in.Labels {
		if i > 0 {
			refspec.WriteString(",")
		}
		fmt.Fprintf(&refspec, "l=%s", l)
	}
	reviewerEmails, err := coordinatorEmails(in.Reviewers)
	if err != nil {
		return nil, fmt.Errorf("resolving coordinator emails (safe to retry this step): %w", err)
	}
	for _, r := range reviewerEmails {
		fmt.Fprintf(&refspec, ",r=%s", r)
	}

	// What's coming up next involves side-effects in external systems,
	// so beyond this point of the task we want manual retries only, not automated ones.
	ctx.DisableRetries()

	ctx.Printf("pushing %s to %s", refspec.String(), publicOrigin)
	gitPushOutput, err := repo.RunGitPush(ctx, publicOrigin, refspec.String())
	if err != nil {
		return nil, fmt.Errorf("pushing security CLs to public Gerrit (manual intervention required): %w", err)
	}
	ctx.Printf("git push output:\n%s\n", gitPushOutput)

	// Extract the CL numbers from the output using a simple regexp.
	re := regexp.MustCompile(fmt.Sprintf(`https://go-review\.googlesource\.com/c/%s/\+/(\d+)`, regexp.QuoteMeta(in.Project)))
	matches := re.FindAllSubmatch(gitPushOutput, -1)
	if matches == nil {
		return nil, fmt.Errorf("no matches for successful mail of CL in git push output:\n%s", gitPushOutput)
	}
	for i, match := range matches {
		if len(match) != 2 {
			return nil, fmt.Errorf("bad match %d for successful mail of CL in git push output:\n%s", i, gitPushOutput)
		}
		changeIDs = append(changeIDs, in.Project+"~"+string(match[1]))
	}
	ctx.Printf("Mailed %d changes to await for:", len(changeIDs))
	for _, c := range changeIDs {
		ctx.Printf("• %s", ChangeLink(c))
	}

	return changeIDs, nil
}

var changeIDRe = regexp.MustCompile(`(?m)^Change-Id: (I[0-9a-f]{40})$`)

// checkAlreadyPublicized returns a complete slice of already disclosed CLs
// or an empty slice, indicating the caller owns pushing.
//
// If the number of resolved CLs does not match the complete list, an error
// is returned. checkAlreadyPublicized assumes that any state that is not
// "total disclosure" or "no disclosure" is an error requiring manual review.
func checkAlreadyPublicized(ctx *wf.TaskContext, repo *GitDir, public GerritClient, project, targetBranch, startingHead string) ([]string, error) {
	out, err := repo.RunCommand(ctx, "log", "--format=%H %B%x00", startingHead+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("listing cherry-picked commits: %w", err)
	}
	type commitChangeID struct {
		hash     string
		changeID string
	}
	var commits []commitChangeID
	for entry := range strings.SplitSeq(strings.TrimRight(string(out), "\x00"), "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		before, after, ok := strings.Cut(entry, " ")
		if !ok {
			continue
		}
		hash := before
		body := after
		m := changeIDRe.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		commits = append(commits, commitChangeID{hash: hash, changeID: m[1]})
	}
	if len(commits) == 0 {
		return nil, nil
	}

	var (
		found, queryErrors int
		existingCLs        []string
	)
	for _, c := range commits {
		results, err := public.QueryChanges(ctx,
			fmt.Sprintf("project:%s branch:%s change:%s -is:abandoned", project, targetBranch, c.changeID))
		if err != nil {
			ctx.Printf("error querying public Gerrit for Change-Id %s (commit %.8s): %v", c.changeID, c.hash, err)
			queryErrors++
			continue
		}
		if len(results) > 0 {
			found++
			existingCLs = append(existingCLs, fmt.Sprintf("%s~%d", project, results[0].ChangeNumber))
		}
	}
	if queryErrors > 0 {
		return nil, fmt.Errorf("querying public Gerrit for already-pushed CLs: %d of %d queries failed (manual intervention required)", queryErrors, len(commits))
	}
	if found > 0 && found < len(commits) {
		return nil, fmt.Errorf("partial publicize detected: %d of %d security CLs already exist on public Gerrit; %d are missing (manual intervention required)", found, len(commits), len(commits)-found)
	}
	if found == len(commits) {
		return existingCLs, nil
	}
	return nil, nil
}

func ResolveExternalChangelists(ctx *wf.TaskContext, private, public GerritClient, project string, patches []*relmeta.SecurityPatch) (map[string]string, error) {
	external := make(map[string]string)
	for _, p := range patches {
		if p.Track == relmeta.Public {
			continue
		}
		for _, clURL := range DeployedChangelists(p, project, "public") {
			_, num, ok := strings.Cut(clURL, "/+/")
			if !ok {
				continue
			}
			msg, err := private.GetCommitMessage(ctx, num)
			if err != nil {
				return nil, err
			}
			m := changeIDRe.FindStringSubmatch(msg)
			if m == nil {
				return nil, fmt.Errorf("private CL %s has no Change-Id footer (manual intervention required)", clURL)
			}
			query := fmt.Sprintf("project:%s branch:master change:%s", project, m[1])
			ci, err := AwaitCondition(ctx, time.Minute, func() (*gerrit.ChangeInfo, bool, error) {
				results, err := public.QueryChanges(ctx, query)
				if err != nil {
					return nil, false, err
				}
				if len(results) == 0 {
					ctx.Printf("awaiting public master CL for %s (Change-Id %s)", clURL, m[1])
					return nil, false, nil
				}
				return results[0], true, nil
			})
			if err != nil {
				return nil, err
			}
			external[clURL] = fmt.Sprintf("https://go.dev/cl/%d", ci.ChangeNumber)
		}
	}
	return external, nil
}
