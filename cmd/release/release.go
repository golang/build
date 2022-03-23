// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command release builds a Go release.
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/build"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/internal/workflow"
)

//go:embed releaselet/releaselet.go
var releaselet string

var (
	flagTarget   = flag.String("target", "", "The specific target to build.")
	flagLongTest = flag.Bool("longtest", false, "if false, run the normal build. if true, run only long tests.")
	watch        = flag.Bool("watch", false, "Watch the build.")

	stagingDir = flag.String("staging_dir", "", "If specified, use this as the staging directory for untested release artifacts. Default is the system temporary directory.")

	rev         = flag.String("rev", "", "Go revision to build")
	flagVersion = flag.String("version", "", "Version string (go1.5.2)")
	user        = flag.String("user", username(), "coordinator username, appended to 'user-'")
	skipTests   = flag.Bool("skip_tests", false, "skip tests; run make.bash but not all.bash (only use if sufficient testing was done elsewhere)")

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

	if *rev == "" {
		log.Fatal("must specify -rev")
	}
	if *flagTarget == "" {
		log.Fatal("must specify -target")
	}
	if *flagVersion == "" {
		log.Fatal(`must specify -version flag (such as "go1.12" or "go1.13beta1")`)
	}

	coordClient = coordinatorClient()
	buildEnv = buildenv.Production

	var target *releasetargets.Target
	if *flagTarget != "src" {
		targets, ok := releasetargets.TargetsForVersion(*flagVersion)
		if !ok {
			log.Fatalf("could not parse version %q", *flagVersion)
		}
		target, ok = targets[*flagTarget]
		if !ok {
			log.Fatalf("no such target %q in version %q", *flagTarget, *flagVersion)
		}
	}
	if *flagLongTest && (*skipTests || target.BuildOnly) {
		log.Fatalf("long testing requested, but no tests to run: skip=%v, build only=%v", *skipTests, target.BuildOnly)
	}

	ctx := &workflow.TaskContext{
		Context: context.TODO(),
		Logger:  &logger{*flagTarget},
	}
	ctx.Printf("Start.")
	if err := runBuild(ctx, target, *flagLongTest, *flagTarget == "src"); err != nil {
		ctx.Printf("Error: %v", err)
		os.Exit(1)
	} else {
		ctx.Printf("Done.")
	}
}

type logger struct {
	Name string
}

func (l *logger) Printf(format string, args ...interface{}) {
	format = fmt.Sprintf("%v: %s", l.Name, format)
	log.Printf(format, args...)
}

var preBuildCleanFiles = []string{
	".gitattributes",
	".github",
	".gitignore",
	".hgignore",
	".hgtags",
	"misc/dashboard",
	"misc/makerelease",
}

