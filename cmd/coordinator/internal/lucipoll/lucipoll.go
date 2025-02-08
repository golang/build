// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package lucipoll implements a simple polling LUCI client
// for the possibly-short-term needs of the build dashboard.
package lucipoll

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	bbpb "go.chromium.org/luci/buildbucket/proto"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/repos"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// maintnerClient is a subset of apipb.MaintnerServiceClient.
type maintnerClient interface {
	// GetDashboard is extracted from apipb.MaintnerServiceClient.
	GetDashboard(ctx context.Context, in *apipb.DashboardRequest, opts ...grpc.CallOption) (*apipb.DashboardResponse, error)
}

type Builder struct {
	Name string
	*BuilderConfigProperties
}

type BuilderConfigProperties struct {
	Repo     string `json:"project"`
	GoBranch string `json:"go_branch"`
	Target   struct {
		GOOS   string `json:"goos"`
		GOARCH string `json:"goarch"`
	} `json:"target"`
	KnownIssue int `json:"known_issue"`
}

type Build struct {
	ID          int64
	BuilderName string
	Status      bbpb.Status
}

func NewService(maintCl maintnerClient, buildersCl bbpb.BuildersClient, buildsCl bbpb.BuildsClient) *service {
	s := &service{
		maintCl:    maintCl,
		buildersCl: buildersCl,
		buildsCl:   buildsCl,
	}
	go s.pollLoop()
	return s
}

type service struct {
	maintCl maintnerClient

	buildersCl bbpb.BuildersClient
	buildsCl   bbpb.BuildsClient

	mu     sync.RWMutex
	cached Snapshot
}

// A Snapshot is a consistent snapshot in time holding LUCI post-submit state.
type Snapshot struct {
	Builders         map[string]Builder                     // Map key is builder name.
	RepoCommitBuilds map[string]map[string]map[string]Build // Map keys are repo, commit ID, builder name.
}

// PostSubmitSnapshot returns a cached snapshot.
func (s *service) PostSubmitSnapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cached
}

func (s *service) pollLoop() {
	// A hard timeout for runOnce to complete.
	// Normally it takes about a minute or so.
	// Sometimes (a few times a week) it takes 24 hours and a minute.
	// Don't let it run more than 30 minutes, so we'll find out trying
	// again sooner can help, at least until the root problem is fixed.
	// See go.dev/issue/66687.
	const runOnceTimeout = 30 * time.Minute

	ticker := time.NewTicker(2 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), runOnceTimeout)
		builders, builds, err := runOnce(ctx, s.maintCl, s.buildersCl, s.buildsCl)
		cancel()
		if err != nil {
			log.Println("lucipoll:", err)
			// Sleep a bit and retry.
			time.Sleep(30 * time.Second)
			continue
		}
		s.mu.Lock()
		s.cached = Snapshot{builders, builds}
		s.mu.Unlock()
		<-ticker.C // Limit how often we're willing to poll.
	}
}

