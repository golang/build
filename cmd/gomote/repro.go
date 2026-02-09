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

	"github.com/kballard/go-shellquote"
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
	var public bool
	fs.BoolVar(&cfg.printStatus, "status", true, "print regular status updates while waiting")
	fs.IntVar(&cfg.count, "count", 1, "number of instances to create")
	fs.StringVar(&cfg.newGroup, "new-group", "", "also create a new group and add the new instances to it")
	fs.BoolVar(&public, "public", true, "whether the build you're trying to reproduce is public")
	cfg.useGolangbuild = true

	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
	}
	// Parse as a uint even though we'll end up converting to int64 -- negative build IDs are not valid.
	// Strip an optional "b" prefix. Lots of LUCI URLs and UIs use the prefix, so it makes copy-paste
	// easier.
	buildID, err := strconv.ParseUint(strings.TrimPrefix(fs.Arg(0), "b"), 10, 64)
	if err != nil {
		return fmt.Errorf("parsing build ID %s: %v", fs.Arg(0), err)
	}
	// Always use a default unauthenticated client for public builds.
	ctx := context.Background()
	hc := http.DefaultClient
	if !public {
		au := createLUCIAuthenticator(ctx)
		if err := au.CheckLoginRequired(); errors.Is(err, auth.ErrLoginRequired) {
			log.Println("LUCI login required... initiating interactive login process")
			if err := au.Login(); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("checking whether LUCI login is required: %w", err)
		}
		hc, err = au.Client()
		if err != nil {
			return fmt.Errorf("creating HTTP client: %w", err)
		}
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
		mode, err := mustGetInputProperty[float64](build, "mode")
		if err != nil {
			return err
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
	project, err := mustGetInputProperty[string](build, "project")
	if err != nil {
		return err
	}

	// Check if it's a no-network builder.
	noNetwork, _, err := getInputProperty[bool](build, "no_network")
	if err != nil {
		return err
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
		if rest, ok := cutAnyPrefix(t.pkg, "golang.org/x/", "google.golang.org/"); ok {
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
	wrapCmd := func(dir string, goArgs ...string) string {
		gomoteCmd := []string{"gomote", "run", "-dir", dir}
		if group == nil {
			gomoteCmd = append(gomoteCmd, instances[0])
		}
		if noNetwork {
			relGoPath, err := filepath.Rel(dir, "./goroot/bin/go")
			if err != nil {
				panic(err)
			}
			// TODO(go.dev/issue/69599): It's pretty bad that we're copying this logic out of golangbuild.
			// We really should put this in a wrapper that is more accessible. This is a hack
			// for now to avoid issues like go.dev/issue/69561 from being quite so hard to debug
			// in the future.
			ipCmd := []string{"ip", "link", "set", "dev", "lo", "up"}
			goCmd := append([]string{relGoPath}, goArgs...)
			unshareCmd := []string{"unshare", "--net", "--map-root-user", "--", "sh", "-c", shellquote.Join(ipCmd...) + " && " + shellquote.Join(goCmd...)}
			gomoteCmd = append(gomoteCmd, unshareCmd...)
		} else {
			gomoteCmd = append(gomoteCmd, "goroot/bin/go")
			gomoteCmd = append(gomoteCmd, goArgs...)
		}
		result := shellquote.Join(gomoteCmd...)
		if group != nil {
			result = "GOMOTE_GROUP=" + group.Name + " " + result
		}
		return result
	}
	for _, t := range tests {
		log.Printf("$ %s", wrapCmd(t.path, "test", "-run", t.regexp(), "."))
	}
	for _, t := range benchmarks {
		log.Printf("$ %s", wrapCmd(t.path, "test", "-run", "^$", "-bench", t.regexp(), "."))
	}
	for pkg := range specialPackages {
		log.Printf("$ %s", wrapCmd("./goroot", "tool", "dist", "test", pkg))
	}
	for _, pkg := range packageFailures {
		log.Printf("Note: Found package-level test failure for %s.", pkg)
	}
	for _, name := range unknownTests {
		log.Printf("Note: Unable to parse name of failed test %s.", name)
	}
	return nil
}

func cutAnyPrefix(s string, prefixes ...string) (rest string, ok bool) {
	for _, p := range prefixes {
		rest, ok = strings.CutPrefix(s, p)
		if ok {
			return
		}
	}
	return "", false
}

type test struct {
	pkg  string
	name string
	path string // Relative to workdir.
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

func mustGetInputProperty[T any](build *bbpb.Build, propName string) (T, error) {
	value, present, err := getInputProperty[T](build, propName)
	if err == nil && !present {
		return *new(T), fmt.Errorf("expected %s property on build %d but did not find one; try updating gomote?", propName, build.Id)
	}
	return value, err
}

func getInputProperty[T any](build *bbpb.Build, propName string) (T, bool, error) {
	props := build.Input.Properties.AsMap()
	propValue, ok := props[propName]
	if !ok {
		return *new(T), false, nil
	}
	prop, ok := propValue.(T)
	if !ok {
		return *new(T), true, fmt.Errorf("expected %s property on build %d to have type %T, but it did not: found %v; try updating gomote?", propName, build.Id, *new(T), propValue)
	}
	return prop, true, nil
}
