// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
The resultdbpprof command collects the test results from a LUCI
build and assembles them into a pprof profile for analysis.

It assumes the test results have Go test names, along the lines
of `pkg.TestX/Y/Z` and uses that to construct the list of locations
for each pprof sample.
More specifically, `pkg.TestX/Y/Z` will get broken down into the
pprof sample `[pkg, TestX, Y, Z]`, attaching the duration of each
subtest to each sample.

Note that this means this tool will not work with LUCI builds that
run non-Go tests.

The profile that is produced by this tool is a little strange in
that it is quite likely to have *many* unique location lists, so
the pprof tool may struggle or just give up on rendering all of them.
Rest assured that the data is all in there, however.

So, we recommend that when using a flame graph viewer for pprof
profiles, the user of the profiles produced by this tool pivots and
searches for specific packages before assuming that they're not
present in the profile.
*/
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/pprof/profile"
	"go.chromium.org/luci/auth"
	bbpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/grpc/prpc"
	"go.chromium.org/luci/hardcoded/chromeinfra"
	rdbpb "go.chromium.org/luci/resultdb/proto/v1"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

var (
	verbose = flag.Bool("v", false, "print extra debug information")
	public  = flag.Bool("public", true, "whether the build is public or not")
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] <build ID>\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Downloads test results for a LUCI build and generates a pprof\n")
		fmt.Fprintf(flag.CommandLine.Output(), "profile of their execution times. Useful for understanding test\n")
		fmt.Fprintf(flag.CommandLine.Output(), "execution times and identifying low hanging fruit to speed up CI.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Results are written to '<build ID>.prof' in the current working\n")
		fmt.Fprintf(flag.CommandLine.Output(), "directory.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\n")
		fmt.Fprintf(flag.CommandLine.Output(), "This tool expects Go test names of the form 'pkg.TestX/Y/Z'.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Test names not matching this pattern may appear in the output in\n")
		fmt.Fprintf(flag.CommandLine.Output(), "an unexpected form.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\n")
		flag.PrintDefaults()
	}
}

func main() {
	// Validate flags.
	flag.Parse()

	// Parse build ID.
	if flag.NArg() != 1 {
		fmt.Fprintln(flag.CommandLine.Output(), "expected one argument: a LUCI build ID")
		flag.Usage()
		os.Exit(1)
	}
	idArg := flag.Arg(0)
	idArg, _ = strings.CutPrefix(idArg, "b") // Allow optional 'b' prefix for easier copy-pasting.
	buildID, err := strconv.ParseInt(idArg, 10, 64)
	if err != nil {
		log.Fatalf("parsing build ID %s: %v", flag.Arg(0), err)
	}

	// Create client.
	ctx := context.Background()
	var hc *http.Client
	if !*public {
		authOpts := chromeinfra.SetDefaultAuthOptions(auth.Options{})
		au := auth.NewAuthenticator(ctx, auth.SilentLogin, authOpts)
		if err := au.CheckLoginRequired(); errors.Is(err, auth.ErrLoginRequired) {
			log.Println("LUCI login required... initiating interactive login process")
			if err := au.Login(); err != nil {
				log.Fatalf("interactive login failed: %v", err)
			}
		} else if err != nil {
			log.Fatalf("checking whether LUCI login is required: %v", err)
		}
		hc, err = au.Client()
		if err != nil {
			log.Fatalf("creating HTTP client: %v", err)
		}
	}
	lc := NewLUCIClient(hc)

	// Fetch test data.
	if !*verbose {
		log.Print("fetching test timings")
	}
	records, err := fetchTestTimingsForBuild(ctx, lc, buildID)
	if err != nil {
		log.Fatalf("failed to fetch test timings for build %d: %v", buildID, err)
	}

	// Generate a profile.
	prof := makeProfile(records)

	// Write out the file.
	fname := fmt.Sprintf("%d.prof", buildID)
	log.Print("saving profile to ", fname)
	f, err := os.Create(fname)
	if err != nil {
		log.Fatalf("failed to create output file %s: %v", fname, err)
	}
	defer f.Close()
	if err := prof.Write(f); err != nil {
		log.Fatalf("failed to write to output file %s: %v", fname, err)
	}
}

// LUCIClient is a LUCI client.
type LUCIClient struct {
	Builds   bbpb.BuildsClient
	ResultDB rdbpb.ResultDBClient
}

// NewLUCIClient creates a LUCI client.
//
// If c is nil, an unauthenticated http.DefaultClient is used,
// otherwise c is expected to be an authenticated HTTP client.
func NewLUCIClient(c *http.Client) *LUCIClient {
	return &LUCIClient{
		Builds: bbpb.NewBuildsClient(&prpc.Client{
			C:    c,
			Host: chromeinfra.BuildbucketHost,
		}),
		ResultDB: rdbpb.NewResultDBClient(&prpc.Client{
			C:    c,
			Host: chromeinfra.ResultDBHost,
		}),
	}
}

