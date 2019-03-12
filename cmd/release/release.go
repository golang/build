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
	"flag"
	"fmt"
	gobuild "go/build"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/build"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
)

var (
	target = flag.String("target", "", "If specified, build specific target platform (e.g. 'linux-amd64'). Default is to build all.")
	watch  = flag.Bool("watch", false, "Watch the build. Only compatible with -target")

	rev       = flag.String("rev", "", "Go revision to build, alternative to -tarball")
	tarball   = flag.String("tarball", "", "Go tree tarball to build, alternative to -rev")
	toolsRev  = flag.String("tools", "", "Tools revision to build")
	tourRev   = flag.String("tour", "master", "Tour revision to include")
	netRev    = flag.String("net", "master", "Net revision to include")
	version   = flag.String("version", "", "Version string (go1.5.2)")
	user      = flag.String("user", username(), "coordinator username, appended to 'user-'")
	skipTests = flag.Bool("skip_tests", false, "skip tests; run make.bash instead of all.bash (only use if you ran trybots first)")

	uploadMode = flag.Bool("upload", false, "Upload files (exclusive to all other flags)")
	uploadKick = flag.String("edge_kick_command", "", "Command to run to kick off an edge cache update")
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
		if err := upload(flag.Args()); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := findReleaselet(); err != nil {
		log.Fatalf("couldn't find releaselet source: %v", err)
	}

	if (*rev == "" && *tarball == "") || (*rev != "" && *tarball != "") {
		log.Fatal("must specify one of -rev and -tarball")
	}
	if *toolsRev == "" {
		log.Fatal("must specify -tools flag")
	}
	if *version == "" {
		log.Fatal("must specify -version flag")
	}

	coordClient = coordinatorClient()
	buildEnv = buildenv.Production

	var wg sync.WaitGroup
	matches := 0
	for _, b := range builds {
		b := b
		if *target != "" && b.String() != *target {
			continue
		}
		matches++
		b.logf("Start.")
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.make(); err != nil {
				b.logf("Error: %v", err)
			} else {
				b.logf("Done.")
			}
		}()
	}
	if *target != "" && matches == 0 {
		log.Fatalf("no targets matched %q", *target)
	}
	wg.Wait()
}

var releaselet = "releaselet.go"

func findReleaselet() error {
	// First try the working directory.
	if _, err := os.Stat(releaselet); err == nil {
		return nil
	}

	// Then, try to locate the release command in the workspace.
	const importPath = "golang.org/x/build/cmd/release"
	pkg, err := gobuild.Import(importPath, "", gobuild.FindOnly)
	if err != nil {
		return fmt.Errorf("finding %q: %v", importPath, err)
	}
	r := filepath.Join(pkg.Dir, releaselet)
	if _, err := os.Stat(r); err != nil {
		return err
	}
	releaselet = r
	return nil
}

type Build struct {
	OS, Arch string
	Source   bool

	Race bool // Build race detector.

	Builder string // Key for dashboard.Builders.

	Goarm    int  // GOARM value if set.
	MakeOnly bool // don't run tests; needed by cross-compile builders (s390x)
}

func (b *Build) String() string {
	if b.Source {
		return "src"
	}
	if b.Goarm != 0 {
		return fmt.Sprintf("%v-%vv%vl", b.OS, b.Arch, b.Goarm)
	}
	return fmt.Sprintf("%v-%v", b.OS, b.Arch)
}

func (b *Build) toolDir() string { return "go/pkg/tool/" + b.OS + "_" + b.Arch }
func (b *Build) pkgDir() string  { return "go/pkg/" + b.OS + "_" + b.Arch }

func (b *Build) logf(format string, args ...interface{}) {
	format = fmt.Sprintf("%v: %s", b, format)
	log.Printf(format, args...)
}

