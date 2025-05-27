// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v48/github"
	"github.com/shurcooL/githubv4"
	wf "golang.org/x/build/internal/workflow"
	goversion "golang.org/x/build/maintner/maintnerd/maintapi/version"
)

// MilestoneTasks contains the tasks used to check and modify GitHub issues' milestones.
type MilestoneTasks struct {
	Client              GitHubClientInterface
	RepoOwner, RepoName string
	ApproveAction       func(*wf.TaskContext) error
}

// ReleaseKind is the type of release being run.
type ReleaseKind int

const (
	KindUnknown ReleaseKind = iota
	KindBeta
	KindRC
	KindMajor
	KindMinor
)

func (k ReleaseKind) GoString() string {
	switch k {
	case KindUnknown:
		return "KindUnknown"
	case KindBeta:
		return "KindBeta"
	case KindRC:
		return "KindRC"
	case KindMajor:
		return "KindMajor"
	case KindMinor:
		return "KindMinor"
	default:
		return fmt.Sprintf("ReleaseKind(%d)", k)
	}
}

type ReleaseMilestones struct {
	// Current is the GitHub milestone number for the current Go release.
	// For example, 279 for the "Go1.21" milestone (https://github.com/golang/go/milestone/279).
	Current int
	// Next is the GitHub milestone number for the next Go release of the same kind.
	Next int
}

// FetchMilestones returns the milestone numbers for the version currently being
// released, and the next version that outstanding issues should be moved to.
// If this is a major release, it also creates its first minor release
// milestone.
func (m *MilestoneTasks) FetchMilestones(ctx *wf.TaskContext, currentVersion string, kind ReleaseKind) (ReleaseMilestones, error) {
	x, ok := goversion.Go1PointX(currentVersion)
	if !ok {
		return ReleaseMilestones{}, fmt.Errorf("could not parse %q as a Go version", currentVersion)
	}
	majorVersion := fmt.Sprintf("go1.%d", x)

	// Betas, RCs, and major releases use the major version's milestone.
	if kind == KindBeta || kind == KindRC || kind == KindMajor {
		currentVersion = majorVersion
	}

	currentMilestone, err := m.Client.FetchMilestone(ctx, m.RepoOwner, m.RepoName, uppercaseVersion(currentVersion), false)
	if err != nil {
		return ReleaseMilestones{}, err
	}
	nextV, err := nextVersion(currentVersion)
	if err != nil {
		return ReleaseMilestones{}, err
	}
	nextMilestone, err := m.Client.FetchMilestone(ctx, m.RepoOwner, m.RepoName, uppercaseVersion(nextV), true)
	if err != nil {
		return ReleaseMilestones{}, err
	}
	if kind == KindMajor {
		// Create the first minor release milestone too.
		firstMinor := majorVersion + ".1"
		if err != nil {
			return ReleaseMilestones{}, err
		}
		_, err = m.Client.FetchMilestone(ctx, m.RepoOwner, m.RepoName, uppercaseVersion(firstMinor), true)
		if err != nil {
			return ReleaseMilestones{}, err
		}
	}
	return ReleaseMilestones{Current: currentMilestone, Next: nextMilestone}, nil
}

func uppercaseVersion(version string) string {
	return strings.Replace(version, "go", "Go", 1)
}

// CheckBlockers returns an error if there are open release blockers in
// the current milestone.
func (m *MilestoneTasks) CheckBlockers(ctx *wf.TaskContext, milestones ReleaseMilestones, version string, kind ReleaseKind) error {
	issues, err := m.Client.FetchMilestoneIssues(ctx, m.RepoOwner, m.RepoName, milestones.Current)
	if err != nil {
		return err
	}
	var blockers []string
	for number, labels := range issues {
		releaseBlocker := labels["release-blocker"]
		switch {
		case kind == KindBeta && strings.HasSuffix(version, "beta1") && labels["okay-after-beta1"],
			kind == KindRC && strings.HasSuffix(version, "rc1") && labels["okay-after-rc1"]:
			releaseBlocker = false
		}
		if releaseBlocker {
			blockers = append(blockers, fmt.Sprintf("https://go.dev/issue/%v", number))
		}
	}
	sort.Strings(blockers)
	if len(blockers) == 0 {
		return nil
	}
	ctx.Printf("There are open release blockers in https://github.com/golang/go/milestone/%d. Check that they're expected and approve this task:\n%v",
		milestones.Current, strings.Join(blockers, "\n"))
	return m.ApproveAction(ctx)
}