func runBuild(ctx *workflow.TaskContext, target *releasetargets.Target, longTest bool, source bool) error {
	if source {
		target = &releasetargets.Target{
			GOOS:    "linux",
			GOARCH:  "amd64",
			Builder: "linux-amd64",
		}
	}
	builder := target.Builder
	if longTest {
		builder = target.LongTestBuilder
	}
	buildConfig, ok := dashboard.Builders[builder]
	if !ok {
		return fmt.Errorf("unknown builder: %v", buildConfig)
	}

	var hostArch string // non-empty if we're cross-compiling (s390x)
	if target.BuildOnly && buildConfig.IsContainer() && (buildConfig.GOARCH() != "amd64" && buildConfig.GOARCH() != "386") {
		hostArch = "amd64"
	}

	ctx.Printf("Creating buildlet.")
	client, err := coordClient.CreateBuildlet(builder)
	if err != nil {
		return err
	}
	client.SetReleaseMode(true) // disable pargzip; golang.org/issue/19052
	if err != nil {
		return err
	}
	defer client.Close()

	work, err := client.WorkDir(ctx)
	if err != nil {
		return err
	}

	// Push source to buildlet.
	ctx.Printf("Pushing source to buildlet.")
	const (
		goDir  = "go"
		goPath = "gopath"
		go14   = "go1.4"
	)

	tar := "https://go.googlesource.com/go/+archive/" + *rev + ".tar.gz"
	if err := client.PutTarFromURL(ctx, tar, goDir); err != nil {
		ctx.Printf("failed to put tarball %q into dir %q: %v", tar, goDir, err)
		return err
	}

	if u := buildConfig.GoBootstrapURL(buildEnv); u != "" && source {
		ctx.Printf("Installing go1.4.")
		if err := client.PutTarFromURL(ctx, u, go14); err != nil {
			return err
		}
	}

	// Write out version file.
	ctx.Printf("Writing VERSION file.")
	if err := client.Put(ctx, strings.NewReader(*flagVersion), "go/VERSION", 0644); err != nil {
		return err
	}

	ctx.Printf("Cleaning goroot (pre-build).")
	if err := client.RemoveAll(ctx, addPrefix(goDir, preBuildCleanFiles)...); err != nil {
		return err
	}

	if source {
		ctx.Printf("Skipping build.")

		// Remove unwanted top-level directories and verify only "go" remains:
		if err := client.RemoveAll(ctx, "tmp", "gocache"); err != nil {
			return err
		}
		if err := checkTopLevelDirs(ctx, client); err != nil {
			return fmt.Errorf("verifying no unwanted top-level directories: %v", err)
		}
		if err := checkPerm(ctx, client, target, source); err != nil {
			return fmt.Errorf("verifying file permissions: %v", err)
		}

		finalFilename := *flagVersion + "." + *flagTarget + ".tar.gz"
		return fetchTarball(ctx, client, finalFilename)
	}

	// Set up build environment.
	sep := "/"
	if target.GOOS == "windows" {
		sep = "\\"
	}
	env := append(buildConfig.Env(),
		"GOROOT_FINAL="+buildConfig.GorootFinal(),
		"GOROOT="+work+sep+goDir,
		"GOPATH="+work+sep+goPath,
		"GOBIN=",
	)
	env = append(env, target.ExtraEnv...)

	// Execute build (make.bash only first).
	ctx.Printf("Building (make.bash only).")
	out := new(bytes.Buffer)
	var execOut io.Writer = out
	if *watch {
		execOut = io.MultiWriter(out, os.Stdout)
	}
	remoteErr, err := client.Exec(context.Background(), filepath.Join(goDir, buildConfig.MakeScript()), buildlet.ExecOpts{
		Output:   execOut,
		ExtraEnv: env,
		Args:     buildConfig.MakeScriptArgs(),
	})
	if err != nil {
		return err
	}
	if remoteErr != nil {
		return fmt.Errorf("Build failed: %v\nOutput:\n%v", remoteErr, out)
	}

	goCmd := path.Join(goDir, "bin/go")
	if target.GOOS == "windows" {
		goCmd += ".exe"
	}
	runGo := func(args ...string) error {
		out := new(bytes.Buffer)
		var execOut io.Writer = out
		if *watch {
			execOut = io.MultiWriter(out, os.Stdout)
		}
		cmdEnv := append([]string(nil), env...)
		if len(args) > 0 && args[0] == "run" && hostArch != "" {
			cmdEnv = setGOARCH(cmdEnv, hostArch)
		}
		remoteErr, err := client.Exec(context.Background(), goCmd, buildlet.ExecOpts{
			Output:   execOut,
			Dir:      ".", // root of buildlet work directory
			Args:     args,
			ExtraEnv: cmdEnv,
		})
		if err != nil {
			return err
		}
		if remoteErr != nil {
			return fmt.Errorf("go %v: %v\n%s", strings.Join(args, " "), remoteErr, out)
		}
		return nil
	}

	if target.Race {
		ctx.Printf("Building race detector.")

		if err := runGo("install", "-race", "std"); err != nil {
			return err
		}
	}

	// postBuildCleanFiles are the list of files to remove in the go/ directory
	// after things have been built.
	postBuildCleanFiles := []string{
		"VERSION.cache",
		"pkg/bootstrap",
	}

	// Remove race detector *.syso files for other GOOS/GOARCHes (except for the source release).
	if !source {
		okayRace := fmt.Sprintf("race_%s_%s.syso", target.GOOS, target.GOARCH)
		err := client.ListDir(ctx, ".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
			name := strings.TrimPrefix(ent.Name(), "go/")
			if strings.HasPrefix(name, "src/runtime/race/race_") &&
				strings.HasSuffix(name, ".syso") &&
				path.Base(name) != okayRace {
				postBuildCleanFiles = append(postBuildCleanFiles, name)
			}
		})
		if err != nil {
			return fmt.Errorf("enumerating files to clean race syso files: %v", err)
		}
	}

	ctx.Printf("Cleaning goroot (post-build).")
	if err := client.RemoveAll(ctx, addPrefix(goDir, postBuildCleanFiles)...); err != nil {
		return err
	}
	// Users don't need the api checker binary pre-built. It's
	// used by tests, but all.bash builds it first.
	if err := client.RemoveAll(ctx, "go/pkg/tool/"+target.GOOS+"_"+target.GOARCH+"/api"); err != nil {
		return err
	}
	// Remove go/pkg/${GOOS}_${GOARCH}/cmd. This saves a bunch of
	// space, and users don't typically rebuild cmd/compile,
	// cmd/link, etc. If they want to, they still can, but they'll
	// have to pay the cost of rebuilding dependent libaries. No
	// need to ship them just in case.
	//
	// Also remove go/pkg/${GOOS}_${GOARCH}_{dynlink,shared,testcshared_shared}
	// per Issue 20038.
	pkgDir := "go/pkg/" + target.GOOS + "_" + target.GOARCH
	if err := client.RemoveAll(ctx,
		pkgDir+"/cmd",
		pkgDir+"_dynlink",
		pkgDir+"_shared",
		pkgDir+"_testcshared_shared",
	); err != nil {
		return err
	}

	ctx.Printf("Pushing and running releaselet.")
	err = client.Put(ctx, strings.NewReader(releaselet), "releaselet.go", 0666)
	if err != nil {
		return err
	}
	if err := runGo("run", "releaselet.go"); err != nil {
		log.Printf("releaselet failed: %v", err)
		client.ListDir(ctx, ".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
			log.Printf("remote: %v", ent)
		})
		return err
	}

	cleanFiles := []string{"releaselet.go", goPath, go14, "tmp", "gocache"}

	// So far, we've run make.bash. We want to create the release archive next.
	// Since the release archive hasn't been tested yet, place it in a temporary
	// location. After all.bash runs successfully (or gets explicitly skipped),
	// we'll move the release archive to its final location. For long test builds,
	// we only care whether tests passed and do not produce release artifacts.
	type releaseFile struct {
		Untested string // Temporary location of the file before the release has been tested.
		Final    string // Final location where to move the file after the release has been tested.
	}
	var releases []releaseFile
	stagingDir := *stagingDir
	if stagingDir == "" {
		var err error
		stagingDir, err = ioutil.TempDir("", "go-release-staging_")
		if err != nil {
			log.Fatal(err)
		}
	}
	stagingFile := func(ext string) string {
		return filepath.Join(stagingDir, *flagVersion+"."+*flagTarget+ext+".untested")
	}

	if !target.BuildOnly && target.GOOS == "windows" {
		untested := stagingFile(".msi")
		if err := fetchFile(ctx, client, untested, "msi"); err != nil {
			return err
		}
		releases = append(releases, releaseFile{
			Untested: untested,
			Final:    *flagVersion + "." + *flagTarget + ".msi",
		})
	}

	if target.GOOS == "windows" {
		cleanFiles = append(cleanFiles, "msi")
	}
	if target.GOOS == "windows" && target.GOARCH == "arm64" {
		// At least on windows-arm64, 'wix/winterop.dll' gets created.
		// Delete the entire wix directory since it's unrelated to Go.
		cleanFiles = append(cleanFiles, "wix")
	}

	// Need to delete everything except the final "go" directory,
	// as we make the tarball relative to workdir.
	ctx.Printf("Cleaning workdir.")
	if err := client.RemoveAll(ctx, cleanFiles...); err != nil {
		return err
	}

	// And verify there's no other top-level stuff besides the "go" directory:
	if err := checkTopLevelDirs(ctx, client); err != nil {
		return fmt.Errorf("verifying no unwanted top-level directories: %v", err)
	}

	if err := checkPerm(ctx, client, target, source); err != nil {
		return fmt.Errorf("verifying file permissions: %v", err)
	}

	switch {
	case !target.BuildOnly && target.GOOS != "windows":
		untested := stagingFile(".tar.gz")
		if err := fetchTarball(ctx, client, untested); err != nil {
			return fmt.Errorf("fetching and writing tarball: %v", err)
		}
		releases = append(releases, releaseFile{
			Untested: untested,
			Final:    *flagVersion + "." + *flagTarget + ".tar.gz",
		})
	case !target.BuildOnly && target.GOOS == "windows":
		untested := stagingFile(".zip")
		if err := fetchZip(ctx, client, untested); err != nil {
			return fmt.Errorf("fetching and writing zip: %v", err)
		}
		releases = append(releases, releaseFile{
			Untested: untested,
			Final:    *flagVersion + "." + *flagTarget + ".zip",
		})
	case target.BuildOnly:
		// Use an empty .test-only file to indicate the test outcome.
		// This file gets moved from its initial location in the
		// release-staging directory to the final release directory
		// when the test-only build passes tests successfully.
		untested := stagingFile(".test-only")
		if err := ioutil.WriteFile(untested, nil, 0600); err != nil {
			return fmt.Errorf("writing empty test-only file: %v", err)
		}
		releases = append(releases, releaseFile{
			Untested: untested,
			Final:    *flagVersion + "." + *flagTarget + ".test-only",
		})
	}

	// Execute build (all.bash) if running tests.
	if *skipTests || target.BuildOnly {
		ctx.Printf("Skipping all.bash tests.")
	} else {
		if u := buildConfig.GoBootstrapURL(buildEnv); u != "" {
			ctx.Printf("Installing go1.4 (second time, for all.bash).")
			if err := client.PutTarFromURL(ctx, u, go14); err != nil {
				return err
			}
		}

		ctx.Printf("Building (all.bash to ensure tests pass).")
		out := new(bytes.Buffer)
		var execOut io.Writer = out
		if *watch {
			execOut = io.MultiWriter(out, os.Stdout)
		}
		remoteErr, err := client.Exec(ctx, filepath.Join(goDir, buildConfig.AllScript()), buildlet.ExecOpts{
			Output:   execOut,
			ExtraEnv: env,
			Args:     buildConfig.AllScriptArgs(),
		})
		if err != nil {
			return err
		}
		if remoteErr != nil {
			return fmt.Errorf("Build failed: %v\nOutput:\n%v", remoteErr, out)
		}
	}

	// If we get this far, the all.bash tests have passed (or been skipped).
	// Move untested release files to their final locations.
	for _, r := range releases {
		ctx.Printf("Moving %q to %q.", r.Untested, r.Final)
		if err := os.Rename(r.Untested, r.Final); err != nil {
			return err
		}
	}
	return nil
}

