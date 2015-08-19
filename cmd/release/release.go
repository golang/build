// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TODO(adg): add flag so that we can choose to run make.bash only

// Command release builds a Go release.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	gobuild "go/build"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/build"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
)

var (
	target = flag.String("target", "", "If specified, build specific target platform ('linux-amd64')")

	rev       = flag.String("rev", "", "Go revision to build")
	toolsRev  = flag.String("tools", "", "Tools revision to build")
	tourRev   = flag.String("tour", "master", "Tour revision to include")
	blogRev   = flag.String("blog", "master", "Blog revision to include")
	netRev    = flag.String("net", "master", "Net revision to include")
	version   = flag.String("version", "", "Version string (go1.5.2)")
	coordAddr = flag.String("coordinator", "", "Coordinator instance address (default is production)")
	user      = flag.String("user", username(), "coordinator username, appended to 'user-'")

	uploadMode = flag.Bool("upload", false, "Upload files (exclusive to all other flags)")
)

var coordClient *buildlet.CoordinatorClient

func main() {
	flag.Parse()

	if *uploadMode {
		if err := upload(flag.Args()); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := findReleaselet(); err != nil {
		log.Fatalf("couldn't find releaselet source: %v", err)
	}

	if *rev == "" {
		log.Fatal("must specify -rev flag")
	}
	if *toolsRev == "" {
		log.Fatal("must specify -tools flag")
	}
	if *version == "" {
		log.Fatal("must specify -version flag")
	}

	coordClient = coordinatorClient()

	var wg sync.WaitGroup
	for _, b := range builds {
		b := b
		if *target != "" && b.String() != *target {
			continue
		}
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

	Race   bool // Build race detector.
	Static bool // Statically-link binaries.

	Builder string // Key for dashboard.Builders.
}

func (b *Build) String() string {
	if b.Source {
		return "src"
	}
	return fmt.Sprintf("%v-%v", b.OS, b.Arch)
}

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
		Arch:    "amd64",
		Race:    true,
		Static:  true,
		Builder: "linux-amd64",
	},
	{
		OS:      "freebsd",
		Arch:    "386",
		Builder: "freebsd-386-gce101",
	},
	{
		OS:      "freebsd",
		Arch:    "amd64",
		Race:    true,
		Static:  true,
		Builder: "freebsd-amd64-gce101",
	},
	{
		OS:      "windows",
		Arch:    "386",
		Builder: "windows-386-gce",
	},
	{
		OS:      "windows",
		Arch:    "amd64",
		Race:    true,
		Builder: "windows-amd64-gce",
	},
	{
		OS:      "darwin",
		Arch:    "amd64",
		Race:    true,
		Builder: "darwin-amd64-10_10",
	},
}

const (
	toolsRepo = "golang.org/x/tools"
	blogRepo  = "golang.org/x/blog"
	tourRepo  = "golang.org/x/tour"
)

var toolPaths = []string{
	"golang.org/x/tools/cmd/godoc",
	"golang.org/x/tour/gotour",
}

var preBuildCleanFiles = []string{
	".gitattributes",
	".gitignore",
	".hgignore",
	".hgtags",
	"misc/dashboard",
	"misc/makerelease",
}

var postBuildCleanFiles = []string{
	"VERSION.cache",
	"pkg/bootstrap",
}

func (b *Build) buildlet() (*buildlet.Client, error) {
	b.logf("Creating buildlet.")
	bc, err := coordClient.CreateBuildlet(b.Builder)
	if err != nil {
		return nil, err
	}
	bc.SetCloseFunc(func() error {
		return bc.Destroy()
	})
	return bc, nil
}

