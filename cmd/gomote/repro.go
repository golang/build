// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"go.chromium.org/luci/auth"
	bbpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/grpc/prpc"
	"go.chromium.org/luci/hardcoded/chromeinfra"
	rdbpb "go.chromium.org/luci/resultdb/proto/v1"
	sauth "go.chromium.org/luci/server/auth"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func repro(args []string) error {
	if luciDisabled() {
		return fmt.Errorf("repro subcommand is only available for LUCI builds")
	}
	fs := flag.NewFlagSet("repro", flag.ContinueOnError)
	fs.Usage = func() {
		log := usageLogger
		log.Print("repro usage: gomote repro [repro-opts] <build ID>")
		log.Print()
		log.Print("If there's a valid group specified, new instances are")
		log.Print("automatically added to the group. If the group in")
		log.Print("$GOMOTE_GROUP doesn't exist, and there's no other group")
		log.Print("specified, it will be created and new instances will be")
		log.Print("added to that group.")
		fs.PrintDefaults()
		os.Exit(1)
	}
	var cfg createConfig
	fs.BoolVar(&cfg.printStatus, "status", true, "print regular status updates while waiting")
	fs.IntVar(&cfg.count, "count", 1, "number of instances to create")
	fs.StringVar(&cfg.newGroup, "new-group", "", "also create a new group and add the new instances to it")
	cfg.useGolangbuild = true

	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}
	// Parse as a uint even though we'll end up converting to int64 -- negative build IDs are not valid.
	buildID, err := strconv.ParseUint(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("parsing build ID: %v", err)
	}
	ctx := context.Background()
	au := createLUCIAuthenticator(ctx)
	if err := au.CheckLoginRequired(); errors.Is(err, auth.ErrLoginRequired) {
		log.Println("LUCI login required... initiating interactive login process")
		if err := au.Login(); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("checking whether LUCI login is required: %w", err)
	}
	hc, err := au.Client()
	if err != nil {
		return fmt.Errorf("creating HTTP client: %w", err)
	}
	bc := bbpb.NewBuildsPRPCClient(&prpc.Client{
		C:       hc,
		Host:    chromeinfra.BuildbucketHost,
		Options: prpc.DefaultOptions(),
	})
	build, err := bc.GetBuild(ctx, &bbpb.GetBuildRequest{
		Id: int64(buildID),
		Mask: &bbpb.BuildMask{
			Fields: &fieldmaskpb.FieldMask{
				Paths: []string{
					"id",
					"builder",
					"ancestor_ids",
					"input.properties",
					"infra.resultdb.invocation",
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("getting build info for %d: %w", buildID, err)
	}
	if build.Builder.Project != "golang" {
		return fmt.Errorf("build ID is not for a go build: found builder %s", build.Builder.Builder)
	}

	// Figure out the builder type and the build ID we'll use for GOMOTE_REPRO.
	//
	// This is a bit complex because we want to be able to support users just picking any Go build and
	// plugging them into this tool. This means we should do the right thing whether or not they're looking
	// at a worker build or a coordinator build or something else.
	gomoteBuilderType := build.Builder.Builder // Builder type to pass to createInstances. May not be build.Builder.
	gomoteReproID := int64(buildID)            // Build ID to be passed to GOMOTE_REPRO. May not be build.Id or buildID
	if strings.HasSuffix(build.Builder.Bucket, "-workers") {
		// This is a worker builder. We'll use this for GOMOTE_REPRO, but we need the parent builder to pass to createInstances to get the right gomote.
		// createInstances for example expects a coordinator builder
		coordBuild, err := bc.GetBuild(ctx, &bbpb.GetBuildRequest{Id: build.AncestorIds[len(build.AncestorIds)-1] /* immediate parent */})
		if err != nil {
			return fmt.Errorf("getting build info for parent build %d: %w", buildID, err)
		}
		if coordBuild.Builder.Project != "go" {
			return fmt.Errorf("parent build for worker build unexpectedly not for the go project: for %q project instead", coordBuild.Builder.Project)
		}
		gomoteBuilderType = coordBuild.Builder.Builder
	} else {
		// This may be a coordinator builder. Let's check, and if so, fetch one of its children as the poster-child for GOMOTE_REPRO.
		props := build.Input.Properties.AsMap()
		value, ok := props["mode"]
		if !ok {
			return fmt.Errorf("expected mode property on build %d but did not find one; try updating gomote?", buildID)
		}
		mode, ok := value.(float64)
		if !ok {
			return fmt.Errorf("expected mode property on build %d to have type float64, but it did not: found %T; try updating gomote?", buildID, value)
		}
		if int(mode) == 1 /*MODE_COORDINATOR*/ {
			log.Print("Detected coordinator-mode builder; fetching child builds to use to initialize gomote.")
			resp, err := bc.SearchBuilds(ctx, &bbpb.SearchBuildsRequest{Predicate: &bbpb.BuildPredicate{ChildOf: int64(buildID)}})
			if err != nil {
				return fmt.Errorf("fetching children of %d: %v", buildID, err)
			}
			if len(resp.Builds) == 0 {
				return fmt.Errorf("found no children of %d: if the build is still in progress, try running this command again in a minute or two", buildID)
			}
			// Take any child build.
			gomoteReproID = resp.Builds[0].Id
		}
	}
	log.Printf("Selected build %d to initialize the gomote.", gomoteReproID)

	log.Printf("Creating %d instance(s) of type %s...", cfg.count, gomoteBuilderType)
	instances, group, err := createInstances(ctx, gomoteBuilderType, &cfg)
	if err != nil {
		return err
	}
	log.Printf("Initializing %d instance(s) with environment of %d...", len(instances), gomoteReproID)
	if err := initReproInstances(ctx, instances, gomoteReproID); err != nil {
		return err
	}
	return printTestCommands(ctx, hc, build, instances, group)
}

func initReproInstances(ctx context.Context, instances []string, reproBuildID int64) error {
	var tmpOutDir string
	var tmpOutDirOnce sync.Once
	eg, ctx := errgroup.WithContext(ctx)
	for _, inst := range instances {
		inst := inst
		eg.Go(func() error {
			var err error
			tmpOutDirOnce.Do(func() {
				tmpOutDir, err = os.MkdirTemp("", "gomote")
			})
			if err != nil {
				return fmt.Errorf("failed to create a temporary directory for setup output: %w", err)
			}

			// Create a file to write output to so it doesn't get lost.
			outf, err := os.Create(filepath.Join(tmpOutDir, fmt.Sprintf("%s.stdout", inst)))
			if err != nil {
				return err
			}
			defer func() {
				outf.Close()
				log.Printf("Wrote results from %q to %q.", inst, outf.Name())
			}()
			log.Printf("Streaming results from %q to %q...", inst, outf.Name())

			// If this is the only command running, print to stdout too, for convenience and
			// backwards compatibility.
			outputs := []io.Writer{outf}
			if len(instances) > 1 {
				// Emit detailed progress.
				outputs = append(outputs, os.Stdout)
			} else {
				log.Printf("Initializing gomote %q...", inst)
			}
			return doRun(
				ctx,
				inst,
				"golangbuild",
				[]string{},
				runSystem(true), // Run in the work directory.
				// Set GOMOTE_REPRO and unset GOMOTE_SETUP.
				runEnv([]string{fmt.Sprintf("GOMOTE_REPRO=%d", reproBuildID), "GOMOTE_SETUP="}),
				runWriters(outputs...),
			)
		})
	}
	return eg.Wait()
}

func printTestCommands(ctx context.Context, hc *http.Client, build *bbpb.Build, instances []string, group *groupData) error {
	// Figure out what project this build is for.
	props := build.Input.Properties.AsMap()
	projValue, ok := props["project"]
	if !ok {
		return fmt.Errorf("expected project property on build %d but did not find one; try updating gomote?", build.Id)
	}
	project, ok := projValue.(string)
	if !ok {
		return fmt.Errorf("expected project property on build %d to have type string, but it did not: found %v; try updating gomote?", build.Id, projValue)
	}

	log.Printf("Fetching test results for %d", build.Id)
	rc := rdbpb.NewResultDBClient(&prpc.Client{
		C:    hc,
		Host: chromeinfra.ResultDBHost,
	})
	req := &rdbpb.QueryTestResultsRequest{
		Invocations: []string{build.Infra.Resultdb.Invocation},
		Predicate: &rdbpb.TestResultPredicate{
			TestIdRegexp: ".*",
			Expectancy:   rdbpb.TestResultPredicate_VARIANTS_WITH_UNEXPECTED_RESULTS,
		},
	}
	resp, err := rc.QueryTestResults(ctx, req)
	if err != nil {
		return fmt.Errorf("querying test results: %v", err)
	}
	if len(resp.TestResults) > 0 {
		log.Printf("Found failed tests. Commands to reproduce:")
	}
	var unknownTests []string
	var packageFailures []string
	specialPackages := make(map[string]struct{})
	var benchmarks []test
	var tests []test
	for _, result := range resp.TestResults {
		if result.TestId == "make.bash" {
			log.Printf("$ gomote run go/src/make.bash")
			continue
		}

		// Try to split by ".Test".
		var bench bool
		i := strings.Index(result.TestId, ".Test")
		if i < 0 {
			// That didn't work. Try to split by ".Benchmark".
			i := strings.Index(result.TestId, ".Benchmark")
			if i < 0 {
				// Assume the TestId is a package, for a package-level failure.
				packageFailures = append(packageFailures, result.TestId)
				continue
			}
			bench = true
		}
		t := test{
			pkg:  result.TestId[:i],
			name: result.TestId[i+1:],
		}

		// Look for special packages. These need to be invoked via dist.
		if strings.IndexByte(t.pkg, ':') >= 0 {
			if project == "go" {
				specialPackages[t.pkg] = struct{}{}
			} else {
				// We are almost definitely unable to run this test -- something went very wrong.
				unknownTests = append(unknownTests, result.TestId)
			}
			continue
		}
		if rest, ok := strings.CutPrefix(t.pkg, "golang.org/x/"); ok {
			if strings.HasPrefix(rest, project) {
				t.path = "./x_" + rest
			} else {
				// We are almost definitely unable to run this test -- something went very wrong.
				unknownTests = append(unknownTests, result.TestId)
			}
		} else {
			// Assume it's a std test.
			t.path = "goroot/src/" + t.pkg
		}
		if bench {
			benchmarks = append(benchmarks, t)
		} else {
			tests = append(tests, t)
		}
	}
	prefix := ""
	instName := " " + instances[0]
	if group != nil {
		prefix = "GOMOTE_GROUP=" + group.Name + " "
		instName = ""
	}
	for _, t := range tests {
		log.Printf("$ %sgomote run%s -dir %s goroot/bin/go test -run='%s' .", prefix, instName, t.pkgPath(), t.regexp())
	}
	for _, t := range benchmarks {
		log.Printf("$ %sgomote run%s -dir %s goroot/bin/go test -run='^$' -bench='%s' .", prefix, instName, t.pkgPath(), t.regexp())
	}
	for _, pkg := range specialPackages {
		log.Printf("$ %sgomote run%s -dir ./goroot goroot/bin/go tool dist test %s", prefix, instName, pkg)
	}
	for _, pkg := range packageFailures {
		log.Printf("Note: Found package-level test failure for %s.", pkg)
	}
	for _, name := range unknownTests {
		log.Printf("Note: Unable to parse name of failed test %s.", name)
	}
	return nil
}

type test struct {
	pkg  string
	name string
	path string // Relative to workdir.
}

func (t test) pkgPath() string {
	if t.path != "" {
		return t.path
	}
	return t.pkg
}

// regexp returns a regexp matching this test's name, suitable for passing to -run and -bench.
func (t test) regexp() string {
	cmps := strings.Split(t.name, "/")
	for i, c := range cmps {
		cmps[i] = "^" + c + "$"
	}
	return strings.Join(cmps, "/")
}

func createLUCIAuthenticator(ctx context.Context) *auth.Authenticator {
	authOpts := chromeinfra.SetDefaultAuthOptions(auth.Options{
		Scopes: append([]string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/gerritcodereview",
		}, sauth.CloudOAuthScopes...),
	})
	return auth.NewAuthenticator(ctx, auth.SilentLogin, authOpts)
}
