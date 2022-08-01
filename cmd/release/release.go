// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command release builds a Go release.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/build"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/sync/errgroup"
)

var (
	flagTarget = flag.String("target", "", "The specific target to build.")
	flagWatch  = flag.Bool("watch", false, "Watch the build.")

	flagStagingDir = flag.String("staging_dir", "", "If specified, use this as the staging directory for untested release artifacts. Default is the system temporary directory.")

	flagRevision      = flag.String("rev", "", "Go revision to build")
	flagVersion       = flag.String("version", "", "Version string (go1.5.2)")
	user              = flag.String("user", username(), "coordinator username, appended to 'user-'")
	flagSkipTests     = flag.Bool("skip_tests", false, "skip all tests (only use if sufficient testing was done elsewhere)")
	flagSkipLongTests = flag.Bool("skip_long_tests", false, "skip long tests (only use if sufficient testing was done elsewhere)")

	uploadMode = flag.Bool("upload", false, "Upload files (exclusive to all other flags)")
)

var (
	coordClient *buildlet.CoordinatorClient
	buildEnv    *buildenv.Environment
)

func main() {
	flag.Parse()
	rand.Seed(time.Now().UnixNano())

	if *uploadMode {
		buildenv.CheckUserCredentials()
		userToken() // Call userToken for the side-effect of exiting if a gomote token doesn't exist.
		if err := upload(flag.Args()); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx := &workflow.TaskContext{
		Context: context.TODO(),
		Logger:  &logger{*flagTarget},
	}

	if *flagRevision == "" {
		log.Fatal("must specify -rev")
	}
	if *flagTarget == "" {
		log.Fatal("must specify -target")
	}
	if *flagVersion == "" {
		log.Fatal(`must specify -version flag (such as "go1.12" or "go1.13beta1")`)
	}
	stagingDir := *flagStagingDir
	if stagingDir == "" {
		var err error
		stagingDir, err = ioutil.TempDir("", "go-release-staging_")
		if err != nil {
			log.Fatal(err)
		}
	}
	if *flagTarget == "src" {
		if err := writeSourceFile(ctx, *flagRevision, *flagVersion, *flagVersion+".src.tar.gz"); err != nil {
			log.Fatalf("building source archive: %v", err)
		}
		return
	}

	coordClient = coordinatorClient()
	buildEnv = buildenv.Production

	targets, ok := releasetargets.TargetsForVersion(*flagVersion)
	if !ok {
		log.Fatalf("could not parse version %q", *flagVersion)
	}
	target, ok := targets[*flagTarget]
	if !ok {
		log.Fatalf("no such target %q in version %q", *flagTarget, *flagVersion)
	}
	if *flagSkipTests {
		target.BuildOnly = true
	}
	if *flagSkipLongTests {
		target.LongTestBuilder = ""
	}

	ctx.Printf("Start.")
	if err := doRelease(ctx, *flagRevision, *flagVersion, target, stagingDir, *flagWatch); err != nil {
		ctx.Printf("Error: %v", err)
		os.Exit(1)
	} else {
		ctx.Printf("Done.")
	}
}

const gerritURL = "https://go.googlesource.com/go"

func doRelease(ctx *workflow.TaskContext, revision, version string, target *releasetargets.Target, stagingDir string, watch bool) error {
	srcBuf := &bytes.Buffer{}
	if err := task.WriteSourceArchive(ctx, http.DefaultClient, gerritURL, revision, version, srcBuf); err != nil {
		return fmt.Errorf("Building source archive: %v", err)
	}

	var stagingFiles []*os.File
	stagingFile := func(ext string) (*os.File, error) {
		f, err := ioutil.TempFile(stagingDir, fmt.Sprintf("%v.%v.%v.release-staging-*", version, target.Name, ext))
		stagingFiles = append(stagingFiles, f)
		return f, err
	}
	// runWithBuildlet runs f with a newly-created builder.
	runWithBuildlet := func(builder string, f func(*task.BuildletStep) error) error {
		buildConfig, ok := dashboard.Builders[builder]
		if !ok {
			return fmt.Errorf("unknown builder: %v", buildConfig)
		}
		client, err := coordClient.CreateBuildlet(builder)
		if err != nil {
			return err
		}
		defer client.Close()
		buildletStep := &task.BuildletStep{
			Target:      target,
			Buildlet:    client,
			BuildConfig: buildConfig,
			LogWriter:   os.Stdout,
		}
		if err := f(buildletStep); err != nil {
			return err
		}
		return client.Close()
	}
	defer func() {
		for _, f := range stagingFiles {
			f.Close()
		}
	}()

	// Build the binary distribution.
	binary, err := stagingFile("tar.gz")
	if err != nil {
		return err
	}
	if err := runWithBuildlet(target.Builder, func(step *task.BuildletStep) error {
		return step.BuildBinary(ctx, srcBuf, binary)
	}); err != nil {
		return fmt.Errorf("Building binary archive: %v", err)
	}
	// Multiple tasks need to read the binary archive concurrently. Use a
	// new SectionReader for each to keep them from conflicting.
	binaryReader := func() io.Reader { return io.NewSectionReader(binary, 0, math.MaxInt64) }

	// Do everything else in parallel.
	group, groupCtx := errgroup.WithContext(ctx)

	// If windows, produce the zip and MSI.
	if target.GOOS == "windows" {
		ctx := &workflow.TaskContext{Context: groupCtx, Logger: ctx.Logger}
		msi, err := stagingFile("msi")
		if err != nil {
			return err
		}
		zip, err := stagingFile("zip")
		if err != nil {
			return err
		}
		group.Go(func() error {
			if err := runWithBuildlet(target.Builder, func(step *task.BuildletStep) error {
				return step.BuildMSI(ctx, binaryReader(), msi)
			}); err != nil {
				return fmt.Errorf("Building Windows artifacts: %v", err)
			}
			return nil
		})
		group.Go(func() error {
			return task.ConvertTGZToZIP(binaryReader(), zip)
		})
	}

	// Run tests.
	if !target.BuildOnly {
		runTest := func(builder string) error {
			ctx := &workflow.TaskContext{
				Context: groupCtx,
				Logger:  &logger{fmt.Sprintf("%v (tests on %v)", target.Name, builder)},
			}
			if err := runWithBuildlet(builder, func(step *task.BuildletStep) error {
				return step.TestTarget(ctx, binaryReader())
			}); err != nil {
				return fmt.Errorf("Testing on %v: %v", builder, err)
			}
			return nil
		}
		group.Go(func() error { return runTest(target.Builder) })
		if target.LongTestBuilder != "" {
			group.Go(func() error { return runTest(target.LongTestBuilder) })
		}
	}
	if err := group.Wait(); err != nil {
		return err
	}

	// If we get this far, the all.bash tests have passed (or been skipped).
	// Move untested release files to their final locations.
	stagingRe := regexp.MustCompile(`([^/]*)\.release-staging-.*`)
	for _, f := range stagingFiles {
		if err := f.Close(); err != nil {
			return err
		}
		match := stagingRe.FindStringSubmatch(f.Name())
		if len(match) != 2 {
			return fmt.Errorf("unexpected file name %q didn't match %v", f.Name(), stagingRe)
		}
		finalName := match[1]
		ctx.Printf("Moving %q to %q.", f.Name(), finalName)
		if err := os.Rename(f.Name(), finalName); err != nil {
			return err
		}
	}
	return nil
}

type logger struct {
	Name string
}

func (l *logger) Printf(format string, args ...interface{}) {
	format = fmt.Sprintf("%v: %s", l.Name, format)
	log.Printf(format, args...)
}

func writeSourceFile(ctx *workflow.TaskContext, revision, version, outPath string) error {
	w, err := os.Create(outPath)
	if err != nil {
		return err
	}
	if err := task.WriteSourceArchive(ctx, http.DefaultClient, gerritURL, revision, version, w); err != nil {
		return err
	}
	return w.Close()
}

func coordinatorClient() *buildlet.CoordinatorClient {
	return &buildlet.CoordinatorClient{
		Auth: buildlet.UserPass{
			Username: "user-" + *user,
			Password: userToken(),
		},
		Instance: build.ProdCoordinator,
	}
}

func homeDir() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
	}
	return os.Getenv("HOME")
}

func configDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "Gomote")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gomote")
	}
	return filepath.Join(homeDir(), ".config", "gomote")
}

func username() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("USERNAME")
	}
	return os.Getenv("USER")
}

func userToken() string {
	if *user == "" {
		panic("userToken called with user flag empty")
	}
	keyDir := configDir()
	baseFile := "user-" + *user + ".token"
	tokenFile := filepath.Join(keyDir, baseFile)
	slurp, err := ioutil.ReadFile(tokenFile)
	if os.IsNotExist(err) {
		log.Printf("Missing file %s for user %q. Change --user or obtain a token and place it there.",
			tokenFile, *user)
	}
	if err != nil {
		log.Fatal(err)
	}
	return strings.TrimSpace(string(slurp))
}
