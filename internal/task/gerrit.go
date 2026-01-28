// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/oauth2"
)

type CommitInfo struct {
	Commit  string
	Parents []string
	Message string
}

func (c CommitInfo) Title() string {
	// See https://git-scm.com/docs/git-commit#_discussion.
	titleLines, _, _ := strings.Cut(c.Message, "\n\n")
	return strings.ReplaceAll(titleLines, "\n", " ")
}

type GerritClient interface {
	// GitilesURL returns the URL to the Gitiles server for this Gerrit instance.
	GitilesURL() string
	// GitRepoURL returns the git repository URL for the specified Gerrit project name.
	// This URL can be used in the context of a git clone or a git remote.
	GitRepoURL(project string) string

	// CreateAutoSubmitChange creates a change with the given metadata and
	// contents, starts trybots with auto-submit enabled, and returns its change ID.
	// If the content of a file is empty, that file will be deleted from the repository.
	// If the requested contents match the state of the repository, the created change
	// is closed and the returned change ID will be empty.
	// Consider GenerateAutoSubmitChange if the list of files that need to be updated
	// isn't known ahead of time.
	//
	// Reviewers is the username part of a golang.org or google.com email address.
	//
	// As a special case for the main Go repository, if the only file content being
	// updated is the special VERSION file, trybots are bypassed.
	CreateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string, contents map[string]string) (string, error)
	// Submitted checks if the specified change has been submitted or failed
	// trybots. If the CL is submitted, returns the submitted commit hash.
	// If parentCommit is non-empty, the submitted CL's parent must match it.
	Submitted(ctx context.Context, changeID, parentCommit string) (commit string, submitted bool, _ error)

	// GetTag returns tag information for a specified tag.
	GetTag(ctx context.Context, project, tag string) (gerrit.TagInfo, error)
	// Tag creates a tag on project at the specified commit.
	Tag(ctx context.Context, project, tag, commit string) error
	// ListTags returns all the tags on project.
	ListTags(ctx context.Context, project string) ([]string, error)

	// ReadBranchHead returns the head of a branch in project.
	// If the branch doesn't exist, it returns an error matching gerrit.ErrResourceNotExist.
	ReadBranchHead(ctx context.Context, project, branch string) (string, error)
	// ListBranches returns the branch info for all the branch in a project.
	ListBranches(ctx context.Context, project string) ([]gerrit.BranchInfo, error)
	// CreateBranch creates the given branch and returns the created branch's revision.
	CreateBranch(ctx context.Context, project, branch string, input gerrit.BranchInput) (string, error)

	// ListCommits lists commits in project between head and base (including head, not including base).
	// Both head and base must be non-empty strings, otherwise an error is returned.
	// If either head or base refers to a commit that doesn't exist,
	// an error matching gerrit.ErrResourceNotExist is returned.
	// The commits are returned in order from head to base (i.e., first element is newest commit, last element is oldest comit),
	// capped in length to an implementation-defined page size. The caller can inspect
	// the parents of the last commit if it needs to detect the page size being insufficient.
	ListCommits(ctx context.Context, project, head, base string) ([]CommitInfo, error)

	// ListProjects lists all the projects on the server.
	ListProjects(ctx context.Context) ([]string, error)

	// ReadFile reads a file from project at the specified commit.
	// If the file doesn't exist, it returns an error matching gerrit.ErrResourceNotExist.
	ReadFile(ctx context.Context, project, commit, file string) ([]byte, error)
	// ReadDir reads a directory from project at the specified commit.
	// If the directory doesn't exist, it returns an error matching gerrit.ErrResourceNotExist.
	ReadDir(ctx context.Context, project, commit, dir string) ([]struct{ Name string }, error)

	// QueryChanges gets changes which match the query.
	QueryChanges(ctx context.Context, query string) ([]*gerrit.ChangeInfo, error)
	// GetChange gets information about a specific change.
	GetChange(ctx context.Context, changeID string, opts ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error)
	// SubmitChange submits a specific change.
	SubmitChange(ctx context.Context, changeID string) (gerrit.ChangeInfo, error)

	// SetHashtags modifies the hashtags for a CL.
	SetHashtags(ctx context.Context, changeID string, hashtags gerrit.HashtagsInput) error

	// CreateCherryPick creates a cherry-pick change. If there are no merge
	// conflicts, it starts trybots. If commitMessage is provided, the commit
	// message is updated, otherwise it is taken from the original change.
	// Reviewers are taken from the original change.
	CreateCherryPick(ctx context.Context, changeID string, branch string, commitMessage string) (_ gerrit.ChangeInfo, conflicts bool, _ error)
	// RebaseChange rebases a change onto a base revision. If revision is empty,
	// the change is rebased directly on top of the target branch.
	RebaseChange(ctx context.Context, changeID string, revision string) (gerrit.ChangeInfo, error)
	// MoveChange moves a change onto a new branch.
	MoveChange(ctx context.Context, changeID string, branch string) (gerrit.ChangeInfo, error)
	// GetRevisionActions retrieves revision actions.
	GetRevisionActions(ctx context.Context, changeID, revision string) (map[string]*gerrit.ActionInfo, error)
	// GetCommitMessage retrieves the commit message for a change.
	GetCommitMessage(ctx context.Context, changeID string) (string, error)
	// GetCommitsInRefs gets refs in which the specified commits were merged into.
	GetCommitsInRefs(ctx context.Context, project string, commits, refs []string) (map[string][]string, error)
}