func runOnce(
	ctx context.Context,
	maintCl maintnerClient, buildersCl bbpb.BuildersClient, buildsCl bbpb.BuildsClient,
) (_ map[string]Builder, _ map[string]map[string]map[string]Build, err error) {
	defer func() {
		if e := recover(); e != nil {
			err = fmt.Errorf("internal panic: %v\n\n%s", e, debug.Stack())
		}
	}()

	// Fetch all current completed LUCI builders.
	//
	// TODO: It would be possible to cache initially fetched builders and then fetch
	// additional individual builders when seeing a build referencing an unknown one.
	// But that would need to take into account that a builder may be intentionally
	// removed from the LUCI dashboard. It adds more complexity, so for now do the
	// simple thing and save caching as an optional enhancement.
	builderList, err := listBuilders(ctx, buildersCl)
	if err != nil {
		return nil, nil, err
	}
	var builders = make(map[string]Builder)
	for _, b := range builderList {
		if _, ok := builders[b.Name]; ok {
			return nil, nil, fmt.Errorf("duplicate builder name %q", b.Name)
		}
		if b.KnownIssue != 0 {
			// Skip LUCI builders with a known issue at this time.
			// This also means builds from these builders are skipped below as well.
			// Such builders&builds can be included when the callers deem it useful.
			continue
		}
		builders[b.Name] = b
	}

	// Fetch LUCI builds for the builders, repositories, and their commits
	// that are deemed relevant to the callers of this package.
	//
	// TODO: It would be possible to cache the last GetDashboard response
	// and if didn't change since the last, only fetch new LUCI builds
	// since then. Similarly, builds that were earlier for commits that
	// still show up in the response can be reused instead of refetched.
	// Furthermore, builds can be sorted according to how complete/useful
	// they are. These known enhancements are left for later as needed.
	var builds = make(map[string]map[string]map[string]Build)
	dashResp, err := maintCl.GetDashboard(ctx, &apipb.DashboardRequest{MaxCommits: 30})
	if err != nil {
		return nil, nil, err
	}
	var used, total int
	t0 := time.Now()
	// Fetch builds for Go repo commits.
	for _, c := range dashResp.Commits {
		repo, commit := "go", c.Commit
		buildList, err := fetchBuildsForCommit(ctx, buildsCl, repo, commit, "id", "builder.builder", "status", "input.gitiles_commit")
		if err != nil {
			return nil, nil, err
		}
		total += len(buildList)
		for _, b := range buildList {
			if c := b.GetInput().GetGitilesCommit(); c.Project != repo {
				return nil, nil, fmt.Errorf(`internal error: in Go repo commit loop, c.Project is %q but expected it to be "go"`, c.Project)
			} else if c.Id != commit {
				return nil, nil, fmt.Errorf("internal error: in Go repo commit loop, c.Id is %q but expected it to be %q", c.Id, commit)
			}
			switch b.GetStatus() {
			case bbpb.Status_STARTED, bbpb.Status_SUCCESS, bbpb.Status_FAILURE, bbpb.Status_INFRA_FAILURE:
			default:
				// Skip builds with other statuses at this time.
				// Such builds can be included when the callers deem it useful.
				continue
			}
			builder, ok := builders[b.GetBuilder().GetBuilder()]
			if !ok {
				// A build that isn't associated with a current builder we're tracking.
				// It might've been removed, or has a known issue. Skip this build too.
				continue
			} else if builder.Repo != "go" {
				// Not a Go repo build. Those are handled below, so out of scope here.
				continue
			}
			if builds[repo] == nil {
				builds[repo] = make(map[string]map[string]Build)
			}
			if builds[repo][commit] == nil {
				builds[repo][commit] = make(map[string]Build)
			}
			builds[repo][commit][b.GetBuilder().GetBuilder()] = Build{
				ID:          b.GetId(),
				BuilderName: b.GetBuilder().GetBuilder(),
				Status:      b.GetStatus(),
			}
			used++
		}
	}
	// Fetch builds for the single latest commit of each golang.org/x repo,
	// ones that were invoked from the Go repository side.
	var repoHeads = make(map[string]string) // A repo → head commit ID map.
	for _, rh := range dashResp.RepoHeads {
		repoHeads[rh.GerritProject] = rh.Commit.Commit
	}
	for _, r := range dashResp.Releases {
		repo, commit := "go", r.GetBranchCommit()
		buildList, err := fetchBuildsForCommit(ctx, buildsCl, repo, commit, "id", "builder.builder", "status", "input.gitiles_commit", "output.properties")
		if err != nil {
			return nil, nil, err
		}
		total += len(buildList)
		for _, b := range buildList {
			if c := b.GetInput().GetGitilesCommit(); c.Project != "go" {
				return nil, nil, fmt.Errorf(`internal error: in x/ repo loop for builds invoked from the Go repo side, c.Project is %q but expected it to be "go"`, c.Project)
			}
			switch b.GetStatus() {
			case bbpb.Status_STARTED, bbpb.Status_SUCCESS, bbpb.Status_FAILURE, bbpb.Status_INFRA_FAILURE:
			default:
				// Skip builds with other statuses at this time.
				// Such builds can be included when the callers deem it useful.
				continue
			}
			builder, ok := builders[b.GetBuilder().GetBuilder()]
			if !ok {
				// A build that isn't associated with a current builder we're tracking.
				// It might've been removed, or has a known issue. Skip this build too.
				continue
			} else if builder.Repo == "go" {
				// A Go repo build. Those were handled above, so out of scope here.
				continue
			}
			var buildOutputProps struct {
				Sources []struct {
					GitilesCommit struct {
						Project string `json:"project"`
						Ref     string `json:"ref"`
						Id      string `json:"id"`
					} `json:"gitiles_commit"`
				} `json:"sources"`
			}
			if data, err := b.GetOutput().GetProperties().MarshalJSON(); err != nil {
				return nil, nil, fmt.Errorf("marshaling build output properties to JSON failed: %v", err)
			} else if err := json.Unmarshal(data, &buildOutputProps); err != nil {
				return nil, nil, err
			}
			repoCommit, ok := func() (string, bool) {
				for _, s := range buildOutputProps.Sources {
					if c := s.GitilesCommit; c.Project == builder.Repo {
						if c.Ref != "refs/heads/master" {
							panic(fmt.Errorf(`internal error: in x/ repo loop for project %s, c.Ref != "refs/heads/master"`, c.Project))
						}
						return c.Id, true
					}
				}
				return "", false
			}()
			if !ok && b.GetStatus() == bbpb.Status_STARTED {
				// A started build that hasn't selected the x/ repo commit yet.
				// As an approximation, assume it'll pick the latest x/ repo head commit.
				repoCommit = repoHeads[builder.Repo]
			} else if !ok {
				// Repo commit not found in output properties, and it's not a started build.
				// As an example, this can happen if a build failed due to an infra failure
				// early on, before selecting the x/ repo commit. Skip such builds.
				continue
			}
			if repoCommit != repoHeads[builder.Repo] {
				// Skip builds that are not for the x/ repository's head commit.
				continue
			}
			if builds[builder.Repo] == nil {
				builds[builder.Repo] = make(map[string]map[string]Build)
			}
			if builds[builder.Repo][repoCommit] == nil {
				builds[builder.Repo][repoCommit] = make(map[string]Build)
			}
			builds[builder.Repo][repoCommit][b.GetBuilder().GetBuilder()] = Build{
				ID:          b.GetId(),
				BuilderName: b.GetBuilder().GetBuilder(),
				Status:      b.GetStatus(),
			}
			used++
		}
	}
	// Fetch builds for the single latest commit of each golang.org/x repo,
	// ones that were invoked from the x/ repository side.
	var goHeads = make(map[string]string) // A branch → head commit ID map.
	for _, r := range dashResp.Releases {
		goHeads[r.GetBranchName()] = r.GetBranchCommit()
	}
	for _, rh := range dashResp.RepoHeads {
		if rh.GerritProject == "go" {
			continue
		}
		if r, ok := repos.ByGerritProject[rh.GerritProject]; !ok || !r.ShowOnDashboard() {
			// Not a golang.org/x repository that's marked visible on the dashboard.
			// Skip it.
			continue
		}
		repo, commit := rh.GerritProject, rh.Commit.Commit
		buildList, err := fetchBuildsForCommit(ctx, buildsCl, repo, commit, "id", "builder.builder", "status", "input.gitiles_commit", "output.properties")
		if err != nil {
			return nil, nil, err
		}
		total += len(buildList)
		for _, b := range buildList {
			switch b.GetStatus() {
			case bbpb.Status_STARTED, bbpb.Status_SUCCESS, bbpb.Status_FAILURE, bbpb.Status_INFRA_FAILURE:
			default:
				// Skip builds with other statuses at this time.
				// Such builds can be included when the callers deem it useful.
				continue
			}
			builder, ok := builders[b.GetBuilder().GetBuilder()]
			if !ok {
				// A build that isn't associated with a current builder we're tracking.
				// It might've been removed, or has a known issue. Skip this build too.
				continue
			}
			var buildOutputProps struct {
				Sources []struct {
					GitilesCommit struct {
						Project string `json:"project"`
						Ref     string `json:"ref"`
						Id      string `json:"id"`
					} `json:"gitiles_commit"`
				} `json:"sources"`
			}
			if data, err := b.GetOutput().GetProperties().MarshalJSON(); err != nil {
				return nil, nil, fmt.Errorf("marshaling build output properties to JSON failed: %v", err)
			} else if err := json.Unmarshal(data, &buildOutputProps); err != nil {
				return nil, nil, err
			}
			goCommit, ok := func() (string, bool) {
				for _, s := range buildOutputProps.Sources {
					if c := s.GitilesCommit; c.Project == "go" {
						if c.Ref != "refs/heads/"+builder.GoBranch {
							panic(fmt.Errorf(`internal error: in Go repo loop, c.Ref != "refs/heads/%s"`, builder.GoBranch))
						}
						return c.Id, true
					}
				}
				return "", false
			}()
			if !ok && b.GetStatus() == bbpb.Status_STARTED {
				// A started build that hasn't selected the Go repo commit yet.
				// As an approximation, assume it'll pick the latest Go repo head commit.
				goCommit = goHeads[builder.GoBranch]
			} else if !ok {
				// Repo commit not found in output properties, and it's not a started build.
				// As an example, this can happen if a build failed due to an infra failure
				// early on, before selecting the Go repo commit. Skip such builds.
				continue
			}
			if goCommit != goHeads[builder.GoBranch] {
				// Skip builds that are not for the Go repository's head commit.
				continue
			}
			c := b.GetInput().GetGitilesCommit()
			if c.Project != builder.Repo {
				// When fetching builds for commits in x/ repos, it's expected
				// that build repo will always match builder repo. This isn't
				// true for the main Go repo because it triggers builds for x/
				// repos. But x/ repo builds don't trigger builds elsewhere.
				return nil, nil, fmt.Errorf("internal error: build repo %q doesn't match builder repo %q", c.Project, builder.Repo)
			}
			if builds[builder.Repo] == nil {
				builds[builder.Repo] = make(map[string]map[string]Build)
			}
			if builds[builder.Repo][c.Id] == nil {
				builds[builder.Repo][c.Id] = make(map[string]Build)
			}
			builds[builder.Repo][c.Id][b.GetBuilder().GetBuilder()] = Build{
				ID:          b.GetId(),
				BuilderName: b.GetBuilder().GetBuilder(),
				Status:      b.GetStatus(),
			}
			used++
		}
	}
	log.Printf("lucipoll.runOnce: aggregate GetBuildsForCommit calls fetched %d builds (and used %d of them) in %v\n", total, used, time.Since(t0))

	return builders, builds, nil
}

