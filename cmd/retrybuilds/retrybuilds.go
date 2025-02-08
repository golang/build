// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The retrybuilds command reruns requested builds for the Go project on
// the LUCI infrastructure.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.chromium.org/luci/auth"
	bbpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/common/api/gitiles"
	gitilespb "go.chromium.org/luci/common/proto/gitiles"
	"go.chromium.org/luci/grpc/prpc"
	"go.chromium.org/luci/hardcoded/chromeinfra"
	sauth "go.chromium.org/luci/server/auth"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	dryRun        = flag.Bool("dry-run", true, "just report what would've been done, without changing anything")
	maxConcurrent = flag.Int("max-concurrent-builds", 1, "the maximum number of concurrent outstanding builds")
	commitPattern = flag.String("commits", "", "comma-separated list of repositories and commit ranges (e.g. go:135e51b..512a125,tools:eeeabcc)")
	builder       = flag.String("builder", "", "builder name which is assumed to be in the golang/ci bucket (full builder names (project/bucket/builder) are also accepted)")
	buildID       = flag.Uint64("build", 0, "buildbucket build ID of the one build to retry")
	predicateJSON = flag.String("predicate", "", "BuildPredicate proto in JSON format")
	login         = flag.Bool("login", false, "include interactive login to LUCI")
)

const helpText = `retrybuilds: A tool for retrying Go project builds en masse.

Accepts several options which it uses to search for relevant builds and run them again.
Common requests like -builder are available directly as options, but for anything not
appearing in a flag the -predicate flag can be used to pass a BuildPredicate proto in JSON
format.

By default, this tool runs in a dry-run mode to avoid wasting resources.
To retry builds for real, set -dry-run=false.

Once invoking this tool, leave it running. If a large number of builds are
requested for retry, the tool will wait for existing builds to finish before
triggering new ones. The purpose behind this is to avoid the scheduling timeout
for low-resource builders which may fire if all builds are simply scheduled at
once. To increase the maximum number of concurrent builds, use the
-max-concurrent-builds flag.
`

func init() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), helpText+"\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options]\n", os.Args[0])
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if *dryRun {
		log.Printf("*** DRY RUN MODE ***")
	}

	// Parse all the flags.
	if *maxConcurrent <= 0 {
		flag.Usage()
		return fmt.Errorf("maximum concurrent builds must be at least 1")
	}
	var commitProgs []*commitProgram
	if *commitPattern != "" {
		for _, s := range strings.Split(*commitPattern, ",") {
			cp, err := parseCommitProgram(s)
			if err != nil {
				flag.Usage()
				return err
			}
			commitProgs = append(commitProgs, cp)
		}
	}

	// Parse the predicate.
	predicate := new(bbpb.BuildPredicate)
	if *predicateJSON != "" {
		if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(*predicateJSON), predicate); err != nil {
			return fmt.Errorf("invalid build predicate JSON: %v", err)
		}
	}

	// Parse the builder name and override it in the predicate.
	if *builder != "" {
		b, err := parseBuilderName(*builder)
		if err != nil {
			return err
		}
		predicate.Builder = b
	}

	// Parse the build ID and put it in the predicate.
	if *buildID != 0 {
		predicate.Build = &bbpb.BuildRange{StartBuildId: int64(*buildID), EndBuildId: int64(*buildID)}
	}

	// Fetch any commits that were requested of us.
	ctx := context.Background()
	auth := createLUCIAuthenticator(ctx)
	if *login {
		if err := auth.Login(); err != nil {
			return err
		}
	}
	hc, err := auth.Client()
	if err != nil {
		return fmt.Errorf("creating HTTP client: %w", err)
	}
	var commits []gitCommit
	for _, prog := range commitProgs {
		if prog.commitLo != prog.commitHi {
			log.Printf("fetching commits: %s/%s: %s..%s", publicGoHost, prog.repo, prog.commitHi, prog.commitLo)
		} else {
			log.Printf("fetching commit: %s/%s: %s", publicGoHost, prog.repo, prog.commitHi)
		}
		c, err := prog.run(ctx, hc)
		if err != nil {
			return err
		}
		commits = append(commits, c...)
	}
	bc := bbpb.NewBuildsPRPCClient(&prpc.Client{
		C:       hc,
		Host:    chromeinfra.BuildbucketHost,
		Options: prpc.DefaultOptions(),
	})
	var builds []*bbpb.Build
	if len(commits) == 0 {
		log.Printf("searching for builds with provided predicate")

		// Search for builds using the predicate as-is.
		resp, err := bc.SearchBuilds(ctx, &bbpb.SearchBuildsRequest{Predicate: predicate})
		if err != nil {
			return fmt.Errorf("searching builds: %v", err)
		}
		builds = resp.Builds
	} else {
		// Override GerritChanges and remove any buildset tags from the predicate.
		predicate.GerritChanges = nil
		predicate.Tags = slices.DeleteFunc(predicate.Tags, func(tag *bbpb.StringPair) bool {
			return tag.Key == "buildset"
		})

		// Perform one SearchBuilds request for each commit.
		for _, commit := range commits {
			log.Printf("searching for builds with provided predicate and tag 'buildset:%s'", commit.buildset())

			predicate.Tags = append(predicate.Tags, &bbpb.StringPair{Key: "buildset", Value: commit.buildset()})
			resp, err := bc.SearchBuilds(ctx, &bbpb.SearchBuildsRequest{Predicate: predicate, Mask: &bbpb.BuildMask{AllFields: true}})
			if err != nil {
				return fmt.Errorf("searching builds: %v", err)
			}
			builds = append(builds, resp.Builds...)
			predicate.Tags = predicate.Tags[:len(predicate.Tags)-1]
		}
	}

	// Set up the schedule build requests.
	var reqs []*bbpb.ScheduleBuildRequest
	for _, build := range builds {
		exps := make(map[string]bool)
		for _, exp := range build.Input.Experiments {
			exps[exp] = true
		}
		reqs = append(reqs, &bbpb.ScheduleBuildRequest{
			Builder:       build.Builder,
			Experiments:   exps,
			GitilesCommit: build.Input.GitilesCommit,
			GerritChanges: build.Input.GerritChanges,
			Tags:          build.Tags,
		})
	}

	// Launch (or pretend to launch) builds.
	eg, ctx := errgroup.WithContext(ctx)
	eg.SetLimit(*maxConcurrent)
	for i, req := range reqs {
		i := i
		req := req
		eg.Go(func() error {
			json, err := protojson.Marshal(req)
			if err != nil {
				return fmt.Errorf("failed to marshal JSON for build request: %v", err)
			}
			log.Printf("#%d: launching: %s\n", i+1, string(json))
			if *dryRun {
				return nil
			}
			build, err := bc.ScheduleBuild(ctx, req)
			if err != nil {
				return fmt.Errorf("scheduling build %d: %v", i+1, err)
			}
			id := build.Id
			log.Printf("#%d: launched: https://ci.chromium.org/b/%d\n", i+1, id)
			started := false
			for {
				time.Sleep(10 * time.Second)
				build, err := bc.GetBuild(ctx, &bbpb.GetBuildRequest{Id: id})
				if err != nil {
					return fmt.Errorf("polling build %d: %v", i+1, err)
				}
				if build.Status&bbpb.Status_ENDED_MASK != 0 {
					log.Printf("#%d: finished: %s\n", i+1, build.Status)
					break
				}
				if !started && build.Status == bbpb.Status_STARTED {
					log.Printf("#%d: started\n", i+1)
				}
			}
			return nil
		})
	}
	return eg.Wait()
}

