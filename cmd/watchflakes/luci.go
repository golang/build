// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	bbpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/common/api/gitiles"
	gpb "go.chromium.org/luci/common/proto/gitiles"
	"go.chromium.org/luci/grpc/prpc"
	rdbpb "go.chromium.org/luci/resultdb/proto/v1"
	spb "go.chromium.org/luci/swarming/proto/api_v2"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	crBuildBucketHost = "cr-buildbucket.appspot.com"
	gitilesHost       = "go.googlesource.com"
	resultDBHost      = "results.api.cr.dev"
	swarmingHost      = "chromium-swarm.appspot.com"
)

// LUCIClient is a LUCI client.
type LUCIClient struct {
	HTTPClient     *http.Client
	BotsClient     spb.BotsClient
	BuildersClient bbpb.BuildersClient
	BuildsClient   bbpb.BuildsClient
	GitilesClient  gpb.GitilesClient
	ResultDBClient rdbpb.ResultDBClient

	// TraceSteps controls whether to log each step name as it's executed.
	TraceSteps bool

	nProc int
}

// NewLUCIClient creates a LUCI client.
// nProc controls concurrency. NewLUCIClient panics if nProc is non-positive.
func NewLUCIClient(nProc int) *LUCIClient {
	if nProc < 1 {
		panic(fmt.Errorf("nProc is %d, want 1 or higher", nProc))
	}
	c := new(http.Client)
	gitilesClient, err := gitiles.NewRESTClient(c, gitilesHost, false)
	if err != nil {
		log.Fatal(err)
	}
	buildsClient := bbpb.NewBuildsClient(&prpc.Client{
		C:    c,
		Host: crBuildBucketHost,
	})
	buildersClient := bbpb.NewBuildersClient(&prpc.Client{
		C:    c,
		Host: crBuildBucketHost,
	})
	resultDBClient := rdbpb.NewResultDBClient(&prpc.Client{
		C:    c,
		Host: resultDBHost,
	})
	botsClient := spb.NewBotsClient(&prpc.Client{
		C:    c,
		Host: swarmingHost,
	})
	return &LUCIClient{
		HTTPClient:     c,
		BotsClient:     botsClient,
		GitilesClient:  gitilesClient,
		BuildsClient:   buildsClient,
		BuildersClient: buildersClient,
		ResultDBClient: resultDBClient,
		nProc:          nProc,
	}
}

type BuilderConfigProperties struct {
	Repo     string `json:"project,omitempty"`
	GoBranch string `json:"go_branch,omitempty"`
	Target   struct {
		GOARCH string `json:"goarch,omitempty"`
		GOOS   string `json:"goos,omitempty"`
	} `json:"target"`
	KnownIssue int `json:"known_issue,omitempty"`
}

type Builder struct {
	Name string
	*BuilderConfigProperties
}

type BuildResult struct {
	ID        int64
	Status    bbpb.Status
	Commit    string    // commit hash
	Time      time.Time // commit time
	GoCommit  string    // for subrepo build, go commit hash
	BuildTime time.Time // build end time
	Builder   string
	*BuilderConfigProperties
	InvocationID string // ResultDB invocation ID
	LogURL       string // textual log of the whole run
	LogText      string
	StepLogURL   string // textual log of the (last) failed step, if any
	StepLogText  string
	Failures     []*Failure
	Top          bool // whether this is a consistent failure at the top (tip)
}

type Commit struct {
	Hash string
	Time time.Time
}

type Project struct {
	Repo     string
	GoBranch string
}

type Dashboard struct {
	Project
	Builders []Builder
	Commits  []Commit
	Results  [][]*BuildResult // indexed by builder, then by commit
}

type Failure struct {
	TestID  string
	Status  rdbpb.TestStatus
	LogURL  string
	LogText string
}

type Bot struct {
	ID          string
	Dead        bool
	Quarantined bool
}