// listBuilders lists post-submit LUCI builders.
func listBuilders(ctx context.Context, buildersCl bbpb.BuildersClient) (builders []Builder, _ error) {
	var pageToken string
nextPage:
	resp, err := buildersCl.ListBuilders(ctx, &bbpb.ListBuildersRequest{
		Project: "golang", Bucket: "ci",
		PageSize:  1000,
		PageToken: pageToken,
	})
	if err != nil {
		return nil, err
	}
	for _, b := range resp.GetBuilders() {
		var p BuilderConfigProperties
		if err := json.Unmarshal([]byte(b.GetConfig().GetProperties()), &p); err != nil {
			return nil, err
		}
		builders = append(builders, Builder{b.GetId().GetBuilder(), &p})
	}
	if resp.GetNextPageToken() != "" {
		pageToken = resp.GetNextPageToken()
		goto nextPage
	}
	slices.SortFunc(builders, func(a, b Builder) int {
		return strings.Compare(a.Name, b.Name)
	})
	return builders, nil
}

// fetchBuildsForCommit fetches builds from all post-submit LUCI builders for a specific commit.
func fetchBuildsForCommit(ctx context.Context, buildsCl bbpb.BuildsClient, repo, commit string, maskPaths ...string) (builds []*bbpb.Build, _ error) {
	mask, err := fieldmaskpb.New((*bbpb.Build)(nil), maskPaths...)
	if err != nil {
		return nil, err
	}
	var pageToken string
nextPage:
	resp, err := buildsCl.SearchBuilds(ctx, &bbpb.SearchBuildsRequest{
		Predicate: &bbpb.BuildPredicate{
			Builder: &bbpb.BuilderID{Project: "golang", Bucket: "ci"},
			Tags: []*bbpb.StringPair{
				{Key: "buildset", Value: fmt.Sprintf("commit/gitiles/go.googlesource.com/%s/+/%s", repo, commit)},
			},
		},
		Mask:      &bbpb.BuildMask{Fields: mask},
		PageSize:  1000,
		PageToken: pageToken,
	})
	if err != nil {
		return nil, err
	}
	builds = append(builds, resp.GetBuilds()...)
	if resp.GetNextPageToken() != "" {
		pageToken = resp.GetNextPageToken()
		goto nextPage
	}
	return builds, nil
}