// RelnoteTracking holds milestone and issue numbers for tracking writing release notes.
type RelnoteTracking struct {
	Milestone int // Milestone number.
	Issue     int // Issue number.
}

// FetchRelnoteMilestoneAndIssue finds the Go 1.N milestone number and the "doc: write
// release notes for Go 1.N" issue number for the specified Go 1.N development version.
func (m *MilestoneTasks) FetchRelnoteMilestoneAndIssue(ctx *wf.TaskContext, develVersion int) (RelnoteTracking, error) {
	gh, ok := m.Client.(*GitHubClient)
	if !ok || gh.V4 == nil {
		// It's not worth moving the GraphQL query/mutation into GitHubClientInterface
		// at this time. That harms readability because GraphQL code is a flexible API
		// call and is most readable when close to where it's used.
		return RelnoteTracking{}, fmt.Errorf("no GitHub API v4 client")
	}

	milestoneName := fmt.Sprintf("Go1.%d", develVersion)
	relnoteIssueTitle := fmt.Sprintf("doc: write release notes for Go 1.%d", develVersion)

	milestoneNumber, err := m.Client.FetchMilestone(ctx, m.RepoOwner, m.RepoName, milestoneName, false)
	if err != nil {
		return RelnoteTracking{}, err
	}
	var q struct {
		Search struct {
			Nodes []struct {
				Issue struct {
					Number int
					Title  string
				} `graphql:"...on Issue"`
			}
			IssueCount int
		} `graphql:"search(first: 100, type:ISSUE, query: $relnoteIssueQuery)"`
	}
	relnoteIssueQuery := fmt.Sprintf("repo:%s/%s type:issue state:open milestone:%s label:Documentation label:release-blocker in:title %q", m.RepoOwner, m.RepoName, milestoneName, relnoteIssueTitle)
	if err := gh.V4.Query(ctx, &q, map[string]any{
		"relnoteIssueQuery": githubv4.String(relnoteIssueQuery),
	}); err != nil {
		return RelnoteTracking{}, err
	}
	for _, n := range q.Search.Nodes {
		if n.Issue.Title == relnoteIssueTitle {
			return RelnoteTracking{Milestone: milestoneNumber, Issue: n.Issue.Number}, nil
		}
	}
	return RelnoteTracking{}, fmt.Errorf("release notes issue not found within %d matching issues (see https://github.com/search?type=issues&q=%s)", q.Search.IssueCount, url.QueryEscape(relnoteIssueQuery))
}