func (b *Build) make() error {
	bc, ok := dashboard.Builders[b.Builder]
	if !ok {
		return fmt.Errorf("unknown builder: %v", bc)
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

	// Push source to VM
	b.logf("Pushing source to VM.")
	const (
		goDir  = "go"
		goPath = "gopath"
		go14   = "go1.4"
	)
	for _, r := range []struct {
		repo, rev string
	}{
		{"go", *rev},
		{"tools", *toolsRev},
		{"blog", *blogRev},
		{"tour", *tourRev},
		{"net", *netRev},
	} {
		if b.Source && r.repo != "go" {
			continue
		}
		dir := goDir
		if r.repo != "go" {
			dir = goPath + "/src/golang.org/x/" + r.repo
		}
		tar := "https://go.googlesource.com/" + r.repo + "/+archive/" + r.rev + ".tar.gz"
		if err := client.PutTarFromURL(tar, dir); err != nil {
			return err
		}
	}

	if bc.Go14URL != "" && !b.Source {
		b.logf("Installing go1.4.")
		if err := client.PutTarFromURL(bc.Go14URL, go14); err != nil {
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
	if b.Static {
		env = append(env, "GO_DISTFLAGS=-s")
	}

	// Execute build
	b.logf("Building.")
	out := new(bytes.Buffer)
	all := filepath.Join(goDir, bc.AllScript())
	remoteErr, err := client.Exec(all, buildlet.ExecOpts{
		Output:   out,
		ExtraEnv: env,
		Args:     bc.AllScriptArgs(),
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
		remoteErr, err := client.Exec(goCmd, buildlet.ExecOpts{
			Output:   out,
			Dir:      ".", // root of buildlet work directory
			Args:     args,
			ExtraEnv: env,
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

		// Because on release branches, go install -a std is a NOP,
		// we have to resort to delete pkg/$GOOS_$GOARCH, install -race,
		// and then reinstall std so that we're not left with a slower,
		// race-enabled cmd/go, etc.
		if err := client.RemoveAll(path.Join(goDir, "pkg", b.OS+"_"+b.Arch)); err != nil {
			return err
		}
		if err := runGo("tool", "dist", "install", "runtime"); err != nil {
			return err
		}
		if err := runGo("install", "-race", "std"); err != nil {
			return err
		}
		if err := runGo("install", "std"); err != nil {
			return err
		}
		// Re-building go command leaves old versions of go.exe as go.exe~ on windows.
		// See (*builder).copyFile in $GOROOT/src/cmd/go/build.go for details.
		// Remove it manually.
		if b.OS == "windows" {
			if err := client.RemoveAll(goCmd + "~"); err != nil {
				return err
			}
		}
	}

	b.logf("Building %v.", strings.Join(toolPaths, ", "))
	if err := runGo(append([]string{"install"}, toolPaths...)...); err != nil {
		return err
	}

	b.logf("Cleaning goroot (post-build).")
	if err := client.RemoveAll(addPrefix(goDir, postBuildCleanFiles)...); err != nil {
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
		return err
	}

	cleanFiles := []string{"releaselet.go", goPath, go14}

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

	if b.OS == "windows" {
		return b.fetchZip(client)
	}
	return b.fetchTarball(client)
}

func (b *Build) fetchTarball(client *buildlet.Client) error {
	b.logf("Downloading tarball.")
	tgz, err := client.GetTar(".")
	if err != nil {
		return err
	}
	filename := *version + "." + b.String() + ".tar.gz"
	return b.writeFile(filename, tgz)
}

func (b *Build) fetchZip(client *buildlet.Client) error {
	b.logf("Downloading tarball and re-compressing as zip.")

	tgz, err := client.GetTar(".")
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
	tgz, err := client.GetTar(dir)
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
	b.logf("Wrote %q.", name)
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
	inst := build.ProdCoordinator
	if *coordAddr != "" {
		inst = build.CoordinatorInstance(*coordAddr)
	}
	return &buildlet.CoordinatorClient{
		Auth: buildlet.UserPass{
			Username: "user-" + *user,
			Password: userToken(),
		},
		Instance: inst,
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