var builds = []*Build{
	{
		Source:  true,
		Builder: "linux-amd64",
	},
	{
		OS:      "linux",
		Arch:    "386",
		Builder: "linux-386",
	},
	{
		OS:      "linux",
		Arch:    "arm",
		Builder: "linux-arm",
		Goarm:   6, // for compatibility with all Raspberry Pi models.
		// The tests take too long for the release packaging.
		// Much of the time the whole buildlet times out.
		MakeOnly: true,
	},
	{
		OS:      "linux",
		Arch:    "amd64",
		Race:    true,
		Builder: "linux-amd64",
	},
	{
		OS:      "linux",
		Arch:    "arm64",
		Builder: "linux-arm64-packet",
	},
	{
		OS:      "freebsd",
		Arch:    "386",
		Builder: "freebsd-386-11_1",
	},
	{
		OS:      "freebsd",
		Arch:    "amd64",
		Race:    true,
		Builder: "freebsd-amd64-11_1",
	},
	{
		OS:      "windows",
		Arch:    "386",
		Builder: "windows-386-2008",
	},
	{
		OS:      "windows",
		Arch:    "amd64",
		Race:    true,
		Builder: "windows-amd64-2008",
	},
	{
		OS:      "darwin",
		Arch:    "amd64",
		Race:    true,
		Builder: "darwin-amd64-10_11",
	},
	{
		OS:       "linux",
		Arch:     "s390x",
		MakeOnly: true,
		Builder:  "linux-s390x-crosscompile",
	},
	// TODO(bradfitz): switch this ppc64 builder to a Kubernetes
	// container cross-compiling ppc64 like the s390x one? For
	// now, the ppc64le builders (5) are back, so let's see if we
	// can just depend on them not going away.
	{
		OS:       "linux",
		Arch:     "ppc64le",
		MakeOnly: true,
		Builder:  "linux-ppc64le-buildlet",
	},
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

func (b *Build) buildlet() (*buildlet.Client, error) {
	b.logf("Creating buildlet.")
	bc, err := coordClient.CreateBuildlet(b.Builder)
	if err != nil {
		return nil, err
	}
	bc.SetReleaseMode(true) // disable pargzip; golang.org/issue/19052
	return bc, nil
}

func (b *Build) make() error {
	bc, ok := dashboard.Builders[b.Builder]
	if !ok {
		return fmt.Errorf("unknown builder: %v", bc)
	}

	var hostArch string // non-empty if we're cross-compiling (s390x)
	if b.MakeOnly && bc.IsContainer() && (bc.GOARCH() != "amd64" && bc.GOARCH() != "386") {
		hostArch = "amd64"
	}

	client, err := b.buildlet()
	if err != nil {
		return err
	}
	defer client.Close()

	work, err := client.WorkDir()
	if err != nil {
		return err
	}

	// Push source to buildlet
	b.logf("Pushing source to buildlet.")
	const (
		goDir  = "go"
		goPath = "gopath"
		go14   = "go1.4"
	)
	if *tarball != "" {
		tarFile, err := os.Open(*tarball)
		if err != nil {
			b.logf("failed to open tarball %q: %v", *tarball, err)
			return err
		}
		if err := client.PutTar(tarFile, goDir); err != nil {
			b.logf("failed to put tarball %q into dir %q: %v", *tarball, goDir, err)
			return err
		}
		tarFile.Close()
	} else {
		tar := "https://go.googlesource.com/go/+archive/" + *rev + ".tar.gz"
		if err := client.PutTarFromURL(tar, goDir); err != nil {
			b.logf("failed to put tarball %q into dir %q: %v", tar, goDir, err)
			return err
		}
	}
	for _, r := range []struct {
		repo, rev string
	}{
		{"tools", *toolsRev},
		{"tour", *tourRev},
		{"net", *netRev},
	} {
		if b.Source {
			continue
		}
		if r.repo == "tour" && !versionIncludesTour(*version) {
			continue
		}
		dir := goPath + "/src/golang.org/x/" + r.repo
		tar := "https://go.googlesource.com/" + r.repo + "/+archive/" + r.rev + ".tar.gz"
		if err := client.PutTarFromURL(tar, dir); err != nil {
			b.logf("failed to put tarball %q into dir %q: %v", tar, dir, err)
			return err
		}
	}

	if u := bc.GoBootstrapURL(buildEnv); u != "" && !b.Source {
		b.logf("Installing go1.4.")
		if err := client.PutTarFromURL(u, go14); err != nil {
			return err
		}
	}

	// Write out version file.
	b.logf("Writing VERSION file.")
	if err := client.Put(strings.NewReader(*version), "go/VERSION", 0644); err != nil {
		return err
	}

	b.logf("Cleaning goroot (pre-build).")
	if err := client.RemoveAll(addPrefix(goDir, preBuildCleanFiles)...); err != nil {
		return err
	}

	if b.Source {
		b.logf("Skipping build.")

		// Remove unwanted top-level directories and verify only "go" remains:
		if err := client.RemoveAll("tmp", "gocache"); err != nil {
			return err
		}
		if err := b.checkTopLevelDirs(client); err != nil {
			return fmt.Errorf("verifying no unwanted top-level directories: %v", err)
		}

		return b.fetchTarball(client)
	}

	// Set up build environment.
	sep := "/"
	if b.OS == "windows" {
		sep = "\\"
	}
	env := append(bc.Env(),
		"GOROOT_FINAL="+bc.GorootFinal(),
		"GOROOT="+work+sep+goDir,
		"GOPATH="+work+sep+goPath,
		"GOBIN=",
	)

	if b.Goarm > 0 {
		env = append(env, fmt.Sprintf("GOARM=%d", b.Goarm))
		env = append(env, fmt.Sprintf("CGO_CFLAGS=-march=armv%d", b.Goarm))
		env = append(env, fmt.Sprintf("CGO_LDFLAGS=-march=armv%d", b.Goarm))
	}

	// Execute build
	b.logf("Building.")
	out := new(bytes.Buffer)
	script := bc.AllScript()
	scriptArgs := bc.AllScriptArgs()
	if *skipTests || b.MakeOnly {
		script = bc.MakeScript()
		scriptArgs = bc.MakeScriptArgs()
	}
	all := filepath.Join(goDir, script)
	var execOut io.Writer = out
	if *watch && *target != "" {
		execOut = io.MultiWriter(out, os.Stdout)
	}
	remoteErr, err := client.Exec(all, buildlet.ExecOpts{
		Output:   execOut,
		ExtraEnv: env,
		Args:     scriptArgs,
	})
	if err != nil {
		return err
	}
	if remoteErr != nil {
		return fmt.Errorf("Build failed: %v\nOutput:\n%v", remoteErr, out)
	}

	goCmd := path.Join(goDir, "bin/go")
	if b.OS == "windows" {
		goCmd += ".exe"
	}
	runGo := func(args ...string) error {
		out := new(bytes.Buffer)
		var execOut io.Writer = out
		if *watch && *target != "" {
			execOut = io.MultiWriter(out, os.Stdout)
		}
		cmdEnv := append([]string(nil), env...)
		if len(args) > 0 && args[0] == "run" && hostArch != "" {
			cmdEnv = setGOARCH(cmdEnv, hostArch)
		}
		remoteErr, err := client.Exec(goCmd, buildlet.ExecOpts{
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

	if b.Race {
		b.logf("Building race detector.")

		if err := runGo("install", "-race", "std"); err != nil {
			return err
		}
	}

	toolPaths := []string{
		"golang.org/x/tools/cmd/godoc",
	}
	if versionIncludesTour(*version) {
		toolPaths = append(toolPaths, "golang.org/x/tour")
	}
	b.logf("Building %v.", strings.Join(toolPaths, ", "))
	if err := runGo(append([]string{"install"}, toolPaths...)...); err != nil {
		return err
	}

	// postBuildCleanFiles are the list of files to remove in the go/ directory
	// after things have been built.
	postBuildCleanFiles := []string{
		"VERSION.cache",
		"pkg/bootstrap",
	}

	// Remove race detector *.syso files for other GOOS/GOARCHes (except for the source release).
	if !b.Source {
		okayRace := fmt.Sprintf("race_%s_%s.syso", b.OS, b.Arch)
		err := client.ListDir(".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
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

	b.logf("Cleaning goroot (post-build).")
	if err := client.RemoveAll(addPrefix(goDir, postBuildCleanFiles)...); err != nil {
		return err
	}
	// Users don't need the api checker binary pre-built. It's
	// used by tests, but all.bash builds it first.
	if err := client.RemoveAll(b.toolDir() + "/api"); err != nil {
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
	if err := client.RemoveAll(
		b.pkgDir()+"/cmd",
		b.pkgDir()+"_dynlink",
		b.pkgDir()+"_shared",
		b.pkgDir()+"_testcshared_shared",
	); err != nil {
		return err
	}

	b.logf("Pushing and running releaselet.")
	f, err := os.Open(releaselet)
	if err != nil {
		return err
	}
	err = client.Put(f, "releaselet.go", 0666)
	f.Close()
	if err != nil {
		return err
	}
	if err := runGo("run", "releaselet.go"); err != nil {
		log.Printf("releaselet failed: %v", err)
		client.ListDir(".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
			log.Printf("remote: %v", ent)
		})
		return err
	}

	cleanFiles := []string{"releaselet.go", goPath, go14, "tmp", "gocache"}

	switch b.OS {
	case "darwin":
		filename := *version + "." + b.String() + ".pkg"
		if err := b.fetchFile(client, filename, "pkg"); err != nil {
			return err
		}
		cleanFiles = append(cleanFiles, "pkg")
	case "windows":
		filename := *version + "." + b.String() + ".msi"
		if err := b.fetchFile(client, filename, "msi"); err != nil {
			return err
		}
		cleanFiles = append(cleanFiles, "msi")
	}

	// Need to delete everything except the final "go" directory,
	// as we make the tarball relative to workdir.
	b.logf("Cleaning workdir.")
	if err := client.RemoveAll(cleanFiles...); err != nil {
		return err
	}

	// And verify there's no other top-level stuff besides the "go" directory:
	if err := b.checkTopLevelDirs(client); err != nil {
		return fmt.Errorf("verifying no unwanted top-level directories: %v", err)
	}

	if b.OS == "windows" {
		return b.fetchZip(client)
	}
	return b.fetchTarball(client)
}

// checkTopLevelDirs checks that all files under client's "."
// ($WORKDIR) are are under "go/".
func (b *Build) checkTopLevelDirs(client *buildlet.Client) error {
	var badFileErr error // non-nil once an unexpected file/dir is found
	if err := client.ListDir(".", buildlet.ListDirOpts{Recursive: true}, func(ent buildlet.DirEntry) {
		name := ent.Name()
		if !(strings.HasPrefix(name, "go/") || strings.HasPrefix(name, `go\`)) {
			b.logf("unexpected file: %q", name)
			if badFileErr == nil {
				badFileErr = fmt.Errorf("unexpected filename %q found after cleaning", name)
			}
		}
	}); err != nil {
		return err
	}
	return badFileErr
}

func (b *Build) fetchTarball(client *buildlet.Client) error {
	b.logf("Downloading tarball.")
	tgz, err := client.GetTar(context.Background(), ".")
	if err != nil {
		return err
	}
	filename := *version + "." + b.String() + ".tar.gz"
	return b.writeFile(filename, tgz)
}

func (b *Build) fetchZip(client *buildlet.Client) error {
	b.logf("Downloading tarball and re-compressing as zip.")

	tgz, err := client.GetTar(context.Background(), ".")
	if err != nil {
		return err
	}
	defer tgz.Close()

	filename := *version + "." + b.String() + ".zip"
	f, err := os.Create(filename)
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

	b.logf("Wrote %q.", filename)
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
func (b *Build) fetchFile(client *buildlet.Client, dest, dir string) error {
	b.logf("Downloading file from %q.", dir)
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
	return b.writeFile(dest, tr)
}

func (b *Build) writeFile(name string, r io.Reader) error {
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
	b.logf("Wrote %q.", name)
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

// versionIncludesTour reports whether the provided Go version (of the
// form "go1.N" or "go1.N.M" includes the Go tour binary.
func versionIncludesTour(goVer string) bool {
	// We don't do releases of Go 1.9 and earlier, so this only
	// needs to recognize the two current past releases. From Go
	// 1.12 and on, we won't ship the tour binary (see CL 131156).
	return strings.HasPrefix(goVer, "go1.10.") ||
		strings.HasPrefix(goVer, "go1.11.")
}