// PushIssues updates issues to reflect a finished release.
// For major and minor releases, it moves issues to the next milestone and closes the current milestone.
// For pre-releases, it cleans up any "okay-after-..." labels in the current milestone that are done serving their purpose.
func (m *MilestoneTasks) PushIssues(ctx *wf.TaskContext, milestones ReleaseMilestones, version string, kind ReleaseKind) error {
	issues, err := m.Client.FetchMilestoneIssues(ctx, m.RepoOwner, m.RepoName, milestones.Current)
	if err != nil {
		return err
	}
	ctx.Printf("Processing %d open issues in milestone %d.", len(issues), milestones.Current)
	for issueNumber, labels := range issues {
		var newLabels *[]string
		var newMilestone *int
		var actions []string // A short description of actions taken, for the log line.
		removeLabel := func(name string) {
			if !labels[name] {
				return
			}
			newLabels = new([]string)
			for label := range labels {
				if label == name {
					continue
				}
				*newLabels = append(*newLabels, label)
			}
			actions = append(actions, fmt.Sprintf("removed label %q", name))
		}
		if kind == KindBeta && strings.HasSuffix(version, "beta1") {
			removeLabel("okay-after-beta1")
		} else if kind == KindRC && strings.HasSuffix(version, "rc1") {
			removeLabel("okay-after-rc1")
		} else if kind == KindMajor || kind == KindMinor {
			newMilestone = &milestones.Next
			actions = append(actions, fmt.Sprintf("pushed to milestone %d", milestones.Next))
		}
		if newMilestone == nil && newLabels == nil {
			ctx.Printf("Nothing to do for issue %d.", issueNumber)
			continue
		}
		_, _, err := m.Client.EditIssue(ctx, m.RepoOwner, m.RepoName, issueNumber, &github.IssueRequest{
			Milestone: newMilestone,
			Labels:    newLabels,
		})
		if err != nil {
			return err
		}
		ctx.Printf("Updated issue %d: %s.", issueNumber, strings.Join(actions, ", "))
	}
	if kind == KindMajor || kind == KindMinor {
		_, _, err := m.Client.EditMilestone(ctx, m.RepoOwner, m.RepoName, milestones.Current, &github.Milestone{
			State: github.String("closed"),
		})
		if err != nil {
			return err
		}
		ctx.Printf("Closed milestone %d.", milestones.Current)
	}
	return nil
}

// PingEarlyIssues pings early-in-cycle issues in the development major release milestone.
// This is done once at the opening of a release cycle, currently via a standalone workflow.
//
// develVersion is a value like 22 representing that Go 1.22 is the major version whose
// development has recently started, and whose early-in-cycle issues are to be pinged.
func (m *MilestoneTasks) PingEarlyIssues(ctx *wf.TaskContext, develVersion int, openTreeURL string) (result struct{}, _ error) {
	gh, ok := m.Client.(*GitHubClient)
	if !ok || gh.V4 == nil {
		// It's not worth moving the GraphQL query/mutation into GitHubClientInterface
		// at this time. That harms readability because GraphQL code is a flexible API
		// call and is most readable when close to where it's used.
		return struct{}{}, fmt.Errorf("no GitHub API v4 client")
	}

	milestoneName := fmt.Sprintf("Go1.%d", develVersion)

	// Find all open early-in-cycle issues in the development major release milestone.
	type issue struct {
		ID     githubv4.ID
		Number int
		Title  string

		TimelineItems struct {
			Nodes []struct {
				IssueComment struct {
					Author struct{ Login string }
					Body   string
				} `graphql:"...on IssueComment"`
			}
		} `graphql:"timelineItems(since: $avoidDupSince, itemTypes: ISSUE_COMMENT, last: 100)"`
	}
	var earlyIssues []issue
	milestoneNumber, err := m.Client.FetchMilestone(ctx, m.RepoOwner, m.RepoName, milestoneName, false)
	if err != nil {
		return struct{}{}, err
	}
	variables := map[string]any{
		"repoOwner":       githubv4.String(m.RepoOwner),
		"repoName":        githubv4.String(m.RepoName),
		"avoidDupSince":   githubv4.DateTime{Time: time.Now().Add(-30 * 24 * time.Hour)},
		"milestoneNumber": githubv4.String(fmt.Sprint(milestoneNumber)), // The IssueFilters.milestoneNumber input in GitHub API v4 uses String type.
		"issueCursor":     (*githubv4.String)(nil),
	}
	for {
		var q struct {
			Repository struct {
				Issues struct {
					Nodes    []issue
					PageInfo struct {
						EndCursor   githubv4.String
						HasNextPage bool
					}
				} `graphql:"issues(first: 100, after: $issueCursor, filterBy: {states: OPEN, labels: \"early-in-cycle\", milestoneNumber: $milestoneNumber}, orderBy: {field: CREATED_AT, direction: ASC})"`
			} `graphql:"repository(owner: $repoOwner, name: $repoName)"`
		}
		err := gh.V4.Query(ctx, &q, variables)
		if err != nil {
			return struct{}{}, err
		}
		earlyIssues = append(earlyIssues, q.Repository.Issues.Nodes...)
		if !q.Repository.Issues.PageInfo.HasNextPage {
			break
		}
		variables["issueCursor"] = githubv4.NewString(q.Repository.Issues.PageInfo.EndCursor)
	}

	// Ping them.
	ctx.Printf("Processing %d early-in-cycle issues in %s milestone (milestone number %d).", len(earlyIssues), milestoneName, milestoneNumber)
EarlyIssuesLoop:
	for _, i := range earlyIssues {
		for _, n := range i.TimelineItems.Nodes {
			if n.IssueComment.Author.Login == "gopherbot" && strings.Contains(n.IssueComment.Body, "friendly reminder") {
				ctx.Printf("Skipping issue %d, it was already pinged.", i.Number)
				continue EarlyIssuesLoop
			}
		}

		// Post a comment.
		const dryRun = false
		if dryRun {
			ctx.Printf("[dry run] Would've pinged issue %d (%.32s…).", i.Number, i.Title)
			continue
		}
		err := m.Client.PostComment(ctx, i.ID, fmt.Sprintf("This issue is currently labeled as early-in-cycle for Go 1.%d.\n"+
			"That [time is now](%s), so a friendly reminder to look at it again.", develVersion, openTreeURL))
		if err != nil {
			return struct{}{}, err
		}
		ctx.Printf("Pinged issue %d (%.32s…).", i.Number, i.Title)
		time.Sleep(3 * time.Second) // Take a moment between pinging issues to avoid a high rate of addComment mutations.
	}

	return struct{}{}, nil
}