func fetchTestTimingsForBuild(ctx context.Context, c *LUCIClient, buildID int64) ([]record, error) {
	// Fetch the build.
	buildMask, err := fieldmaskpb.New((*bbpb.Build)(nil), "id", "infra")
	if err != nil {
		return nil, fmt.Errorf("error creating a build mask: %v", err)
	}
	b, err := c.Builds.GetBuild(ctx, &bbpb.GetBuildRequest{
		Id:     buildID,
		Fields: buildMask,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching build %d: %v", buildID, err)
	}
	// Grab the ResultDB invocation.
	inv := b.GetInfra().GetResultdb().GetInvocation()

	// Grab the total number of test results just to make the progress bar a little nicer.
	resp, err := c.ResultDB.QueryTestResultStatistics(ctx, &rdbpb.QueryTestResultStatisticsRequest{Invocations: []string{inv}})
	if err != nil {
		return nil, fmt.Errorf("failed to collect invocation statistics for build %d: %v", buildID, err)
	}
	total := resp.TotalTestResults

	// Fetch all the package test timings.
	if *verbose {
		log.Printf("fetching test results for build %d (https://ci.chromium.org/b/%d)", buildID, buildID)
	}
	testMask, err := fieldmaskpb.New((*rdbpb.TestResult)(nil), "test_id", "duration")
	if err != nil {
		return nil, fmt.Errorf("error creating a build mask: %v", err)
	}
	var recs []record
	ch := make(chan *rdbpb.QueryTestResultsResponse)
	var eg errgroup.Group
	eg.Go(func() error {
		defer func() {
			close(ch)
		}()
		var pageToken string
		for page := 1; ; page++ {
			resp, err := c.ResultDB.QueryTestResults(ctx, &rdbpb.QueryTestResultsRequest{
				Invocations: []string{inv},
				PageSize:    1000,
				PageToken:   pageToken,
				ReadMask:    testMask,
			})
			if err != nil {
				return fmt.Errorf("fetching page %d of test results for build %d: %v", page, b.Id, err)
			}
			ch <- resp
			if resp.NextPageToken == "" {
				break
			}
			pageToken = resp.NextPageToken
		}
		return nil
	})
	processed := 0
	for resp := range ch {
		processed += len(resp.TestResults)
		for _, r := range resp.TestResults {
			recs = append(recs, makeRecord(r.TestId, r.Duration.AsDuration()))
		}
		if *verbose {
			log.Printf("processed %d / %d (%.2f%%)", processed, total, float64(processed)/float64(total)*100)
		}
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return recs, nil
}

func makeRecord(testID string, duration time.Duration) record {
	pkgIdx := strings.Index(testID, ".Test")
	if pkgIdx < 0 {
		pkgIdx = strings.Index(testID, ".Benchmark")
		if pkgIdx < 0 {
			// Package-level test result.
			return record{
				subtests: []string{testID},
				duration: duration,
			}
		}
	}
	pkg := testID[:pkgIdx]
	subtests := strings.Split(testID[pkgIdx+1:], "/")
	slices.Reverse(subtests)
	subtests = append(subtests, pkg)
	return record{
		subtests: subtests,
		duration: duration,
	}
}

type record struct {
	subtests []string
	duration time.Duration
}

func makeProfile(prof []record) *profile.Profile {
	p := &profile.Profile{
		PeriodType: &profile.ValueType{Type: "luci", Unit: "count"},
		Period:     1,
		SampleType: []*profile.ValueType{
			{Type: "time", Unit: "nanoseconds"},
		},
	}
	funcs := make(map[string]*profile.Function)
	locs := make(map[string]*profile.Location)
	for _, rec := range prof {
		var sloc []*profile.Location
		for _, test := range rec.subtests {
			fn := funcs[test]
			loc := locs[test]
			if fn == nil {
				fn = &profile.Function{
					ID:         uint64(len(p.Function) + 1),
					Name:       test,
					SystemName: test,
				}
				p.Function = append(p.Function, fn)
				loc = &profile.Location{
					ID:      fn.ID,
					Address: fn.ID,
					Line: []profile.Line{
						{
							Function: fn,
						},
					},
				}
				p.Location = append(p.Location, loc)
				funcs[test] = fn
				locs[test] = loc
			}
			sloc = append(sloc, loc)
		}
		p.Sample = append(p.Sample, &profile.Sample{
			Value:    []int64{int64(rec.duration)},
			Location: sloc,
		})
	}
	return p
}
