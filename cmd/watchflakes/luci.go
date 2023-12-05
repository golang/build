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
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	bbpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/common/api/gitiles"
	gpb "go.chromium.org/luci/common/proto/gitiles"
	"go.chromium.org/luci/grpc/prpc"
	rdbpb "go.chromium.org/luci/resultdb/proto/v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var nProc = runtime.GOMAXPROCS(0) * 4

const resultDBHost = "results.api.cr.dev"
const crBuildBucketHost = "cr-buildbucket.appspot.com"
const gitilesHost = "go.googlesource.com"

type LUCIClient struct {
	HTTPClient     *http.Client
	GitilesClient  gpb.GitilesClient
	BuildsClient   bbpb.BuildsClient
	BuildersClient bbpb.BuildersClient
	ResultDBClient rdbpb.ResultDBClient
}

func NewLUCIClient() *LUCIClient {
	c := new(http.Client)
	gitilesClient, err := gitiles.NewRESTClient(c, gitilesHost, false)
	if err != nil {
		log.Fatal(err)
	}
	buildsClient := bbpb.NewBuildsPRPCClient(&prpc.Client{
		C:    c,
		Host: crBuildBucketHost,
	})
	buildersClient := bbpb.NewBuildersPRPCClient(&prpc.Client{
		C:    c,
		Host: crBuildBucketHost,
	})
	resultDBClient := rdbpb.NewResultDBPRPCClient(&prpc.Client{
		C:    c,
		Host: resultDBHost,
	})
	return &LUCIClient{
		HTTPClient:     c,
		GitilesClient:  gitilesClient,
		BuildsClient:   buildsClient,
		BuildersClient: buildersClient,
		ResultDBClient: resultDBClient,
	}
}