// GitHubClientInterface is a wrapper around the GitHub v3 and v4 APIs, for
// testing and dry-run support.
type GitHubClientInterface interface {
	// FetchMilestone returns the number of the GitHub milestone with the specified name.
	// If create is true, and the milestone doesn't exist, it will be created.
	FetchMilestone(ctx context.Context, owner, repo, name string, create bool) (int, error)

	// FetchMilestoneIssues returns all the open issues in the specified milestone
	// and their labels.
	FetchMilestoneIssues(ctx context.Context, owner, repo string, milestoneID int) (map[int]map[string]bool, error)

	// See github.Client.Issues.Create.
	CreateIssue(ctx context.Context, owner, repo string, issue *github.IssueRequest) (*github.Issue, *github.Response, error)

	// See github.Client.Issues.Edit.
	EditIssue(ctx context.Context, owner, repo string, number int, issue *github.IssueRequest) (*github.Issue, *github.Response, error)

	// See github.Client.Issues.Get.
	GetIssue(ctx context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error)

	// See github.Client.Issues.EditMilestone.
	EditMilestone(ctx context.Context, owner, repo string, number int, milestone *github.Milestone) (*github.Milestone, *github.Response, error)

	// PostComment creates a comment on a GitHub issue or pull request
	// identified by the given GitHub Node ID.
	PostComment(_ context.Context, id githubv4.ID, body string) error

	// See github.Client.Repositories.CreateRelease.
	CreateRelease(ctx context.Context, owner, repo string, release *github.RepositoryRelease) (*github.RepositoryRelease, error)

	// PublishRelease publishes the release by marking draft as false if not
	// already.
	// See github.Client.Repositories.EditRelease.
	PublishRelease(ctx context.Context, owner, repo string, release *github.RepositoryRelease) (*github.RepositoryRelease, error)

	// UploadReleaseAsset uploads an fs.File to a GitHub release as a release
	// asset.
	// It uses NewUploadRequest as github.Client.Repositories.UploadReleaseAsset
	// only supports uploading from an os.File.
	// Parameters:
	//   - owner:     The account owner of the repository.
	//   - repo:      The name of the repository.
	//   - releaseID: The ID of the github release.
	//   - fileName:  The name of the asset as it will appear in the release.
	//   - file:      The content of the file to upload.
	UploadReleaseAsset(ctx context.Context, owner, repo string, releaseID int64, fileName string, file fs.File) (*github.ReleaseAsset, error)
}