// DestructiveGerritClient is a Gerrit client with destructive operations included.
// It's intended to be used in contexts where it's safe, such as test repositories.
type DestructiveGerritClient interface {
	GerritClient

	// ForceTag forcibly creates a tag on project at the specified commit,
	// even if the tag already exists and points to a different commit.
	ForceTag(ctx context.Context, project, tag, commit string) error
}

type RealGerritClient struct {
	Gitiles     string             // Gitiles server URL, without trailing slash. For example, "https://go.googlesource.com".
	GitilesAuth oauth2.TokenSource // Token source to use for authenticating with the Gitiles server. Optional.
	Client      *gerrit.Client
}

func (c *RealGerritClient) GitilesURL() string {
	return c.Gitiles
}

func (c *RealGerritClient) GitRepoURL(project string) string {
	return c.Gitiles + "/" + project
}

func (c *RealGerritClient) CreateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string, files map[string]string) (_ string, err error) {
	defer func() {
		// Check if status code is known to be not retryable.
		if he := (*gerrit.HTTPError)(nil); errors.As(err, &he) && he.Res.StatusCode/100 == 4 {
			ctx.DisableRetries()
		}
	}()

	reviewerEmails, err := coordinatorEmails(reviewers)
	if err != nil {
		return "", err
	}

	change, err := c.Client.CreateChange(ctx, input)
	if err != nil {
		return "", err
	}
	anyChange := false
	for path, content := range files {
		var err error
		if content == "" {
			err = c.Client.DeleteFileInChangeEdit(ctx, change.ID, path)
		} else {
			err = c.Client.ChangeFileContentInChangeEdit(ctx, change.ID, path, content)
		}
		if errors.Is(err, gerrit.ErrNotModified) {
			continue
		}
		if err != nil {
			return "", err
		}
		anyChange = true
	}
	if !anyChange {
		if err := c.Client.AbandonChange(ctx, change.ID, "no changes necessary"); err != nil {
			return "", err
		}
		return "", nil
	}

	if err := c.Client.PublishChangeEdit(ctx, change.ID); err != nil {
		return "", err
	}

	review := gerrit.ReviewInput{
		Labels: map[string]int{
			"Commit-Queue": 1,
			"Auto-Submit":  1,
		},
	}
	if v, ok := files["VERSION"]; input.Project == "go" && len(files) == 1 && ok && strings.HasPrefix(v, "go1.") {
		// As a special case for the main Go repo, if the only change is the content
		// of the VERSION file (still being some Go 1 version), opt to bypass trybots.
		// This file is overwritten by golangbuild during test execution anyway, so we rely
		// on tests run as part of the Go release process where the new VERSION file content
		// is set precisely to match that of the upcoming Go release. See go.dev/issue/73614.
		delete(review.Labels, "Commit-Queue")
		review.Labels["TryBot-Bypass"] = 1
	}
	for _, r := range reviewerEmails {
		review.Reviewers = append(review.Reviewers, gerrit.ReviewerInput{Reviewer: r})
	}
	if reviewError := c.Client.SetReview(ctx, change.ID, "current", review); reviewError != nil {
		ctx.Printf("CreateAutoSubmitChange: error setting a review on change %s: %v", change.ID, reviewError)

		// It's possible one of the Gerrit accounts has changed but the gophers package entry hasn't been
		// updated yet. If so, try setting review again, this time without any reviewers, then keep going.
		if he := (*gerrit.HTTPError)(nil); errors.As(reviewError, &he) && he.Res.StatusCode == http.StatusBadRequest {
			review.Reviewers = nil
			if err := c.Client.SetReview(ctx, change.ID, "current", review); err != nil {
				ctx.Printf("CreateAutoSubmitChange: error setting a review on change %s without reviewers: %v", change.ID, err)
			} else {
				ctx.Printf("CreateAutoSubmitChange: setting a review on change %s without reviewers succeeded", change.ID)
			}
		}
	}
	return change.ID, nil
}

