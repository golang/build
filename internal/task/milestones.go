package task

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-github/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/build/internal/workflow"
	goversion "golang.org/x/build/maintner/maintnerd/maintapi/version"
)

// MilestoneTasks contains the tasks used to check and modify GitHub issues' milestones.
type MilestoneTasks struct {
	Client              GitHubClientInterface
	RepoOwner, RepoName string
}

// ReleaseKind is the type of release being run.
type ReleaseKind int

const (
	KindUnknown ReleaseKind = iota
	KindBeta
	KindRC
	KindFinal
	KindMinor
)

type ReleaseMilestones struct {
	Current, Next int
}

// FetchMilestones returns the milestone numbers for the version currently being
// released, and the next version that outstanding issues should be moved to.
// If this is a final release, it also creates its first minor release
// milestone.
func (m *MilestoneTasks) FetchMilestones(ctx *workflow.TaskContext, currentVersion string, kind ReleaseKind) (ReleaseMilestones, error) {
	x, ok := goversion.Go1PointX(currentVersion)
	if !ok {
		return ReleaseMilestones{}, fmt.Errorf("could not parse %q as a Go version", currentVersion)
	}
	majorVersion := fmt.Sprintf("go1.%d", x)

	// RCs and betas use the major version's milestone.
	if kind == KindRC || kind == KindBeta {
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
	if kind == KindFinal {
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

func nextVersion(version string) (string, error) {
	parts := strings.Split(version, ".")
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return "", err
	}
	parts[len(parts)-1] = strconv.Itoa(n + 1)
	return strings.Join(parts, "."), nil
}

func uppercaseVersion(version string) string {
	return strings.Replace(version, "go", "Go", 1)
}

// CheckBlockers returns an error if there are open release blockers in
// the current milestone.
func (m *MilestoneTasks) CheckBlockers(ctx *workflow.TaskContext, milestones ReleaseMilestones, kind ReleaseKind) (string, error) {
	issues, err := m.loadMilestoneIssues(ctx, milestones.Current, kind)
	if err != nil {
		return "", err
	}
	var blockers []string
	for number, blocker := range issues {
		if blocker {
			blockers = append(blockers, fmt.Sprintf("https://go.dev/issue/%v", number))
		}
	}
	sort.Strings(blockers)
	if len(blockers) != 0 {
		return "", fmt.Errorf("open release blockers:\n%v", strings.Join(blockers, "\n"))
	}
	return "", nil
}

// loadMilestoneIssues returns all the open issues in milestone and whether
// they should block the given kind of release.
func (m *MilestoneTasks) loadMilestoneIssues(ctx *workflow.TaskContext, milestoneID int, kind ReleaseKind) (map[int]bool, error) {
	issues := map[int]bool{}
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
	if err := m.Client.Query(ctx, &query, map[string]interface{}{
		"repoOwner":       githubv4.String(m.RepoOwner),
		"repoName":        githubv4.String(m.RepoName),
		"milestoneNumber": githubv4.String(fmt.Sprint(milestoneID)),
		"afterToken":      afterToken,
	}); err != nil {
		return nil, err
	}
	for _, issue := range query.Repository.Issues.Nodes {
		if issue.Labels.PageInfo.HasNextPage {
			return nil, fmt.Errorf("issue %v (#%v) has more than 10 labels", issue.Title, issue.Number)
		}
		releaseBlocker := false
		betaOK := false
		for _, label := range issue.Labels.Nodes {
			if label.Name == "release-blocker" {
				releaseBlocker = true
			}
			if label.Name == "okay-after-beta1" {
				betaOK = true
			}
		}
		if kind == KindBeta && betaOK {
			releaseBlocker = false
		}
		issues[issue.Number] = releaseBlocker
	}
	if query.Repository.Issues.PageInfo.HasNextPage {
		afterToken = &query.Repository.Issues.PageInfo.EndCursor
		goto more
	}
	return issues, nil
}

// PushIssues moves all the open issues in the current milestone to the next one.
func (m *MilestoneTasks) PushIssues(ctx *workflow.TaskContext, milestones ReleaseMilestones) (string, error) {
	issues, err := m.loadMilestoneIssues(ctx, milestones.Current, KindUnknown)
	if err != nil {
		return "", err
	}
	for issueNumber := range issues {
		_, _, err := m.Client.EditIssue(ctx, m.RepoOwner, m.RepoName, issueNumber, &github.IssueRequest{
			Milestone: &milestones.Next,
		})
		if err != nil {
			return "", err
		}
	}
	return "", nil
}

// GitHubClientInterface is a wrapper around the GitHub v3 and v4 APIs, for
// testing and dry-run support.
type GitHubClientInterface interface {
	// FetchMilestone returns the number of the requested milestone. If create is true,
	// and the milestone doesn't exist, it will be created.
	FetchMilestone(ctx context.Context, owner, repo, name string, create bool) (int, error)

	// See githubv4.Client.Query.
	Query(ctx context.Context, q interface{}, variables map[string]interface{}) error

	// See github.Client.Issues.Edit.
	EditIssue(ctx context.Context, owner string, repo string, number int, issue *github.IssueRequest) (*github.Issue, *github.Response, error)
}

type GitHubClient struct {
	V3 *github.Client
	V4 *githubv4.Client
}

func (c *GitHubClient) Query(ctx context.Context, q interface{}, variables map[string]interface{}) error {
	return c.V4.Query(ctx, q, variables)
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
	if err := client.Query(ctx, &query, map[string]interface{}{
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

func (c *GitHubClient) EditIssue(ctx context.Context, owner string, repo string, number int, issue *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return c.V3.Issues.Edit(ctx, owner, repo, number, issue)
}