type GitHubClient struct {
	V3 *github.Client
	V4 *githubv4.Client
}

func (c *GitHubClient) FetchMilestone(ctx context.Context, owner, repo, name string, create bool) (int, error) {
	n, found, err := findMilestone(ctx, c.V4, owner, repo, name)
	if err != nil {
		return 0, err
	}
	if found {
		return n, nil
	} else if !create {
		return 0, fmt.Errorf("no milestone named %q found, and creation was disabled", name)
	}
	m, _, createErr := c.V3.Issues.CreateMilestone(ctx, owner, repo, &github.Milestone{
		Title: github.String(name),
	})
	if createErr != nil {
		return 0, fmt.Errorf("could not find an open milestone named %q and creating it failed: %v", name, createErr)
	}
	return *m.Number, nil
}

func (c *GitHubClient) UploadReleaseAsset(ctx context.Context, owner, repo string, releaseID int64, fileName string, file fs.File) (*github.ReleaseAsset, error) {
	// Query parameter "name" is used to determine the asset name.
	// See details https://docs.github.com/en/rest/releases/assets?apiVersion=2022-11-28#upload-a-release-asset
	u := fmt.Sprintf("repos/%s/%s/releases/%d/assets?name=%s", owner, repo, releaseID, url.QueryEscape(fileName))

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if stat.IsDir() {
		return nil, errors.New("the asset to upload can't be a directory")
	}

	req, err := c.V3.NewUploadRequest(u, file, stat.Size(), "")
	if err != nil {
		return nil, err
	}

	asset := new(github.ReleaseAsset)
	if _, err = c.V3.Do(ctx, req, asset); err != nil {
		return nil, err
	}
	return asset, nil
}

func (c *GitHubClient) CreateRelease(ctx context.Context, owner, repo string, release *github.RepositoryRelease) (*github.RepositoryRelease, error) {
	release, _, err := c.V3.Repositories.CreateRelease(ctx, owner, repo, release)
	return release, err
}

func (c *GitHubClient) PublishRelease(ctx context.Context, owner, repo string, release *github.RepositoryRelease) (*github.RepositoryRelease, error) {
	if release.Draft != nil && !*release.Draft {
		return release, nil
	}
	release, _, err := c.V3.Repositories.EditRelease(ctx, owner, repo, release.GetID(), &github.RepositoryRelease{
		Draft: github.Bool(false),
	})
	return release, err
}

func findMilestone(ctx context.Context, client *githubv4.Client, owner, repo, name string) (int, bool, error) {
	var query struct {
		Repository struct {
			Milestones struct {
				Nodes []struct {
					Title  string
					Number int
					State  string
				}
			} `graphql:"milestones(first:10, query: $milestoneName)"`
		} `graphql:"repository(owner: $repoOwner, name: $repoName)"`
	}
	if err := client.Query(ctx, &query, map[string]any{
		"repoOwner":     githubv4.String(owner),
		"repoName":      githubv4.String(repo),
		"milestoneName": githubv4.String(name),
	}); err != nil {
		return 0, false, err
	}
	// The milestone query is case-insensitive and a partial match; we're okay
	// with case variations but it needs to be a full match.
	var open, closed []string
	milestoneNumber := 0
	for _, m := range query.Repository.Milestones.Nodes {
		if strings.ToLower(name) != strings.ToLower(m.Title) {
			continue
		}
		if m.State == "OPEN" {
			open = append(open, m.Title)
			milestoneNumber = m.Number
		} else {
			closed = append(closed, m.Title)
		}
	}
	// GitHub allows "go" and "Go" to exist at the same time.
	// If there's any confusion, fail: we expect either one open milestone,
	// or no matching milestones at all.
	switch {
	case len(open) == 1:
		return milestoneNumber, true, nil
	case len(open) > 1:
		return 0, false, fmt.Errorf("multiple open milestones matching %q: %q", name, open)
	// No open milestones.
	case len(closed) == 0:
		return 0, false, nil
	case len(closed) > 0:
		return 0, false, fmt.Errorf("no open milestones matching %q, but some closed: %q (re-open or delete?)", name, closed)
	}
	// The switch above is exhaustive.
	panic(fmt.Errorf("unhandled case: open: %q closed: %q", open, closed))
}