func parseBuilderName(builderName string) (*bbpb.BuilderID, error) {
	if strings.IndexByte(builderName, '/') < 0 {
		return &bbpb.BuilderID{
			Project: "go",
			Bucket:  "ci",
			Builder: builderName,
		}, nil
	}
	c := strings.Split(builderName, "/")
	if len(c) != 3 {
		return nil, fmt.Errorf("expected 3 components for full builder name: project / bucket / builder")
	}
	return &bbpb.BuilderID{
		Project: c[0],
		Bucket:  c[1],
		Builder: c[2],
	}, nil
}

type gitCommit struct {
	host string
	repo string
	hash string
}

func (c gitCommit) buildset() string {
	return fmt.Sprintf("commit/gitiles/%s/%s/+/%s", c.host, c.repo, c.hash)
}

type commitProgram struct {
	repo               string
	commitLo, commitHi string // inclusive range
}

func parseCommitProgram(s string) (*commitProgram, error) {
	c := strings.SplitN(s, ":", 2)
	if len(c) != 2 {
		return nil, fmt.Errorf("invalid commits format: expected ':' to separate repo from commit in %q", s)
	}
	cp := new(commitProgram)
	cp.repo = c[0]
	i := strings.Index(c[1], "..")
	if i < 0 {
		cp.commitHi, cp.commitLo = c[1], c[1]
	} else {
		cp.commitHi, cp.commitLo = c[1][:i], c[1][i+2:]
	}
	if !commitRegexp.MatchString(cp.commitLo) {
		return nil, fmt.Errorf("%s is not a valid commit", cp.commitLo)
	}
	if !commitRegexp.MatchString(cp.commitHi) {
		return nil, fmt.Errorf("%s is not a valid commit", cp.commitHi)
	}
	return cp, nil
}

const publicGoHost = "go.googlesource.com"

func (cp *commitProgram) run(ctx context.Context, hc *http.Client) ([]gitCommit, error) {
	gc, err := gitiles.NewRESTClient(hc, publicGoHost, true)
	if err != nil {
		return nil, fmt.Errorf("gitiles.NewRESTClient: %w", err)
	}
	var commits []gitCommit
	var pageToken string
	for {
		log, err := gc.Log(ctx, &gitilespb.LogRequest{
			Project:    cp.repo,
			Committish: cp.commitHi,
			PageSize:   20,
			PageToken:  pageToken,
		})
		if err != nil {
			return nil, fmt.Errorf("fetching commits: %w", err)
		}
		if len(commits) == 0 {
			if len(log.Log) == 0 {
				return nil, fmt.Errorf("no commits found for project %s at commit %s", cp.repo, cp.commitHi)
			}
			if log.Log[0].Id != cp.commitHi {
				return nil, fmt.Errorf("internal error: first commit does not match %s: got %s", cp.commitHi, log.Log[0].Id)
			}
		} else if len(log.Log) == 0 {
			return nil, fmt.Errorf("project %s does not contain commit %s", cp.repo, cp.commitLo)
		}
		for _, commit := range log.Log {
			commits = append(commits, gitCommit{host: publicGoHost, repo: cp.repo, hash: commit.Id})
			if strings.HasPrefix(commit.Id, cp.commitLo) {
				return commits, nil
			}
			if len(commits) > 100 {
				return nil, fmt.Errorf("either commit %s does not exist in %s or commit range is too large (>100)", cp.commitLo, cp.repo)
			}
		}
		pageToken = log.NextPageToken
	}
}

var commitRegexp = regexp.MustCompile(`[0-9a-f]{7,40}`)

func createLUCIAuthenticator(ctx context.Context) *auth.Authenticator {
	authOpts := chromeinfra.SetDefaultAuthOptions(auth.Options{
		Scopes: append([]string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/gerritcodereview",
		}, sauth.CloudOAuthScopes...),
	})
	return auth.NewAuthenticator(ctx, auth.SilentLogin, authOpts)
}
