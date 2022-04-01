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
	"math"
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
	"golang.org/x/sync/errgroup"
)

//go:embed releaselet/releaselet.go
var releaselet string

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

func doRelease(ctx *workflow.TaskContext, revision, version string, target *releasetargets.Target, stagingDir string, watch bool) error {
	srcBuf := &bytes.Buffer{}
	if err := writeSource(ctx, revision, version, srcBuf); err != nil {
		return fmt.Errorf("Building source archive: %v", err)
	}

	var stagingFiles []*os.File
	stagingFile := func(ext string) (*os.File, error) {
		f, err := ioutil.TempFile(stagingDir, fmt.Sprintf("%v.%v.%v.release-staging-*", version, target.Name, ext))
		stagingFiles = append(stagingFiles, f)
		return f, err
	}
	defer func() {
		for _, f := range stagingFiles {
			f.Close()
		}
	}()

	// Build the binary distribution.
	wrapper, err := newBuildletWrapper(ctx, target.Builder, target, coordClient, watch)
	if err != nil {
		return err
	}
	defer wrapper.Close()
	binary, err := stagingFile("tar.gz")
	if err != nil {
		return err
	}
	if err := buildBinary(ctx, wrapper, srcBuf, binary, version); err != nil {
		return fmt.Errorf("Building binary archive: %v", err)
	}
	// Multiple tasks need to read the binary archive concurrently. Use a
	// new SectionReader for each to keep them from conflicting.
	binaryReader := func() io.Reader { return io.NewSectionReader(binary, 0, math.MaxInt64) }
	if err := wrapper.Close(); err != nil {
		return err
	}

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
			wrapper, err := newBuildletWrapper(ctx, target.Builder, target, coordClient, watch)
			if err != nil {
				return err
			}
			defer wrapper.Close()
			if err := buildMSI(ctx, wrapper, binaryReader(), msi); err != nil {
				return fmt.Errorf("Building Windows artifacts: %v", err)
			}
			return wrapper.Close()
		})
		group.Go(func() error {
			return tgzToZip(binaryReader(), zip)
		})
	}

	// Run tests.
	if !target.BuildOnly {
		runTest := func(builder string) error {
			ctx := &workflow.TaskContext{
				Context: groupCtx,
				Logger:  &logger{fmt.Sprintf("%v (tests on %v)", target.Name, builder)},
			}
			wrapper, err := newBuildletWrapper(ctx, builder, target, coordClient, watch)
			if err != nil {
				return err
			}
			defer wrapper.Close()
			if err := testTarget(ctx, wrapper, binaryReader()); err != nil {
				return fmt.Errorf("Testing on %v: %v", builder, err)
			}
			return wrapper.Close()
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
	stagingRe := regexp.MustCompile(`(.*)\.release-staging-.*`)
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
	if err := writeSource(ctx, revision, version, w); err != nil {
		return err
	}
	return w.Close()
}

func writeSource(ctx *workflow.TaskContext, revision, version string, out io.Writer) error {
	ctx.Printf("Create source archive.")
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

func buildBinary(ctx *workflow.TaskContext, wrapper *buildletWrapper, sourceArchive io.Reader, out io.Writer, version string) error {
	// Push source to buildlet.
	ctx.Printf("Pushing source to buildlet.")
	if err := wrapper.client.PutTar(ctx, sourceArchive, ""); err != nil {
		return fmt.Errorf("failed to put generated source tarball: %v", err)
	}

	if u := wrapper.config.GoBootstrapURL(buildEnv); u != "" {
		ctx.Printf("Installing go1.4.")
		if err := wrapper.client.PutTarFromURL(ctx, u, go14); err != nil {
			return err
		}
	}

	// Execute build (make.bash only first).
	ctx.Printf("Building (make.bash only).")
	makeEnv := []string{"GOROOT_FINAL=" + wrapper.config.GorootFinal()}
	makeEnv = append(makeEnv, wrapper.target.ExtraEnv...)
	if err := wrapper.exec(ctx, goDir+"/"+wrapper.config.MakeScript(), wrapper.config.MakeScriptArgs(), buildlet.ExecOpts{
		ExtraEnv: makeEnv,
	}); err != nil {
		return err
	}

	if wrapper.target.Race {
		ctx.Printf("Building race detector.")
		if err := wrapper.runGo(ctx, "install", "-race", "std"); err != nil {
			return err
		}
	}

	ctx.Printf("Building release tarball.")
	input, err := wrapper.client.GetTar(ctx, "go")
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
	gzWriter := gzip.NewWriter(out)
	writer := tar.NewWriter(gzWriter)
	if err := adjustTar(reader, writer, "go/", []adjustFunc{
		dropRegexpMatches(dropPatterns),
		dropUnwantedSysos(wrapper.target),
		fixupCrossCompile(wrapper.target),
		fixPermissions(),
	}); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return gzWriter.Close()
}

func buildMSI(ctx *workflow.TaskContext, wrapper *buildletWrapper, binaryArchive io.Reader, msi io.Writer) error {
	if err := wrapper.client.PutTar(ctx, binaryArchive, ""); err != nil {
		return err
	}
	ctx.Printf("Pushing and running releaselet.")
	if err := wrapper.client.Put(ctx, strings.NewReader(releaselet), "releaselet.go", 0666); err != nil {
		return err
	}
	if err := wrapper.runGo(ctx, "run", "releaselet.go"); err != nil {
		ctx.Printf("releaselet failed: %v", err)
		wrapper.client.ListDir(ctx, ".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
			ctx.Printf("remote: %v", ent)
		})
		return err
	}
	return fetchFile(ctx, wrapper.client, msi, "msi")
}

func testTarget(ctx *workflow.TaskContext, wrapper *buildletWrapper, binaryArchive io.Reader) error {
	if err := wrapper.client.PutTar(ctx, binaryArchive, ""); err != nil {
		return err
	}
	if u := wrapper.config.GoBootstrapURL(buildEnv); u != "" {
		ctx.Printf("Installing go1.4 (second time, for all.bash).")
		if err := wrapper.client.PutTarFromURL(ctx, u, go14); err != nil {
			return err
		}
	}
	ctx.Printf("Building (all.bash to ensure tests pass).")
	return wrapper.exec(ctx, goDir+"/"+wrapper.config.AllScript(), wrapper.config.AllScriptArgs(), buildlet.ExecOpts{})
}

// buildletWrapper provides convenience functions for working with buildlets
// for a release.
type buildletWrapper struct {
	target *releasetargets.Target
	client buildlet.Client
	config *dashboard.BuildConfig
	watch  bool
}

func newBuildletWrapper(ctx *workflow.TaskContext, builder string, target *releasetargets.Target, coordClient *buildlet.CoordinatorClient, watch bool) (*buildletWrapper, error) {
	buildConfig, ok := dashboard.Builders[builder]
	if !ok {
		return nil, fmt.Errorf("unknown builder: %v", buildConfig)
	}
	ctx.Printf("Creating buildlet.")
	client, err := coordClient.CreateBuildlet(builder)
	if err != nil {
		return nil, err
	}
	return &buildletWrapper{target, client, buildConfig, watch}, nil
}

func (b *buildletWrapper) Close() error {
	return b.client.Close()
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

func tgzToZip(r io.Reader, w io.Writer) error {
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
func fetchFile(ctx *workflow.TaskContext, client buildlet.Client, dest io.Writer, dir string) error {
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
	_, err = io.Copy(dest, tr)
	return err
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