// ListCommits fetches the list of commits from Gerrit.
func (c *LUCIClient) ListCommits(ctx context.Context, repo, goBranch string, since time.Time) []Commit {
	if c.TraceSteps {
		log.Println("ListCommits", repo, goBranch)
	}
	branch := "master"
	if repo == "go" {
		branch = goBranch
	}
	var commits []Commit
	var pageToken string
nextPage:
	resp, err := c.GitilesClient.Log(ctx, &gpb.LogRequest{
		Project:    repo,
		Committish: "refs/heads/" + branch,
		PageSize:   1000,
		PageToken:  pageToken,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, c := range resp.GetLog() {
		commitTime := c.GetCommitter().GetTime().AsTime()
		if commitTime.Before(since) {
			goto done
		}
		commits = append(commits, Commit{
			Hash: c.GetId(),
			Time: commitTime,
		})
	}
	if resp.GetNextPageToken() != "" {
		pageToken = resp.GetNextPageToken()
		goto nextPage
	}
done:
	return commits
}

// ListBuilders fetches the list of builders, on the given repo and goBranch.
// If repo and goBranch are empty, it fetches all builders.
func (c *LUCIClient) ListBuilders(ctx context.Context, repo, goBranch string) ([]Builder, error) {
	if c.TraceSteps {
		log.Println("ListBuilders", repo, goBranch)
	}
	all := repo == "" && goBranch == ""
	var builders []Builder
	var pageToken string
nextPage:
	resp, err := c.BuildersClient.ListBuilders(ctx, &bbpb.ListBuildersRequest{
		Project:   "golang",
		Bucket:    "ci",
		PageSize:  1000,
		PageToken: pageToken,
	})
	if err != nil {
		return nil, err
	}
	for _, b := range resp.GetBuilders() {
		var p BuilderConfigProperties
		err := json.Unmarshal([]byte(b.GetConfig().GetProperties()), &p)
		if err != nil {
			return nil, err
		}
		if p.KnownIssue != 0 {
			continue
		}
		if all || (p.Repo == repo && p.GoBranch == goBranch) {
			builders = append(builders, Builder{b.GetId().GetBuilder(), &p})
		}
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

func (c *LUCIClient) ListBoards(ctx context.Context) ([]*Dashboard, error) {
	builders, err := c.ListBuilders(ctx, "", "")
	if err != nil {
		return nil, err
	}
	repoMap := make(map[Project]bool)
	for _, b := range builders {
		repoMap[Project{b.Repo, b.GoBranch}] = true
	}
	boards := make([]*Dashboard, 0, len(repoMap))
	for p := range repoMap {
		d := &Dashboard{Project: p}
		boards = append(boards, d)
	}
	slices.SortFunc(boards, func(d1, d2 *Dashboard) int {
		if d1.Repo != d2.Repo {
			// put main repo first
			if d1.Repo == "go" {
				return -1
			}
			if d2.Repo == "go" {
				return 1
			}
			return strings.Compare(d1.Repo, d2.Repo)
		}
		return strings.Compare(d1.GoBranch, d2.GoBranch)
	})
	return boards, nil
}

// GetBuilds fetches builds from one builder.
func (c *LUCIClient) GetBuilds(ctx context.Context, builder string, since time.Time) ([]*bbpb.Build, error) {
	if c.TraceSteps {
		log.Println("GetBuilds", builder)
	}
	pred := &bbpb.BuildPredicate{
		Builder:    &bbpb.BuilderID{Project: "golang", Bucket: "ci", Builder: builder},
		CreateTime: &bbpb.TimeRange{StartTime: timestamppb.New(since)},
	}
	mask, err := fieldmaskpb.New((*bbpb.Build)(nil), "id", "builder", "output", "status", "steps", "infra", "end_time")
	if err != nil {
		return nil, err
	}
	var builds []*bbpb.Build
	var pageToken string
nextPage:
	resp, err := c.BuildsClient.SearchBuilds(ctx, &bbpb.SearchBuildsRequest{
		Predicate: pred,
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

// ReadBoard reads the build dashboard dash, then fills in the content.
func (c *LUCIClient) ReadBoard(ctx context.Context, dash *Dashboard, since time.Time) error {
	if c.TraceSteps {
		log.Println("ReadBoard", dash.Repo, dash.GoBranch)
	}
	dash.Commits = c.ListCommits(ctx, dash.Repo, dash.GoBranch, since)
	var err error
	dash.Builders, err = c.ListBuilders(ctx, dash.Repo, dash.GoBranch)
	if err != nil {
		return err
	}

	dashMap := make([]map[string]*BuildResult, len(dash.Builders)) // indexed by builder, then keyed by commit hash

	// Get builds from builders.
	g, groupContext := errgroup.WithContext(ctx)
	g.SetLimit(c.nProc)
	for i, builder := range dash.Builders {
		builder := builder
		buildMap := make(map[string]*BuildResult)
		dashMap[i] = buildMap
		g.Go(func() error {
			bName := builder.Name
			builds, err := c.GetBuilds(groupContext, bName, since)
			if err != nil {
				return err
			}
			for _, b := range builds {
				r := c.GetBuildResult(groupContext, dash.Repo, builder, b)
				if r == nil {
					continue
				}
				if r0 := buildMap[r.Commit]; r0 != nil {
					// A build already exists for the same builder and commit.
					// Maybe manually retried, or different go commits on same subrepo commit.
					// Pick the one ended at later time.
					const printDup = false
					if printDup {
						fmt.Printf("skip duplicate build: %s %s %d %d\n", bName, shortHash(r.Commit), r.ID, r0.ID)
					}
					if r.BuildTime.Before(r0.BuildTime) {
						continue
					}
				}
				buildMap[r.Commit] = r
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Gather into dashboard.
	dash.Results = make([][]*BuildResult, len(dash.Builders))
	for i, m := range dashMap {
		dash.Results[i] = make([]*BuildResult, len(dash.Commits))
		for j, c := range dash.Commits {
			r := m[c.Hash]
			if r == nil {
				continue
			}
			r.Time = c.Time // fill in commit time
			dash.Results[i][j] = r
		}
	}

	return nil
}

// GetBuildResult gets the build result of a given build.
func (c *LUCIClient) GetBuildResult(ctx context.Context, repo string, builder Builder, b *bbpb.Build) *BuildResult {
	id := b.GetId()
	var commit, goCommit string
	prop := b.GetOutput().GetProperties().GetFields()
	for _, s := range prop["sources"].GetListValue().GetValues() {
		fm := s.GetStructValue().GetFields()
		gc := fm["gitilesCommit"]
		if gc == nil {
			gc = fm["gitiles_commit"]
		}
		x := gc.GetStructValue().GetFields()
		c := x["id"].GetStringValue()
		switch bRepo := x["project"].GetStringValue(); bRepo {
		case repo:
			commit = c
		case "go":
			goCommit = c
		default:
			// go.dev/issue/70091 pointed out that failing because of a missmatch between builder definition
			// and input version is suboptimal. Instead log the case and continue processing.
			log.Printf("repo mismatch: %s %s %s", bRepo, repo, buildURL(id))
			return nil
		}
	}
	if commit == "" {
		switch b.GetStatus() {
		case bbpb.Status_SUCCESS:
			log.Fatalf("empty commit: %s", buildURL(id))
		default:
			// unfinished build, or infra failure, ignore
			return nil
		}
	}
	buildTime := b.GetEndTime().AsTime()
	rdb := b.GetInfra().GetResultdb()
	if rdb.GetHostname() != resultDBHost {
		log.Fatalf("ResultDB host mismatch: %s %s %s", rdb.GetHostname(), resultDBHost, buildURL(id))
	}
	bName := builder.Name
	if b.GetBuilder().GetBuilder() != bName { // coherence check
		log.Fatalf("builder mismatch: %s %s %s", b.GetBuilder().GetBuilder(), bName, buildURL(id))
	}
	r := &BuildResult{
		ID:                      id,
		Status:                  b.GetStatus(),
		Commit:                  commit,
		GoCommit:                goCommit,
		BuildTime:               buildTime,
		Builder:                 bName,
		BuilderConfigProperties: builder.BuilderConfigProperties,
		InvocationID:            rdb.GetInvocation(),
	}
	if r.Status == bbpb.Status_FAILURE {
		links := prop["failure"].GetStructValue().GetFields()["links"].GetListValue().GetValues()
		for _, l := range links {
			m := l.GetStructValue().GetFields()
			if strings.Contains(m["name"].GetStringValue(), "(combined output)") {
				r.LogURL = m["url"].GetStringValue()
				break
			}
		}
		if r.LogURL == "" {
			// No log URL, Probably a build failure.
			// E.g. https://ci.chromium.org/ui/b/8759448820419452721
			// Use the build's stderr instead.
			for _, l := range b.GetOutput().GetLogs() {
				if l.GetName() == "stderr" {
					r.LogURL = l.GetViewUrl()
					break
				}
			}
		}

		// Fetch the stderr of the failed step.
		steps := b.GetSteps()
	stepLoop:
		for i := len(steps) - 1; i >= 0; i-- {
			s := steps[i]
			if s.GetStatus() == bbpb.Status_FAILURE {
				for _, l := range s.GetLogs() {
					if l.GetName() == "stderr" || l.GetName() == "output" {
						r.StepLogURL = l.GetViewUrl()
						break stepLoop
					}
				}
			}
		}
	}
	return r
}

// GetBuild gets the information (builder info and build result) of a single build,
// given build ID.
func (c *LUCIClient) GetBuild(ctx context.Context, id int64) (*BuildResult, error) {
	if c.TraceSteps {
		log.Println("GetBuild", id)
	}
	mask, err := fieldmaskpb.New((*bbpb.Build)(nil), "id", "builder", "output", "status", "steps", "infra", "end_time")
	if err != nil {
		return nil, err
	}
	b, err := c.BuildsClient.GetBuild(ctx, &bbpb.GetBuildRequest{
		Id:   id,
		Mask: &bbpb.BuildMask{Fields: mask},
	})
	if err != nil {
		return nil, err
	}
	bi, err := c.BuildersClient.GetBuilder(ctx, &bbpb.GetBuilderRequest{
		Id: &bbpb.BuilderID{
			Project: "golang",
			Bucket:  "ci",
			Builder: b.GetBuilder().GetBuilder(),
		},
	})
	if err != nil {
		return nil, err
	}
	var p BuilderConfigProperties
	err = json.Unmarshal([]byte(bi.GetConfig().GetProperties()), &p)
	if err != nil {
		return nil, err
	}
	builder := Builder{bi.GetId().GetBuilder(), &p}
	r := c.GetBuildResult(ctx, builder.Repo, builder, b)
	return r, nil
}

func (c *LUCIClient) ReadBoards(ctx context.Context, boards []*Dashboard, since time.Time) error {
	for _, dash := range boards {
		err := c.ReadBoard(ctx, dash, since)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetResultAndArtifacts fetches the failed tests and artifacts for the failed run r.
func (c *LUCIClient) GetResultAndArtifacts(ctx context.Context, r *BuildResult) []*Failure {
	if c.TraceSteps {
		log.Println("GetResultAndArtifacts", r.Builder, shortHash(r.Commit), r.ID)
	}
	req := &rdbpb.QueryTestResultsRequest{
		Invocations: []string{r.InvocationID},
		Predicate:   &rdbpb.TestResultPredicate{Expectancy: rdbpb.TestResultPredicate_VARIANTS_WITH_UNEXPECTED_RESULTS},
		PageSize:    1000,
		// TODO: paging? Not sure we want to handle more than 1000 failures in a run...
	}
	resp, err := c.ResultDBClient.QueryTestResults(ctx, req)
	if err != nil {
		log.Fatal(err)
	}

	var failures []*Failure
	for _, rr := range resp.GetTestResults() {
		testID := rr.GetTestId()
		resp, err := c.ResultDBClient.QueryArtifacts(ctx, &rdbpb.QueryArtifactsRequest{
			Invocations: []string{r.InvocationID},
			Predicate: &rdbpb.ArtifactPredicate{
				TestResultPredicate: &rdbpb.TestResultPredicate{
					TestIdRegexp: regexp.QuoteMeta(testID),
					Expectancy:   rdbpb.TestResultPredicate_VARIANTS_WITH_UNEXPECTED_RESULTS,
				},
			},
			PageSize: 1000,
		})
		if err != nil {
			log.Fatal(err)
		}
		for _, a := range resp.GetArtifacts() {
			if a.GetArtifactId() != "output" {
				continue
			}
			url := a.GetFetchUrl()
			f := &Failure{
				TestID: testID,
				Status: rr.GetStatus(),
				LogURL: url,
			}
			failures = append(failures, f)
		}
	}
	slices.SortFunc(failures, func(f1, f2 *Failure) int {
		return strings.Compare(f1.TestID, f2.TestID)
	})
	return failures
}

// split TestID to package and test name.
func splitTestID(testid string) (string, string) {
	// TestId is <package path>.<test name>.
	// Both package path and test name could contain "." and "/" (due to subtests).
	// So looking for "." or "/" are not reliable.
	// Tests are always start with ".Test" (or ".Example", ".Benchmark" (do we
	// run benchmarks?)). Looking for them instead.
	// TODO: handle test flavors (e.g. -cpu=1,2,4, -linkmode=internal, etc.)
	for _, sep := range []string{".Test", ".Example", ".Benchmark"} {
		pkg, test, ok := strings.Cut(testid, sep)
		if ok {
			return pkg, sep[1:] + test // add back "Test" prefix (without ".")
		}
	}
	// Maybe a package-level target (e.g. build failure).
	return testid, ""
}

func buildURL(buildID int64) string { // keep in sync with buildUrlRE in github.go
	return fmt.Sprintf("https://ci.chromium.org/b/%d", buildID)
}

func shortHash(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// FindFailures returns the failures listed in the dashboards.
// The result is sorted by commit date, then repo, then builder.
// Pupulate the failure contents (the .Failures fields) for the
// failures.
func (c *LUCIClient) FindFailures(ctx context.Context, boards []*Dashboard) []*BuildResult {
	var res []*BuildResult
	var wg sync.WaitGroup
	sem := make(chan int, c.nProc)
	for _, dash := range boards {
		for i, b := range dash.Builders {
			for _, r := range dash.Results[i] {
				if r == nil {
					continue
				}
				if r.Builder != b.Name { // coherence check
					log.Fatalf("builder mismatch: %s %s", b.Name, r.Builder)
				}

				if r.Status == bbpb.Status_FAILURE {
					wg.Add(1)
					sem <- 1
					go func(r *BuildResult) {
						defer func() { wg.Done(); <-sem }()
						r.Failures = c.GetResultAndArtifacts(ctx, r)
					}(r)
					res = append(res, r)
				}
			}
		}
	}
	wg.Wait()

	slices.SortFunc(res, func(a, b *BuildResult) int {
		if !a.Time.Equal(b.Time) {
			return a.Time.Compare(b.Time)
		}
		if a.Repo != b.Repo {
			return strings.Compare(a.Repo, b.Repo)
		}
		if a.Builder != b.Builder {
			return strings.Compare(a.Builder, b.Builder)
		}
		return strings.Compare(a.Commit, b.Commit)
	})

	return res
}

// PrintDashboard prints the dashboard.
// For each builder, it prints a list of commits and status.
func PrintDashboard(dash *Dashboard) {
	for i, b := range dash.Builders {
		fmt.Println(b.Name)
		for _, r := range dash.Results[i] {
			if r == nil {
				continue
			}
			fmt.Printf("\t%s %v %v\n", shortHash(r.Commit), r.Time, r.Status)
		}
	}
}

// FetchLogs fetches logs for build results.
func (c *LUCIClient) FetchLogs(res []*BuildResult) {
	// TODO: caching?
	g := new(errgroup.Group)
	g.SetLimit(c.nProc)
	for _, r := range res {
		r := r
		g.Go(func() error {
			c.fetchLogsForBuild(r)
			return nil
		})
	}
	g.Wait()
}

func (c *LUCIClient) fetchLogsForBuild(r *BuildResult) {
	if c.TraceSteps {
		log.Println("fetchLogsForBuild", r.Builder, shortHash(r.Commit), r.ID)
	}
	if r.LogURL == "" {
		fmt.Printf("no log url: %s\n", buildURL(r.ID))
	} else {
		r.LogText = fetchURL(r.LogURL + "?format=raw")
	}
	if r.StepLogURL != "" {
		r.StepLogText = fetchURL(r.StepLogURL + "?format=raw")
	}
	for _, f := range r.Failures {
		if f.LogURL == "" {
			fmt.Printf("no log url: %s %s\n", buildURL(r.ID), f.TestID)
		} else {
			f.LogText = fetchURL(f.LogURL)
		}
	}
}

// filter enables the creation of filter functions which can remove bots
// from the list of returned bots.
type filter func(bot *spb.BotInfo) bool

// filterOutDarwin filters out darwin machines which no longer exist.
func filterOutDarwin(bot *spb.BotInfo) bool {
	return strings.HasPrefix(bot.BotId, "darwin-")
}

// ListBrokenBots lists bots that are either dead or quarantined. This list is limited
// to the bots in the shared-workers pool since they are generally the contributor provided
// bots. Arbitrary filters can be applied to filter out builders.
func (c *LUCIClient) ListBrokenBots(ctx context.Context, filters ...filter) ([]Bot, error) {
	if c.TraceSteps {
		log.Println("ListBrokenBots")
	}
	checkFilters := func(bot *spb.BotInfo) bool {
		for _, f := range filters {
			if f(bot) {
				return true
			}
		}
		return false
	}
	var brokenBots []Bot
	var cursor string
nextCursor:
	resp, err := c.BotsClient.ListBots(ctx, &spb.BotsRequest{
		Limit:  1000,
		Cursor: cursor,
		Dimensions: []*spb.StringPair{
			&spb.StringPair{Key: "pool", Value: "luci.golang.shared-workers"},
		},
	})
	if err != nil {
		return nil, err
	}
	for _, bot := range resp.GetItems() {
		if checkFilters(bot) {
			continue
		}
		if bot.IsDead || bot.Quarantined {
			brokenBots = append(brokenBots, Bot{ID: bot.BotId, Dead: bot.IsDead, Quarantined: bot.Quarantined})
		}
	}
	if resp.GetCursor() != "" {
		cursor = resp.GetCursor()
		goto nextCursor
	}
	return brokenBots, nil
}

func fetchURL(url string) string {
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ""
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		log.Fatal(fmt.Errorf("GET %s: non-200 OK status code: %v body: %q", url, resp.Status, body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(fmt.Errorf("GET %s: failed to read body: %v body: %q", url, err, body))
	}
	return string(body)
}