func (c *GitHubClient) FetchMilestoneIssues(ctx context.Context, owner, repo string, milestoneID int) (map[int]map[string]bool, error) {
	issues := map[int]map[string]bool{}
	var query struct {
		Repository struct {
			Issues struct {
				PageInfo struct {
					EndCursor   githubv4.String
					HasNextPage bool
				}

				Nodes []struct {
					Number int
					ID     githubv4.ID
					Title  string
					Labels struct {
						PageInfo struct {
							HasNextPage bool
						}
						Nodes []struct {
							Name string
						}
					} `graphql:"labels(first:10)"`
				}
			} `graphql:"issues(first:100, after:$afterToken, filterBy:{states:OPEN, milestoneNumber:$milestoneNumber})"`
		} `graphql:"repository(owner: $repoOwner, name: $repoName)"`
	}
	var afterToken *githubv4.String
more:
	if err := c.V4.Query(ctx, &query, map[string]any{
		"repoOwner":       githubv4.String(owner),
		"repoName":        githubv4.String(repo),
		"milestoneNumber": githubv4.String(fmt.Sprint(milestoneID)),
		"afterToken":      afterToken,
	}); err != nil {
		return nil, err
	}
	for _, issue := range query.Repository.Issues.Nodes {
		if issue.Labels.PageInfo.HasNextPage {
			return nil, fmt.Errorf("issue %v (#%v) has more than 10 labels", issue.Title, issue.Number)
		}
		labels := map[string]bool{}
		for _, label := range issue.Labels.Nodes {
			labels[label.Name] = true
		}
		issues[issue.Number] = labels
	}
	if query.Repository.Issues.PageInfo.HasNextPage {
		afterToken = &query.Repository.Issues.PageInfo.EndCursor
		goto more
	}
	return issues, nil
}

func (c *GitHubClient) EditIssue(ctx context.Context, owner, repo string, number int, issue *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return c.V3.Issues.Edit(ctx, owner, repo, number, issue)
}

func (c *GitHubClient) CreateIssue(ctx context.Context, owner, repo string, issue *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return c.V3.Issues.Create(ctx, owner, repo, issue)
}

func (c *GitHubClient) GetIssue(ctx context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error) {
	return c.V3.Issues.Get(ctx, owner, repo, number)
}

func (c *GitHubClient) EditMilestone(ctx context.Context, owner, repo string, number int, milestone *github.Milestone) (*github.Milestone, *github.Response, error) {
	return c.V3.Issues.EditMilestone(ctx, owner, repo, number, milestone)
}

func (c *GitHubClient) PostComment(ctx context.Context, id githubv4.ID, body string) error {
	return c.V4.Mutate(ctx, new(struct {
		AddComment struct {
			ClientMutationID string // Unused; GraphQL doesn't allow for mutations to return nothing.
		} `graphql:"addComment(input: $input)"`
	}), githubv4.AddCommentInput{
		SubjectID: id,
		Body:      githubv4.String(body),
	}, nil)
}