func (c *RealGerritClient) Submitted(ctx context.Context, changeID, parentCommit string) (string, bool, error) {
	detail, err := c.Client.GetChangeDetail(ctx, changeID, gerrit.QueryChangesOpt{
		Fields: []string{"CURRENT_REVISION", "DETAILED_LABELS", "CURRENT_COMMIT"},
	})
	if err != nil {
		return "", false, err
	}
	if detail.Status == "MERGED" {
		parents := detail.Revisions[detail.CurrentRevision].Commit.Parents
		if parentCommit != "" && (len(parents) != 1 || parents[0].CommitID != parentCommit) {
			return "", false, fmt.Errorf("expected merged CL %v to have one parent commit %v, has %v", ChangeLink(changeID), parentCommit, parents)
		}
		return detail.CurrentRevision, true, nil
	}
	for _, approver := range detail.Labels["TryBot-Result"].All {
		if approver.Value < 0 {
			return "", false, fmt.Errorf("trybots failed on %v", ChangeLink(changeID))
		}
	}
	return "", false, nil
}

func (c *RealGerritClient) Tag(ctx context.Context, project, tag, commit string) error {
	info, err := c.Client.GetTag(ctx, project, tag)
	if err != nil && !errors.Is(err, gerrit.ErrResourceNotExist) {
		return fmt.Errorf("checking if tag already exists: %v", err)
	}
	if err == nil {
		if info.Revision != commit {
			return fmt.Errorf("tag %q already exists on revision %q rather than our %q", tag, info.Revision, commit)
		} else {
			// Nothing to do.
			return nil
		}
	}

	_, err = c.Client.CreateTag(ctx, project, tag, gerrit.TagInput{
		Revision: commit,
	})
	return err
}

func (c *RealGerritClient) ForceTag(ctx context.Context, project, tag, commit string) error {
	var needToDeleteExistingTag bool

	// Check for an existing tag.
	info, err := c.Client.GetTag(ctx, project, tag)
	if errors.Is(err, gerrit.ErrResourceNotExist) {
		// OK. We'll be creating it below.
	} else if err != nil {
		return fmt.Errorf("error checking if tag %q already exists: %v", tag, err)
	} else {
		if info.Revision == commit {
			// Nothing to do.
			return nil
		}
		// The tag exists but points elsewhere, so it needs to be deleted before being recreated.
		needToDeleteExistingTag = true
	}

	// Delete its previous version if needed.
	if needToDeleteExistingTag {
		err := c.Client.DeleteTag(ctx, project, tag)
		if err != nil {
			return err
		}
	}

	// Create new tag.
	_, err = c.Client.CreateTag(ctx, project, tag, gerrit.TagInput{
		Revision: commit,
	})
	return err
}

func (c *RealGerritClient) ListTags(ctx context.Context, project string) ([]string, error) {
	tags, err := c.Client.GetProjectTags(ctx, project)
	if err != nil {
		return nil, err
	}
	var tagNames []string
	for _, tag := range tags {
		tagNames = append(tagNames, strings.TrimPrefix(tag.Ref, "refs/tags/"))
	}
	return tagNames, nil
}

func (c *RealGerritClient) GetTag(ctx context.Context, project, tag string) (gerrit.TagInfo, error) {
	return c.Client.GetTag(ctx, project, tag)
}

func (c *RealGerritClient) ReadBranchHead(ctx context.Context, project, branch string) (string, error) {
	branchInfo, err := c.Client.GetBranch(ctx, project, branch)
	if err != nil {
		return "", err
	}
	return branchInfo.Revision, nil
}

func (c *RealGerritClient) ListBranches(ctx context.Context, project string) ([]gerrit.BranchInfo, error) {
	return c.Client.ListBranches(ctx, project)
}

func (c *RealGerritClient) CreateBranch(ctx context.Context, project, branch string, input gerrit.BranchInput) (string, error) {
	branchInfo, err := c.Client.CreateBranch(ctx, project, branch, input)
	if err != nil {
		return "", err
	}
	return branchInfo.Revision, nil
}

func (c *RealGerritClient) ListProjects(ctx context.Context) ([]string, error) {
	projects, err := c.Client.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return names, nil
}