type BuilderConfigProperties struct {
	Repo     string `json:"project, omitempty"`
	GoBranch string `json:"go_branch, omitempty"`
	Target   struct {
		GOARCH string `json:"goarch, omitempty"`
		GOOS   string `json:"goos, omitempty"`
	} `json:"target"`
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

// Get the list of commits
func (c *LUCIClient) ListCommits(ctx context.Context, repo, gobranch string, since time.Time) []Commit {
	log.Println("ListCommits", repo, gobranch)
	branch := "master"
	if repo == "go" {
		branch = gobranch
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

// Get the list of builders, on the given repo and gobranch.
// If repo and gobranch are empty, list all builders.
func (c *LUCIClient) ListBuilders(ctx context.Context, repo, gobranch string) []Builder {
	log.Println("ListBuilders", repo, gobranch)
	all := repo == "" && gobranch == ""
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
		log.Fatal(err)
	}
	for _, b := range resp.GetBuilders() {
		var p BuilderConfigProperties
		json.Unmarshal([]byte(b.GetConfig().GetProperties()), &p)
		if all || (p.Repo == repo && p.GoBranch == gobranch) {
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
	return builders
}

func (c *LUCIClient) ListBoards(ctx context.Context) []*Dashboard {
	builders := c.ListBuilders(ctx, "", "")
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
	return boards
}

// Get builds from one builder.
func (c *LUCIClient) GetBuilds(ctx context.Context, builder string, since time.Time) []*bbpb.Build {
	log.Println("GetBuilds", builder)
	pred := &bbpb.BuildPredicate{
		Builder:    &bbpb.BuilderID{Project: "golang", Bucket: "ci", Builder: builder},
		CreateTime: &bbpb.TimeRange{StartTime: timestamppb.New(since)},
	}
	mask, err := fieldmaskpb.New((*bbpb.Build)(nil), "id", "builder", "output", "status", "steps", "infra", "end_time")
	if err != nil {
		log.Fatal(err)
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
		log.Fatal(err)
	}
	builds = append(builds, resp.GetBuilds()...)
	if resp.GetNextPageToken() != "" {
		pageToken = resp.GetNextPageToken()
		goto nextPage
	}
	return builds
}

// Read the build dashboard dash, fill in the content.
func (c *LUCIClient) ReadBoard(ctx context.Context, dash *Dashboard, since time.Time) {
	log.Println("ReadBoard", dash.Repo, dash.GoBranch)
	dash.Commits = c.ListCommits(ctx, dash.Repo, dash.GoBranch, since)
	dash.Builders = c.ListBuilders(ctx, dash.Repo, dash.GoBranch)

	dashMap := make([]map[string]*BuildResult, len(dash.Builders)) // indexed by builder, then keyed by commit hash

	// Get builds from builders.
	var wg sync.WaitGroup
	sem := make(chan int, nProc)
	for i, builder := range dash.Builders {
		buildMap := make(map[string]*BuildResult)
		dashMap[i] = buildMap
		wg.Add(1)
		sem <- 1
		go func(builder Builder) {
			defer func() { wg.Done(); <-sem }()
			bName := builder.Name
			builds := c.GetBuilds(ctx, bName, since)

			for _, b := range builds {
				id := b.GetId()
				var commit, goCommit string
				prop := b.GetOutput().GetProperties().GetFields()
				for _, s := range prop["sources"].GetListValue().GetValues() {
					x := s.GetStructValue().GetFields()["gitilesCommit"].GetStructValue().GetFields()
					c := x["id"].GetStringValue()
					switch repo := x["project"].GetStringValue(); repo {
					case dash.Repo:
						commit = c
					case "go":
						goCommit = c
					default:
						log.Fatalf("repo mismatch: %s %s %s", repo, dash.Repo, buildURL(id))
					}
				}
				if commit == "" {
					switch b.GetStatus() {
					case bbpb.Status_SUCCESS, bbpb.Status_FAILURE:
						log.Fatalf("empty commit: %s", buildURL(id))
					default:
						// unfinished build, or infra failure, ignore
						continue
					}
				}
				buildTime := b.GetEndTime().AsTime()
				if r0 := buildMap[commit]; r0 != nil {
					// A build already exists for the same builder and commit.
					// Maybe manually retried, or different go commits on same subrepo commit.
					// Pick the one ended at later time.
					const printDup = false
					if printDup {
						fmt.Printf("skip duplicate build: %s %s %d %d\n", bName, shortHash(commit), id, r0.ID)
					}
					if buildTime.Before(r0.BuildTime) {
						continue
					}
				}
				rdb := b.GetInfra().GetResultdb()
				if rdb.GetHostname() != resultDBHost {
					log.Fatalf("ResultDB host mismatch: %s %s %s", rdb.GetHostname(), resultDBHost, buildURL(id))
				}
				if b.GetBuilder().GetBuilder() != bName { // sanity check
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
				buildMap[commit] = r
			}
		}(builder)
	}
	wg.Wait()

	// Gather into dashboard
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
}

func (c *LUCIClient) ReadBoards(ctx context.Context, boards []*Dashboard, since time.Time) {
	for _, dash := range boards {
		c.ReadBoard(ctx, dash, since)
	}
}

// For a failed run, get the failed tests and artifacts.
func (c *LUCIClient) GetResultAndArtifacts(ctx context.Context, r *BuildResult) []*Failure {
	log.Println("GetResultAndArtifacts", r.Builder, shortHash(r.Commit), r.ID)
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
	return "", testid
}

func buildURL(buildid int64) string { // keep in sync with buildUrlRE in github.go
	return fmt.Sprintf("https://ci.chromium.org/b/%d", buildid)
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
// failures
func (c *LUCIClient) FindFailures(ctx context.Context, boards []*Dashboard) []*BuildResult {
	var res []*BuildResult
	var wg sync.WaitGroup
	sem := make(chan int, nProc)
	for _, dash := range boards {
		for i, b := range dash.Builders {
			for _, r := range dash.Results[i] {
				if r == nil {
					continue
				}
				if r.Builder != b.Name { // sanity check
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

// Print the dashboard. For each builder, print a list of commits and status.
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

func fetchURL(url string) string {
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body)
}

func fetchLogsForBuild(r *BuildResult) {
	log.Println("fetchLogs", r.Builder, shortHash(r.Commit), r.ID)
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

func fetchLogs(res []*BuildResult) {
	// TODO: caching?
	var wg sync.WaitGroup
	sem := make(chan int, nProc)
	for _, r := range res {
		wg.Add(1)
		sem <- 1
		go func(r *BuildResult) {
			defer func() { wg.Done(); <-sem }()
			fetchLogsForBuild(r)
		}(r)
	}
	wg.Wait()
}
