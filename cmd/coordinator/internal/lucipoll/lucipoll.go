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
	"go.chromium.org/luci/grpc/prpc"
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
	Repo     string `json:"project,omitempty"`
	GoBranch string `json:"go_branch,omitempty"`
	Target   struct {
		GOOS   string `json:"goos,omitempty"`
		GOARCH string `json:"goarch,omitempty"`
	} `json:"target"`
	KnownIssue int `json:"known_issue,omitempty"`
}

type Build struct {
	ID          int64
	BuilderName string
	Status      bbpb.Status
}

func NewService(maintCl maintnerClient) *service {
	const crBuildBucketHost = "cr-buildbucket.appspot.com"

	s := &service{
		maintCl:    maintCl,
		buildersCl: bbpb.NewBuildersPRPCClient(&prpc.Client{Host: crBuildBucketHost}),
		buildsCl:   bbpb.NewBuildsPRPCClient(&prpc.Client{Host: crBuildBucketHost}),
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
	ticker := time.NewTicker(2 * time.Minute)
	for {
		builders, builds, err := runOnce(context.Background(), s.maintCl, s.buildersCl, s.buildsCl)
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
		buildList, err := fetchBuildsForCommit(ctx, buildsCl, repo, commit, "id", "builder.builder", "status", "input.gitiles_commit", "input.properties")
		if err != nil {
			return nil, nil, err
		}
		total += len(buildList)
		for _, b := range buildList {
			builder, ok := builders[b.GetBuilder().GetBuilder()]
			if !ok {
				// A build that isn't associated with a current builder we're tracking.
				// It might've been removed, or has a known issue. Skip this build too.
				continue
			}
			c := b.GetInput().GetGitilesCommit()
			if c.Project != builder.Repo {
				// A build that was triggered from a different project than the builder is for.
				// If the build hasn't completed, the exact repo commit hasn't been chosen yet.
				// For now such builds are not represented in the simple model of this package,
				// so skip it.
				continue
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
	// Fetch builds for a single commit in each golang.org/x repo.
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
		buildList, err := fetchBuildsForCommit(ctx, buildsCl, repo, commit, "id", "builder.builder", "status", "input.gitiles_commit", "input.properties")
		if err != nil {
			return nil, nil, err
		}
		total += len(buildList)
		for _, b := range buildList {
			builder, ok := builders[b.GetBuilder().GetBuilder()]
			if !ok {
				// A build that isn't associated with a current builder we're tracking.
				// It might've been removed, or has a known issue. Skip this build too.
				continue
			}
			c := b.GetInput().GetGitilesCommit()
			if c.Project != builder.Repo {
				// When fetching builds for commits in x/ repos, it's expected
				// that build repo will always match builder repo. This isn't
				// true for the main Go repo because it triggers builds for x/
				// repos. But x/ repo builds don't trigger builds elsewhere.
				return nil, nil, fmt.Errorf("internal error: build repo %q doesn't match builder repo %q", b.GetInput().GetGitilesCommit().GetProject(), builder.Repo)
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
	fmt.Printf("runOnce: aggregate GetBuildsForCommit calls fetched %d builds in %v\n", total, time.Since(t0))
	fmt.Printf("runOnce: used %d of those %d builds\n", used, total)

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
