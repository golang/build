// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command release builds a Go release.
package main

import (
	"archive/tar"
	"archive/zip"
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
	"net/http"
	"os"
	"path"
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
	"golang.org/x/build/internal/workflow"
)

//go:embed releaselet/releaselet.go
var releaselet string

var (
	flagTarget   = flag.String("target", "", "The specific target to build.")
	flagLongTest = flag.Bool("longtest", false, "if false, run the normal build. if true, run only long tests.")
	flagWatch    = flag.Bool("watch", false, "Watch the build.")

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
	if *stagingDir == "" {
		var err error
		*stagingDir, err = ioutil.TempDir("", "go-release-staging_")
		if err != nil {
			log.Fatal(err)
		}
	}
	if *flagTarget == "src" {
		if err := writeSourceFile(*rev, *flagVersion, *flagVersion+".src.tar.gz"); err != nil {
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
	if *skipTests {
		target.BuildOnly = true
	}
	if *flagLongTest && target.BuildOnly {
		log.Fatalf("long testing requested, but no tests to run: skip=%v, build only=%v", *skipTests, target.BuildOnly)
	}

	ctx := &workflow.TaskContext{
		Context: context.TODO(),
		Logger:  &logger{*flagTarget},
	}
	ctx.Printf("Start.")
	if err := doRelease(ctx, *flagVersion, target, *flagLongTest, *flagWatch); err != nil {
		ctx.Printf("Error: %v", err)
		os.Exit(1)
	} else {
		ctx.Printf("Done.")
	}
}

func doRelease(ctx *workflow.TaskContext, version string, target *releasetargets.Target, longTest, watch bool) error {
	ctx.Printf("Create source archive.")
	srcBuf := &bytes.Buffer{}
	if err := writeSource(*rev, version, srcBuf); err != nil {
		return fmt.Errorf("Building source archive: %v", err)
	}
	return buildTarget(ctx, srcBuf, version, target, longTest, watch)
}

type logger struct {
	Name string
}

func (l *logger) Printf(format string, args ...interface{}) {
	format = fmt.Sprintf("%v: %s", l.Name, format)
	log.Printf(format, args...)
}

func writeSourceFile(revision, version, outPath string) error {
	w, err := os.Create(outPath)
	if err != nil {
		return err
	}
	if err := writeSource(revision, version, w); err != nil {
		return err
	}
	return w.Close()
}

func writeSource(revision, version string, out io.Writer) error {
	tarUrl := "https://go.googlesource.com/go/+archive/" + revision + ".tar.gz"
	resp, err := http.Get(tarUrl)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch %q: %v", tarUrl, resp.Status)
	}
	defer resp.Body.Close()
	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	reader := tar.NewReader(gzReader)

	gzWriter := gzip.NewWriter(out)
	writer := tar.NewWriter(gzWriter)

	// Add go/VERSION to the archive, and fix up the existing contents.
	if err := writer.WriteHeader(&tar.Header{
		Name:       "go/VERSION",
		Size:       int64(len(version)),
		Typeflag:   tar.TypeReg,
		Mode:       0644,
		ModTime:    time.Now(),
		AccessTime: time.Now(),
		ChangeTime: time.Now(),
	}); err != nil {
		return err
	}
	if _, err := writer.Write([]byte(version)); err != nil {
		return err
	}
	if err := adjustTar(reader, writer, "go/", []adjustFunc{
		dropRegexpMatches([]string{`VERSION`}), // Don't overwrite our VERSION file from above.
		dropRegexpMatches(dropPatterns),
		fixPermissions(),
	}); err != nil {
		return err
	}

	if err := writer.Close(); err != nil {
		return err
	}
	return gzWriter.Close()
}

const (
	goDir = "go"
	go14  = "go1.4"
)

func buildTarget(ctx *workflow.TaskContext, sourceArchive io.Reader, version string, target *releasetargets.Target, longTest, watch bool) error {
	builder := target.Builder
	if longTest {
		builder = target.LongTestBuilder
	}
	buildConfig, ok := dashboard.Builders[builder]
	if !ok {
		return fmt.Errorf("unknown builder: %v", buildConfig)
	}

	ctx.Printf("Creating buildlet.")
	client, err := coordClient.CreateBuildlet(builder)
	if err != nil {
		return err
	}
	defer client.Close()

	buildletWrapper := &buildletWrapper{target, client, buildConfig, watch}

	// Push source to buildlet.
	ctx.Printf("Pushing source to buildlet.")
	if err := client.PutTar(ctx, sourceArchive, ""); err != nil {
		return fmt.Errorf("failed to put generated source tarball: %v", err)
	}

	if u := buildConfig.GoBootstrapURL(buildEnv); u != "" {
		ctx.Printf("Installing go1.4.")
		if err := client.PutTarFromURL(ctx, u, go14); err != nil {
			return err
		}
	}

	// Execute build (make.bash only first).
	ctx.Printf("Building (make.bash only).")
	makeEnv := []string{"GOROOT_FINAL=" + buildConfig.GorootFinal()}
	makeEnv = append(makeEnv, target.ExtraEnv...)
	if err := buildletWrapper.exec(ctx, goDir+"/"+buildConfig.MakeScript(), buildConfig.MakeScriptArgs(), buildlet.ExecOpts{
		ExtraEnv: makeEnv,
	}); err != nil {
		return err
	}

	if target.Race {
		ctx.Printf("Building race detector.")
		if err := buildletWrapper.runGo(ctx, "install", "-race", "std"); err != nil {
			return err
		}
	}

	ctx.Printf("Building release tarball.")
	fileName := func(ext string) string {
		return fmt.Sprintf("%v.%v.%v", version, target.Name, ext)
	}
	stagingFileName := func(ext string) string {
		return filepath.Join(*stagingDir, fmt.Sprintf("%v.%v.%v.untested", version, target.Name, ext))
	}
	untestedTarballPath := stagingFileName(".tar.gz")
	if err := buildDistribution(ctx, client, untestedTarballPath, []adjustFunc{
		dropRegexpMatches(dropPatterns),
		dropUnwantedSysos(target),
		fixupCrossCompile(target),
		fixPermissions(),
	}); err != nil {
		return fmt.Errorf("building distribution: %v", err)
	}

	ctx.Printf("Refreshing buildlet go directory.")
	// Replace the dirty tree with the distribution's contents before we
	// proceed. This gives us a bit of free testing, and gives the MSI build
	// process a clean start.
	if err := client.RemoveAll(ctx, "go"); err != nil {
		return err
	}
	r, err := os.Open(untestedTarballPath)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := client.PutTar(ctx, r, ""); err != nil {
		return err
	}

	if target.GOOS == "windows" {
		ctx.Printf("Pushing and running releaselet.")
		err = client.Put(ctx, strings.NewReader(releaselet), "releaselet.go", 0666)
		if err != nil {
			return err
		}
		if err := buildletWrapper.runGo(ctx, "run", "releaselet.go"); err != nil {
			ctx.Printf("releaselet failed: %v", err)
			client.ListDir(ctx, ".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
				ctx.Printf("remote: %v", ent)
			})
			return err
		}
	}

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
	if !target.BuildOnly && target.GOOS == "windows" {
		untested := stagingFileName("msi")
		if err := fetchFile(ctx, client, untested, "msi"); err != nil {
			return err
		}
		releases = append(releases, releaseFile{
			Untested: untested,
			Final:    fileName("msi"),
		})
	}

	switch {
	case !target.BuildOnly && target.GOOS != "windows":
		releases = append(releases, releaseFile{
			Untested: untestedTarballPath,
			Final:    fileName("tar.gz"),
		})
	case !target.BuildOnly && target.GOOS == "windows":
		untested := stagingFileName("zip")
		if err := tgzToZip(untestedTarballPath, untested); err != nil {
			return err
		}
		releases = append(releases, releaseFile{
			Untested: untested,
			Final:    fileName("zip"),
		})
	case target.BuildOnly:
		// Use an empty .test-only file to indicate the test outcome.
		// This file gets moved from its initial location in the
		// release-staging directory to the final release directory
		// when the test-only build passes tests successfully.
		untested := stagingFileName("test-only")
		if err := ioutil.WriteFile(untested, nil, 0600); err != nil {
			return fmt.Errorf("writing empty test-only file: %v", err)
		}
		releases = append(releases, releaseFile{
			Untested: untested,
			Final:    fileName("test-only"),
		})
	}

	// Execute build (all.bash) if running tests.
	if target.BuildOnly {
		ctx.Printf("Skipping all.bash tests.")
	} else {
		if u := buildConfig.GoBootstrapURL(buildEnv); u != "" {
			ctx.Printf("Installing go1.4 (second time, for all.bash).")
			if err := client.PutTarFromURL(ctx, u, go14); err != nil {
				return err
			}
		}
		ctx.Printf("Building (all.bash to ensure tests pass).")
		if err := buildletWrapper.exec(ctx, buildConfig.AllScript(), buildConfig.AllScriptArgs(), buildlet.ExecOpts{}); err != nil {
			return err
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

// buildletWrapper provides convenience functions for working with buildlets
// for a release.
type buildletWrapper struct {
	target *releasetargets.Target
	client buildlet.Client
	config *dashboard.BuildConfig
	watch  bool
}

// exec runs cmd with args. Its working dir is opts.Dir, or the directory of cmd.
// Its environment is the buildlet's environment, plus a GOPATH setting, plus opts.ExtraEnv.
// If the command fails, its output is included in the returned error.
func (b *buildletWrapper) exec(ctx context.Context, cmd string, args []string, opts buildlet.ExecOpts) error {
	work, err := b.client.WorkDir(ctx)
	if err != nil {
		return err
	}

	// Set up build environment. The caller's environment wins if there's a conflict.
	env := append(b.config.Env(), "GOPATH="+work+"/gopath")
	env = append(env, opts.ExtraEnv...)
	out := &bytes.Buffer{}
	opts.Output = out
	opts.ExtraEnv = env
	opts.Args = args
	if b.watch {
		opts.Output = io.MultiWriter(opts.Output, os.Stdout)
	}
	err, remoteErr := b.client.Exec(ctx, cmd, opts)
	if err != nil {
		return err
	}
	if remoteErr != nil {
		return fmt.Errorf("Command %v %s failed: %v\nOutput:\n%v", cmd, args, remoteErr, out)
	}
	return nil
}

func (b *buildletWrapper) runGo(ctx context.Context, args ...string) error {
	goCmd := goDir + "/bin/go"
	if b.target.GOOS == "windows" {
		goCmd += ".exe"
	}
	return b.exec(ctx, goCmd, args, buildlet.ExecOpts{
		Dir:  ".", // root of buildlet work directory
		Args: args,
	})
}

func buildDistribution(ctx *workflow.TaskContext, client buildlet.Client, outputPath string, adjusts []adjustFunc) error {
	input, err := client.GetTar(ctx, "go")
	if err != nil {
		return err
	}
	defer input.Close()

	gzReader, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gzReader.Close()
	reader := tar.NewReader(gzReader)
	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	gzWriter := gzip.NewWriter(output)
	writer := tar.NewWriter(gzWriter)
	if err := adjustTar(reader, writer, "go/", adjusts); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := gzWriter.Close(); err != nil {
		return err
	}
	return output.Close()
}

// An adjustFunc updates a tar file header in some way.
// The input is safe to write to. A nil return means to drop the file.
type adjustFunc func(*tar.Header) *tar.Header

// adjustTar copies the files from reader to writer, putting them in prefixDir
// and adjusting them with adjusts along the way.
func adjustTar(reader *tar.Reader, writer *tar.Writer, prefixDir string, adjusts []adjustFunc) error {
	if !strings.HasSuffix(prefixDir, "/") {
		return fmt.Errorf("prefix dir %q must have a trailing /", prefixDir)
	}
	writer.WriteHeader(&tar.Header{
		Name:       prefixDir,
		Typeflag:   tar.TypeDir,
		Mode:       0755,
		ModTime:    time.Now(),
		AccessTime: time.Now(),
		ChangeTime: time.Now(),
	})
file:
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		headerCopy := *header
		newHeader := &headerCopy
		for _, adjust := range adjusts {
			newHeader = adjust(newHeader)
			if newHeader == nil {
				continue file
			}
		}
		newHeader.Name = prefixDir + newHeader.Name
		writer.WriteHeader(newHeader)
		if _, err := io.Copy(writer, reader); err != nil {
			return err
		}
	}
	return nil
}

var dropPatterns = []string{
	// .gitattributes, .github, etc.
	`\..*`,
	// This shouldn't exist, since we create a VERSION file.
	`VERSION.cache`,
	// Remove the build cache that the toolchain build process creates.
	// According to go.dev/cl/82095, it shouldn't exist at all.
	`pkg/obj/.*`,
	// Users don't need the api checker binary pre-built. It's
	// used by tests, but all.bash builds it first.
	`pkg/tool/[^/]+/api`,
	// Remove pkg/${GOOS}_${GOARCH}/cmd. This saves a bunch of
	// space, and users don't typically rebuild cmd/compile,
	// cmd/link, etc. If they want to, they still can, but they'll
	// have to pay the cost of rebuilding dependent libaries. No
	// need to ship them just in case.
	`pkg/[^/]+/cmd/.*`,
	// Clean up .exe~ files; see go.dev/issue/23894.
	`.*\.exe~`,
}

// dropRegexpMatches drops files whose name matches any of patterns.
func dropRegexpMatches(patterns []string) adjustFunc {
	var rejectRegexps []*regexp.Regexp
	for _, pattern := range patterns {
		rejectRegexps = append(rejectRegexps, regexp.MustCompile("^"+pattern+"$"))
	}
	return func(h *tar.Header) *tar.Header {
		for _, regexp := range rejectRegexps {
			if regexp.MatchString(h.Name) {
				return nil
			}
		}
		return h
	}
}

// dropUnwantedSysos drops race detector sysos for other architectures.
func dropUnwantedSysos(target *releasetargets.Target) adjustFunc {
	raceSysoRegexp := regexp.MustCompile(`^src/runtime/race/race_(.*?).syso$`)
	osarch := target.GOOS + "_" + target.GOARCH
	return func(h *tar.Header) *tar.Header {
		matches := raceSysoRegexp.FindStringSubmatch(h.Name)
		if matches != nil && matches[1] != osarch {
			return nil
		}
		return h
	}
}

// fixPermissions sets files' permissions to user-writeable, world-readable.
func fixPermissions() adjustFunc {
	return func(h *tar.Header) *tar.Header {
		if h.Typeflag == tar.TypeDir || h.Mode&0111 != 0 {
			h.Mode = 0755
		} else {
			h.Mode = 0644
		}
		return h
	}
}

// fixupCrossCompile moves cross-compiled tools to their final location and
// drops unnecessary host architecture files.
func fixupCrossCompile(target *releasetargets.Target) adjustFunc {
	if !strings.HasSuffix(target.Builder, "-crosscompile") {
		return func(h *tar.Header) *tar.Header { return h }
	}
	osarch := target.GOOS + "_" + target.GOARCH
	return func(h *tar.Header) *tar.Header {
		// Move cross-compiled tools up to bin/, and drop the existing contents.
		if strings.HasPrefix(h.Name, "bin/") {
			if strings.HasPrefix(h.Name, "bin/"+osarch) {
				h.Name = strings.ReplaceAll(h.Name, "bin/"+osarch, "bin")
			} else {
				return nil
			}
		}
		// Drop host architecture files.
		if strings.HasPrefix(h.Name, "pkg/linux_amd64") ||
			strings.HasPrefix(h.Name, "pkg/tool/linux_amd64") {
			return nil
		}
		return h
	}

}

func tgzToZip(tarballPath, zipPath string) error {
	r, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer r.Close()
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)

	w, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer w.Close()

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
	if err := zw.Close(); err != nil {
		return err
	}
	return w.Close()
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
	ctx.Printf("Wrote %q.", name)
	return nil
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