func (c *RealGerritClient) ReadFile(ctx context.Context, project, commit, file string) ([]byte, error) {
	body, err := c.Client.GetFileContent(ctx, project, commit, file)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

func (c *RealGerritClient) ReadDir(ctx context.Context, project, commit, dir string) ([]struct{ Name string }, error) {
	var resp struct {
		Entries []struct{ Name string }
	}
	err := fetchGitilesJSON(ctx, c.GitilesAuth, c.Gitiles+"/"+url.PathEscape(project)+"/+/"+url.PathEscape(commit)+"/"+url.PathEscape(dir)+"?format=JSON", &resp)
	if err != nil {
		return nil, err
	}
	return resp.Entries, nil
}

func (c *RealGerritClient) ListCommits(ctx context.Context, project, head, base string) ([]CommitInfo, error) {
	switch {
	case head == "":
		return nil, fmt.Errorf("head is empty")
	case base == "":
		return nil, fmt.Errorf("base is empty")
	}
	var resp struct {
		Log []CommitInfo
	}
	err := fetchGitilesJSON(ctx, c.GitilesAuth, c.Gitiles+"/"+url.PathEscape(project)+"/+log/"+url.PathEscape(base)+".."+url.PathEscape(head)+"?format=JSON", &resp)
	if err != nil {
		return nil, err
	}
	return resp.Log, nil
}

func fetchGitilesJSON(ctx context.Context, auth oauth2.TokenSource, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if auth != nil {
		t, err := auth.Token()
		if err != nil {
			return err
		}
		t.SetAuthHeader(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("get %q: %w", req.URL, gerrit.ErrResourceNotExist)
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("did not get acceptable status code: %v body: %q", resp.Status, body)
	}
	if ct, want := resp.Header.Get("Content-Type"), "application/json"; ct != want {
		log.Printf("fetchGitilesJSON: got response with non-'application/json' Content-Type header %q\n", ct)
		if mediaType, _, err := mime.ParseMediaType(ct); err != nil {
			return fmt.Errorf("bad Content-Type header %q: %v", ct, err)
		} else if mediaType != "application/json" {
			return fmt.Errorf("got media type %q, want %q", mediaType, "application/json")
		}
	}
	const magicPrefix = ")]}'\n"
	var buf = make([]byte, len(magicPrefix))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		return err
	} else if !bytes.Equal(buf, []byte(magicPrefix)) {
		return fmt.Errorf("bad magic prefix")
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *RealGerritClient) GetCommitsInRefs(ctx context.Context, project string, commits, refs []string) (map[string][]string, error) {
	return c.Client.GetCommitsInRefs(ctx, project, commits, refs)
}

// ChangeLink returns a link to the review page for the CL with the specified
// change ID. The change ID must be in the project~cl# form.
func ChangeLink(changeID string) string {
	parts := strings.SplitN(changeID, "~", 3)
	if len(parts) != 2 {
		return fmt.Sprintf("(unparseable change ID %q)", changeID)
	}
	return "https://go.dev/cl/" + parts[1]
}

func (c *RealGerritClient) QueryChanges(ctx context.Context, query string) ([]*gerrit.ChangeInfo, error) {
	return c.Client.QueryChanges(ctx, query)
}

func (c *RealGerritClient) GetChange(ctx context.Context, changeID string, opts ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error) {
	return c.Client.GetChange(ctx, changeID, opts...)
}

func (c *RealGerritClient) SetHashtags(ctx context.Context, changeID string, hashtags gerrit.HashtagsInput) error {
	_, err := c.Client.SetHashtags(ctx, changeID, hashtags)
	return err
}

func (c *RealGerritClient) SubmitChange(ctx context.Context, changeID string) (gerrit.ChangeInfo, error) {
	return c.Client.SubmitChange(ctx, changeID)
}

func (c *RealGerritClient) CreateCherryPick(ctx context.Context, changeID string, branch string, commitMessage string) (gerrit.ChangeInfo, bool, error) {
	cpi := gerrit.CherryPickInput{Destination: branch, KeepReviewers: true, AllowConflicts: true, Message: commitMessage}
	ci, err := c.Client.CherryPickRevision(ctx, changeID, "current", cpi)
	if err != nil {
		return gerrit.ChangeInfo{}, false, err
	}
	if ci.ContainsGitConflicts {
		return ci, true, nil
	}
	if err := c.Client.SetReview(ctx, ci.ID, "current", gerrit.ReviewInput{
		Labels: map[string]int{
			"Commit-Queue": 1,
		},
	}); err != nil {
		return gerrit.ChangeInfo{}, false, err
	}
	return ci, false, nil
}

func (c *RealGerritClient) MoveChange(ctx context.Context, changeID string, branch string) (gerrit.ChangeInfo, error) {
	return c.Client.MoveChange(ctx, changeID, gerrit.MoveInput{DestinationBranch: branch})
}

func (c *RealGerritClient) RebaseChange(ctx context.Context, changeID string, base string) (gerrit.ChangeInfo, error) {
	return c.Client.RebaseChange(ctx, changeID, gerrit.RebaseInput{Base: base, AllowConflicts: true})
}

func (c *RealGerritClient) GetRevisionActions(ctx context.Context, changeID, revision string) (map[string]*gerrit.ActionInfo, error) {
	return c.Client.GetRevisionActions(ctx, changeID, revision)
}

func (c *RealGerritClient) GetCommitMessage(ctx context.Context, changeID string) (string, error) {
	cmi, err := c.Client.GetCommitMessage(ctx, changeID)
	if err != nil {
		return "", err
	}
	return cmi.FullMessage, nil
}