// checkTopLevelDirs checks that all files under client's "."
// ($WORKDIR) are are under "go/".
func checkTopLevelDirs(ctx *workflow.TaskContext, client buildlet.Client) error {
	var badFileErr error // non-nil once an unexpected file/dir is found
	if err := client.ListDir(ctx, ".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
		name := ent.Name()
		if !(strings.HasPrefix(name, "go/") || strings.HasPrefix(name, `go\`)) {
			ctx.Printf("unexpected file: %q", name)
			if badFileErr == nil {
				badFileErr = fmt.Errorf("unexpected filename %q found after cleaning", name)
			}
		}
	}); err != nil {
		return err
	}
	return badFileErr
}

// checkPerm checks that files in client's $WORKDIR/go directory
// have expected permissions.
func checkPerm(ctx *workflow.TaskContext, client buildlet.Client, target *releasetargets.Target, source bool) error {
	var badPermErr error // non-nil once an unexpected perm is found
	checkPerm := func(ent buildlet.DirEntry, allowed ...string) {
		for _, p := range allowed {
			if ent.Perm() == p {
				return
			}
		}
		ctx.Printf("unexpected file %q perm: %q", ent.Name(), ent.Perm())
		if badPermErr == nil {
			badPermErr = fmt.Errorf("unexpected file %q perm %q found", ent.Name(), ent.Perm())
		}
	}
	if err := client.ListDir(ctx, "go", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
		switch target.GOOS {
		default:
			checkPerm(ent, "drwxr-xr-x", "-rw-r--r--", "-rwxr-xr-x")
		case "windows":
			checkPerm(ent, "drwxrwxrwx", "-rw-rw-rw-")
		}
	}); err != nil {
		return err
	}
	if !source {
		if err := client.ListDir(ctx, "go/bin", buildlet.ListDirOpts{}, func(ent buildlet.DirEntry) {
			switch target.GOOS {
			default:
				checkPerm(ent, "-rwxr-xr-x")
			case "windows":
				checkPerm(ent, "-rw-rw-rw-")
			}
		}); err != nil {
			return err
		}
	}
	return badPermErr
}

func fetchTarball(ctx *workflow.TaskContext, client buildlet.Client, dest string) error {
	ctx.Printf("Downloading tarball.")
	tgz, err := client.GetTar(ctx, ".")
	if err != nil {
		return err
	}
	return writeFile(ctx, dest, tgz)
}

func fetchZip(ctx *workflow.TaskContext, client buildlet.Client, dest string) error {
	ctx.Printf("Downloading tarball and re-compressing as zip.")

	tgz, err := client.GetTar(context.Background(), ".")
	if err != nil {
		return err
	}
	defer tgz.Close()

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if err := tgzToZip(f, tgz); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ctx.Printf("Wrote %q.", dest)
	return nil
}

func tgzToZip(w io.Writer, r io.Reader) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)

	zw := zip.NewWriter(w)
	for {
		th, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		fi := th.FileInfo()
		zh, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		zh.Name = th.Name // for the full path
		switch strings.ToLower(path.Ext(zh.Name)) {
		case ".jpg", ".jpeg", ".png", ".gif":
			// Don't re-compress already compressed files.
			zh.Method = zip.Store
		default:
			zh.Method = zip.Deflate
		}
		if fi.IsDir() {
			zh.Method = zip.Store
		}
		w, err := zw.CreateHeader(zh)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			continue
		}
		if _, err := io.Copy(w, tr); err != nil {
			return err
		}
	}
	return zw.Close()
}

// fetchFile fetches the specified directory from the given buildlet, and
// writes the first file it finds in that directory to dest.
func fetchFile(ctx *workflow.TaskContext, client buildlet.Client, dest, dir string) error {
	ctx.Printf("Downloading file from %q.", dir)
	tgz, err := client.GetTar(context.Background(), dir)
	if err != nil {
		return err
	}
	defer tgz.Close()
	zr, err := gzip.NewReader(tgz)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
		if !h.FileInfo().IsDir() {
			break
		}
	}
	return writeFile(ctx, dest, tr)
}

func writeFile(ctx *workflow.TaskContext, name string, r io.Reader) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if strings.HasSuffix(name, ".gz") {
		if err := verifyGzipSingleStream(name); err != nil {
			return fmt.Errorf("error verifying that %s is a single-stream gzip: %v", name, err)
		}
	}
	ctx.Printf("Wrote %q.", name)
	return nil
}

// verifyGzipSingleStream verifies that the named gzip file is not
// a multi-stream file. See golang.org/issue/19052
func verifyGzipSingleStream(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	zr, err := gzip.NewReader(br)
	if err != nil {
		return err
	}
	zr.Multistream(false)
	if _, err := io.Copy(ioutil.Discard, zr); err != nil {
		return fmt.Errorf("reading first stream: %v", err)
	}
	peek, err := br.Peek(1)
	if len(peek) > 0 || err != io.EOF {
		return fmt.Errorf("unexpected peek of %d, %v after first gzip stream", len(peek), err)
	}
	return nil
}

func addPrefix(prefix string, in []string) []string {
	var out []string
	for _, s := range in {
		out = append(out, path.Join(prefix, s))
	}
	return out
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

func setGOARCH(env []string, goarch string) []string {
	wantKV := "GOARCH=" + goarch
	existing := false
	for i, kv := range env {
		if strings.HasPrefix(kv, "GOARCH=") && kv != wantKV {
			env[i] = wantKV
			existing = true
		}
	}
	if existing {
		return env
	}
	return append(env, wantKV)
}
