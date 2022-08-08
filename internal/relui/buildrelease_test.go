// Copyright 2022 Go Authors All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-github/github"
	"github.com/google/uuid"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/untar"
	"golang.org/x/build/internal/workflow"
)

func TestRelease(t *testing.T) {
	t.Run("beta", func(t *testing.T) {
		testRelease(t, "go1.18beta1", task.KindBeta)
	})
	t.Run("rc", func(t *testing.T) {
		testRelease(t, "go1.18rc1", task.KindRC)
	})
}

func TestSecurity(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		testSecurity(t, true)
	})
	t.Run("failure", func(t *testing.T) {
		testSecurity(t, false)
	})
}

type releaseTestDeps struct {
	ctx            context.Context
	buildlets      *fakeBuildlets
	gerrit         *fakeGerrit
	versionTasks   *task.VersionTasks
	buildTasks     *BuildReleaseTasks
	milestoneTasks *task.MilestoneTasks
	publishedFiles map[string]*WebsiteFile
	outputListener func(taskName string, output interface{})
}

func newReleaseTestDeps(t *testing.T, wantVersion string) *releaseTestDeps {
	task.AwaitDivisor = 100
	t.Cleanup(func() { task.AwaitDivisor = 1 })
	ctx, cancel := context.WithCancel(context.Background())
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}

	// Set up a server that will be used to serve inputs to the build.
	bootstrapServer := httptest.NewServer(http.HandlerFunc(serveBootstrap))
	t.Cleanup(bootstrapServer.Close)
	fakeBuildlets := &fakeBuildlets{
		t:       t,
		dir:     t.TempDir(),
		httpURL: bootstrapServer.URL,
		logs:    map[string][]*[]string{},
	}

	// Set up the fake signing process.
	scratchDir := t.TempDir()
	argRe := regexp.MustCompile(`--relui_staging="(.*?)"`)
	outputListener := func(taskName string, output interface{}) {
		if taskName != "Start signing command" {
			return
		}
		matches := argRe.FindStringSubmatch(output.(string))
		if matches == nil {
			return
		}
		u, err := url.Parse(matches[1])
		if err != nil {
			t.Fatal(err)
		}
		go fakeSign(ctx, t, u.Path)
	}

	// Set up the fake CDN publishing process.
	servingDir := t.TempDir()
	dlDir := t.TempDir()
	dlServer := httptest.NewServer(http.FileServer(http.FS(os.DirFS(dlDir))))
	t.Cleanup(dlServer.Close)
	go fakeCDNLoad(ctx, t, servingDir, dlDir)

	// Set up the fake website to publish to.
	var filesMu sync.Mutex
	files := map[string]*WebsiteFile{}
	publishFile := func(f *WebsiteFile) error {
		filesMu.Lock()
		defer filesMu.Unlock()
		files[strings.TrimPrefix(f.Filename, wantVersion+".")] = f
		return nil
	}

	gerrit := &fakeGerrit{createdTags: map[string]string{}}
	versionTasks := &task.VersionTasks{
		Gerrit:    gerrit,
		GoProject: "go",
	}
	milestoneTasks := &task.MilestoneTasks{
		Client:    &fakeGitHub{},
		RepoOwner: "golang",
		RepoName:  "go",
	}

	snapshotServer := httptest.NewServer(http.HandlerFunc(serveSnapshot))
	t.Cleanup(snapshotServer.Close)
	buildTasks := &BuildReleaseTasks{
		GerritHTTPClient: http.DefaultClient,
		GerritURL:        snapshotServer.URL,
		GCSClient:        nil,
		ScratchURL:       "file://" + filepath.ToSlash(scratchDir),
		ServingURL:       "file://" + filepath.ToSlash(servingDir),
		CreateBuildlet:   fakeBuildlets.createBuildlet,
		DownloadURL:      dlServer.URL,
		PublishFile:      publishFile,
		ApproveAction: func(ctx *workflow.TaskContext) error {
			if strings.Contains(ctx.TaskName, "Release Coordinator Approval") {
				return nil
			}
			return fmt.Errorf("unexpected approval for %q", ctx.TaskName)
		},
	}
	// Cleanups are called in reverse order, and we need to cancel the context
	// before the temp dirs are deleted.
	t.Cleanup(cancel)
	return &releaseTestDeps{
		ctx:            ctx,
		buildlets:      fakeBuildlets,
		gerrit:         gerrit,
		versionTasks:   versionTasks,
		buildTasks:     buildTasks,
		milestoneTasks: milestoneTasks,
		publishedFiles: files,
		outputListener: outputListener,
	}
}

func testRelease(t *testing.T, wantVersion string, kind task.ReleaseKind) {
	deps := newReleaseTestDeps(t, wantVersion)
	wd := workflow.New()
	v, err := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, 18, kind)
	if err != nil {
		t.Fatal(err)
	}
	workflow.Output(wd, "Published Go version", v)

	w, err := workflow.Start(wd, map[string]interface{}{
		"Targets to skip testing (or 'all') (optional)":            []string{"js-wasm"},
		"Ref from the private repository to build from (optional)": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Run(deps.ctx, &verboseListener{t, deps.outputListener})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range deps.publishedFiles {
		if f.ChecksumSHA256 == "" || f.Size < 1 || f.Filename == "" || f.Kind == "" {
			t.Errorf("release process produced an invalid artifact: %#v", f)
		}
	}

	dlURL, files := deps.buildTasks.DownloadURL, deps.publishedFiles
	checkTGZ(t, dlURL, files, "src.tar.gz", &WebsiteFile{
		OS:   "",
		Arch: "",
		Kind: "source",
	}, map[string]string{
		"go/VERSION":       wantVersion,
		"go/src/make.bash": makeScript,
	})
	checkContents(t, dlURL, files, "windows-amd64.msi", &WebsiteFile{
		OS:   "windows",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm an MSI!\n")
	checkTGZ(t, dlURL, files, "linux-amd64.tar.gz", &WebsiteFile{
		OS:   "linux",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        wantVersion,
		"go/tool/something_orother/compile": "",
		"go/pkg/something_orother/race.a":   "",
	})
	checkZip(t, dlURL, files, "windows-arm64.zip", &WebsiteFile{
		OS:   "windows",
		Arch: "arm64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        wantVersion,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlURL, files, "linux-armv6l.tar.gz", &WebsiteFile{
		OS:   "linux",
		Arch: "armv6l",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        wantVersion,
		"go/tool/something_orother/compile": "",
	})
	checkContents(t, dlURL, files, "darwin-amd64.pkg", &WebsiteFile{
		OS:   "darwin",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm a .pkg!\n")

	wantCLs := 2 // VERSION bump, DL
	if kind == task.KindBeta {
		wantCLs--
	}
	if deps.gerrit.changesCreated != wantCLs {
		t.Errorf("workflow sent %v changes to Gerrit, want %v", deps.gerrit.changesCreated, wantCLs)
	}

	if len(deps.gerrit.createdTags) != 1 {
		t.Errorf("workflow created %v tags, want 1", deps.gerrit.createdTags)
	}

	// TODO: consider logging this to golden files?
	for name, logs := range deps.buildlets.logs {
		t.Logf("%v buildlets:", name)
		for _, group := range logs {
			for _, line := range *group {
				t.Log(line)
			}
		}
	}
}

func testSecurity(t *testing.T, mergeFixes bool) {
	deps := newReleaseTestDeps(t, "go1.18rc1")

	// Set up the fake merge process. Once we stop to ask for approval, switch
	// the public Gerrit server to serve the same content as the private server.
	approved := false
	privateServer := httptest.NewServer(http.HandlerFunc(serveSecureSnapshot))
	t.Cleanup(privateServer.Close)
	deps.buildTasks.PrivateGerritURL = privateServer.URL

	publicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if approved && mergeFixes {
			serveSecureSnapshot(w, r)
		} else {
			serveSnapshot(w, r)
		}
	}))
	t.Cleanup(publicServer.Close)
	deps.buildTasks.GerritURL = publicServer.URL

	defaultApprove := deps.buildTasks.ApproveAction
	deps.buildTasks.ApproveAction = func(tc *workflow.TaskContext) error {
		approved = true
		return defaultApprove(tc)
	}

	// Run the release.
	wd := workflow.New()
	v, err := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, 18, task.KindRC)
	if err != nil {
		t.Fatal(err)
	}
	workflow.Output(wd, "Published Go version", v)

	w, err := workflow.Start(wd, map[string]interface{}{
		"Targets to skip testing (or 'all') (optional)":            []string{"js-wasm"},
		"Ref from the private repository to build from (optional)": "security-ref",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Run(deps.ctx, &verboseListener{t, deps.outputListener})
	if mergeFixes && err != nil {
		t.Fatal(err)
	}
	if !mergeFixes {
		if err == nil {
			t.Fatal("release succeeded without merging fixes to the public repository")
		}
		return
	}
	checkTGZ(t, deps.buildTasks.DownloadURL, deps.publishedFiles, "src.tar.gz", &WebsiteFile{
		OS:   "",
		Arch: "",
		Kind: "source",
	}, map[string]string{
		"go/security.txt": "This file makes us secure",
	})
}

func TestAdvisoryTrybotFail(t *testing.T) {
	deps := newReleaseTestDeps(t, "go1.18rc1")
	defaultApprove := deps.buildTasks.ApproveAction
	approvedTrybots := false
	deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
		if strings.Contains(ctx.TaskName, "TryBot failures") {
			approvedTrybots = true
			return nil
		}
		return defaultApprove(ctx)
	}

	// Run the release.
	wd := workflow.New()
	v, err := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, 18, task.KindRC)
	if err != nil {
		t.Fatal(err)
	}
	workflow.Output(wd, "Published Go version", v)

	w, err := workflow.Start(wd, map[string]interface{}{
		"Targets to skip testing (or 'all') (optional)":            []string(nil),
		"Ref from the private repository to build from (optional)": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(deps.ctx, &verboseListener{t, deps.outputListener}); err != nil {
		t.Fatal(err)
	}
	if !approvedTrybots {
		t.Errorf("advisory trybots didn't need approval")
	}

}

// makeScript pretends to be make.bash. It creates a fake go command that
// knows how to fake the commands the release process runs.
const makeScript = `#!/bin/bash

GO=../
mkdir -p $GO/bin

cat <<'EOF' >$GO/bin/go
#!/bin/bash -eu
case "$1 $2" in
"run releaselet.go")
    # We're building an MSI. The command should be run in the gomote work dir.
	ls go/src/make.bash >/dev/null
	mkdir msi
	echo "I'm an MSI!" > msi/thisisanmsi.msi
	;;
"install -race")
	# Installing the race mode stdlib. Doesn't matter where it's run.
	mkdir -p $(dirname $0)/../pkg/something_orother/
	touch $(dirname $0)/../pkg/something_orother/race.a
	;;
*)
	echo "unexpected command $@"
	exit 1
	;;
esac
EOF
chmod 0755 $GO/bin/go

cp $GO/bin/go $GO/bin/go.exe
# We don't know what GOOS_GOARCH we're "building" for, write some junk for
# versimilitude.
mkdir -p $GO/tool/something_orother/
touch $GO/tool/something_orother/compile
`

// allScript pretends to be all.bash. It is hardcoded to pass.
const allScript = `#!/bin/bash -eu

echo "I'm a test! :D"

if [[ $GO_BUILDER_NAME =~ "js-wasm" ]]; then
  echo "Oh no, WASM is broken"
  exit 1
fi

exit 0
`

func serveBootstrap(w http.ResponseWriter, r *http.Request) {
	serveTarball("go-builder-data/go", map[string]string{
		"bin/go": "I'm a dummy bootstrap go command!",
	}, w, r)
}

func serveSnapshot(w http.ResponseWriter, r *http.Request) {
	serveTarball("+archive", map[string]string{
		"src/make.bash": makeScript,
		"src/make.bat":  makeScript,
		"src/all.bash":  allScript,
		"src/all.bat":   allScript,
		"src/race.bash": allScript,
		"src/race.bat":  allScript,
	}, w, r)
}

func serveSecureSnapshot(w http.ResponseWriter, r *http.Request) {
	serveTarball("+archive", map[string]string{
		"src/make.bash": makeScript,
		"src/make.bat":  makeScript,
		"src/all.bash":  allScript,
		"src/all.bat":   allScript,
		"src/race.bash": allScript,
		"src/race.bat":  allScript,
		"security.txt":  "This file makes us secure",
	}, w, r)
}

// serveTarballs serves the files the release process relies on.
// PutTarFromURL is hardcoded to read from this server.
func serveTarball(pathMatch string, files map[string]string, w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, pathMatch) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	gzw := gzip.NewWriter(w)
	tw := tar.NewWriter(gzw)

	for name, contents := range files {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Size:     int64(len(contents)),
			Mode:     0777,
		}); err != nil {
			panic(err)
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			panic(err)
		}
	}

	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gzw.Close(); err != nil {
		panic(err)
	}
}

func checkFile(t *testing.T, dlURL string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, check func([]byte)) {
	t.Run(filename, func(t *testing.T) {
		f, ok := files[filename]
		if !ok {
			t.Fatalf("file %q not published", filename)
		}
		if diff := cmp.Diff(meta, f, cmpopts.IgnoreFields(WebsiteFile{}, "Filename", "Version", "ChecksumSHA256", "Size")); diff != "" {
			t.Errorf("file metadata mismatch (-want +got):\n%v", diff)
		}
		resp, err := http.Get(dlURL + "/" + f.Filename)
		if err != nil {
			t.Fatalf("getting %v: %v", f.Filename, err)
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("reading %v: %v", f.Filename, err)
		}
		check(body)
	})
}

func checkContents(t *testing.T, dlURL string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, contents string) {
	checkFile(t, dlURL, files, filename, meta, func(b []byte) {
		if got, want := string(b), contents; got != want {
			t.Errorf("%v contains %q, want %q", filename, got, want)
		}
	})
}

func checkTGZ(t *testing.T, dlURL string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, contents map[string]string) {
	checkFile(t, dlURL, files, filename, meta, func(b []byte) {
		gzr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gzr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			want, ok := contents[h.Name]
			if !ok {
				continue
			}
			b, err := ioutil.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			delete(contents, h.Name)
			if string(b) != want {
				t.Errorf("contents of %v were %q, want %q", h.Name, string(b), want)
			}
		}
		if len(contents) != 0 {
			t.Errorf("not all files were found: missing %v", contents)
		}
	})
}

func checkZip(t *testing.T, dlURL string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, contents map[string]string) {
	checkFile(t, dlURL, files, filename, meta, func(b []byte) {
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			want, ok := contents[f.Name]
			if !ok {
				continue
			}
			r, err := zr.Open(f.Name)
			if err != nil {
				t.Fatal(err)
			}
			b, err := ioutil.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			delete(contents, f.Name)
			if string(b) != want {
				t.Errorf("contents of %v were %q, want %q", f.Name, string(b), want)
			}
		}
		if len(contents) != 0 {
			t.Errorf("not all files were found: missing %v", contents)
		}
	})
}

type fakeGerrit struct {
	changesCreated int
	createdTags    map[string]string
}

func (g *fakeGerrit) CreateAutoSubmitChange(ctx context.Context, input gerrit.ChangeInput, contents map[string]string) (string, error) {
	g.changesCreated++
	return "fake~12345", nil
}

func (g *fakeGerrit) Submitted(ctx context.Context, changeID, baseCommit string) (string, bool, error) {
	return "fakehash", true, nil
}

func (g *fakeGerrit) ListTags(ctx context.Context, project string) ([]string, error) {
	return []string{"go1.17"}, nil
}

func (g *fakeGerrit) Tag(ctx context.Context, project, tag, commit string) error {
	g.createdTags[tag] = commit
	return nil
}

func (g *fakeGerrit) ReadBranchHead(ctx context.Context, project, branch string) (string, error) {
	return fmt.Sprintf("fake HEAD commit for %v/%v", project, branch), nil
}

type fakeGitHub struct {
}

func (g *fakeGitHub) FetchMilestone(ctx context.Context, owner, repo, name string, create bool) (int, error) {
	return 0, nil
}

func (g *fakeGitHub) Query(ctx context.Context, q interface{}, variables map[string]interface{}) error {
	return nil
}

func (g *fakeGitHub) EditIssue(ctx context.Context, owner string, repo string, number int, issue *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return nil, nil, nil
}

func (g *fakeGitHub) EditMilestone(ctx context.Context, owner string, repo string, number int, milestone *github.Milestone) (*github.Milestone, *github.Response, error) {
	return nil, nil, nil
}

type fakeBuildlets struct {
	t       *testing.T
	dir     string
	httpURL string

	mu     sync.Mutex
	nextID int
	logs   map[string][]*[]string
}

func (b *fakeBuildlets) createBuildlet(_ context.Context, kind string) (buildlet.RemoteClient, error) {
	b.mu.Lock()
	buildletDir := filepath.Join(b.dir, kind, fmt.Sprint(b.nextID))
	logs := &[]string{}
	b.nextID++
	b.logs[kind] = append(b.logs[kind], logs)
	b.mu.Unlock()
	logf := func(format string, args ...interface{}) {
		line := fmt.Sprintf(format, args...)
		line = strings.ReplaceAll(line, buildletDir, "$WORK")
		*logs = append(*logs, line)
	}
	logf("--- create buildlet ---")

	return &fakeBuildlet{
		t:       b.t,
		kind:    kind,
		dir:     buildletDir,
		httpURL: b.httpURL,
		logf:    logf,
	}, nil
}

type fakeBuildlet struct {
	buildlet.Client
	t       *testing.T
	kind    string
	dir     string
	httpURL string
	logf    func(string, ...interface{})
	closed  bool
}

func (b *fakeBuildlet) Close() error {
	if !b.closed {
		b.logf("--- destroy buildlet ---")
		b.closed = true
	}
	return nil
}

func (b *fakeBuildlet) Exec(ctx context.Context, cmd string, opts buildlet.ExecOpts) (remoteErr error, execErr error) {
	b.logf("exec %v %v\n\twd %q env %v", cmd, opts.Args, opts.Dir, opts.ExtraEnv)
	absCmd := filepath.Join(b.dir, cmd)
retry:
	c := exec.CommandContext(ctx, absCmd, opts.Args...)
	c.Env = append(os.Environ(), opts.ExtraEnv...)
	buf := &bytes.Buffer{}
	var w io.Writer = buf
	if opts.Output != nil {
		w = io.MultiWriter(w, opts.Output)
	}
	c.Stdout = w
	c.Stderr = w
	if opts.Dir == "" {
		c.Dir = filepath.Dir(absCmd)
	} else {
		c.Dir = filepath.Join(b.dir, opts.Dir)
	}
	err := c.Run()
	// Work around Unix foolishness. See go.dev/issue/22315.
	if err != nil && strings.Contains(err.Error(), "text file busy") {
		time.Sleep(100 * time.Millisecond)
		goto retry
	}
	if err != nil {
		return nil, fmt.Errorf("command %v %v failed: %v output: %q", cmd, opts.Args, err, buf.String())
	}
	return nil, nil
}

func (b *fakeBuildlet) GetTar(ctx context.Context, dir string) (io.ReadCloser, error) {
	b.logf("get tar of %q", dir)
	buf := &bytes.Buffer{}
	zw := gzip.NewWriter(buf)
	tw := tar.NewWriter(zw)
	base := filepath.Join(b.dir, filepath.FromSlash(dir))
	// Copied pretty much wholesale from buildlet.go.
	err := filepath.Walk(base, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(path, base)), "/")
		th, err := tar.FileInfoHeader(fi, path)
		if err != nil {
			return err
		}
		th.Name = rel
		if fi.IsDir() && !strings.HasSuffix(th.Name, "/") {
			th.Name += "/"
		}
		if th.Name == "/" {
			return nil
		}
		if err := tw.WriteHeader(th); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return ioutil.NopCloser(buf), nil
}

func (b *fakeBuildlet) ListDir(ctx context.Context, dir string, opts buildlet.ListDirOpts, fn func(buildlet.DirEntry)) error {
	// We call this when something goes wrong, so we need it to "succeed".
	// It's not worth implementing; return some nonsense.
	fn(buildlet.DirEntry{
		Line: "ListDir is silently unimplemented, sorry",
	})
	return nil
}

func (b *fakeBuildlet) Put(ctx context.Context, r io.Reader, path string, mode os.FileMode) error {
	b.logf("write file %q with mode %0o", path, mode)
	f, err := os.OpenFile(filepath.Join(b.dir, path), os.O_CREATE|os.O_RDWR, mode)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Close()
}

func (b *fakeBuildlet) PutTar(ctx context.Context, r io.Reader, dir string) error {
	b.logf("put tar to %q", dir)
	return untar.Untar(r, filepath.Join(b.dir, dir))
}

func (b *fakeBuildlet) PutTarFromURL(ctx context.Context, tarURL string, dir string) error {
	b.logf("put tar from %v to %q", tarURL, dir)
	u, err := url.Parse(tarURL)
	if err != nil {
		return err
	}
	resp, err := http.Get(b.httpURL + u.Path)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status for %q: %v", tarURL, resp.Status)
	}
	defer resp.Body.Close()
	return untar.Untar(resp.Body, filepath.Join(b.dir, dir))
}

func (b *fakeBuildlet) WorkDir(ctx context.Context) (string, error) {
	return b.dir, nil
}

type verboseListener struct {
	t              *testing.T
	outputListener func(string, interface{})
}

func (l *verboseListener) TaskStateChanged(_ uuid.UUID, _ string, st *workflow.TaskState) error {
	switch {
	case !st.Finished:
		l.t.Logf("task %-10v: started", st.Name)
	case st.Error != "":
		l.t.Logf("task %-10v: error: %v", st.Name, st.Error)
	default:
		l.t.Logf("task %-10v: done: %v", st.Name, st.Result)
		if l.outputListener != nil {
			l.outputListener(st.Name, st.Result)
		}
	}
	return nil
}

func (l *verboseListener) Logger(_ uuid.UUID, task string) workflow.Logger {
	return &testLogger{t: l.t, task: task}
}

type testLogger struct {
	t    *testing.T
	task string
}

func (l *testLogger) Printf(format string, v ...interface{}) {
	l.t.Logf("task %-10v: LOG: %s", l.task, fmt.Sprintf(format, v...))
}

// fakeSign acts like a human running the signbinaries job periodically.
func fakeSign(ctx context.Context, t *testing.T, dir string) {
	seen := map[string]bool{}
	periodicallyDo(ctx, t, 100*time.Millisecond, func() error {
		return fakeSignOnce(t, dir, seen)
	})
}

func fakeSignOnce(t *testing.T, dir string, seen map[string]bool) error {
	_, err := os.Stat(filepath.Join(dir, "ready"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	contents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, fi := range contents {
		fn := fi.Name()
		if fn == "signed" || seen[fn] {
			continue
		}
		var copy, gpgSign, makePkg bool
		hasSuffix := func(suffix string) bool { return strings.HasSuffix(fn, suffix) }
		switch {
		case strings.Contains(fn, "darwin") && hasSuffix(".tar.gz"):
			copy = true
			gpgSign = true
			makePkg = true
		case strings.Contains(fn, "darwin") && hasSuffix(".pkg"):
			copy = true
		case hasSuffix(".tar.gz"):
			gpgSign = true
		case hasSuffix("msi"):
			copy = true
		}

		if err := os.MkdirAll(filepath.Join(dir, "signed"), 0777); err != nil {
			t.Fatal(err)
		}

		writeSignedWithHash := func(filename string, contents []byte) error {
			path := filepath.Join(dir, "signed", filename)
			if err := ioutil.WriteFile(path, contents, 0777); err != nil {
				return err
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(contents))
			if err := ioutil.WriteFile(path+".sha256", []byte(hash), 0777); err != nil {
				return err
			}
			return nil
		}

		if copy {
			bytes, err := ioutil.ReadFile(filepath.Join(dir, fn))
			if err != nil {
				return err
			}
			if err := writeSignedWithHash(fn, bytes); err != nil {
				return err
			}
		}
		if makePkg {
			if err := writeSignedWithHash(strings.ReplaceAll(fn, ".tar.gz", ".pkg"), []byte("I'm a .pkg!\n")); err != nil {
				return err
			}
		}
		if gpgSign {
			if err := writeSignedWithHash(fn+".asc", []byte("gpg signature")); err != nil {
				return err
			}
		}
		seen[fn] = true
	}
	return nil
}

// These are the files created by the Go 1.18 release.
const inputs = `
go1.18.darwin-amd64.tar.gz
go1.18.darwin-arm64.tar.gz
go1.18.freebsd-386.tar.gz
go1.18.freebsd-amd64.tar.gz
go1.18.linux-386.tar.gz
go1.18.linux-amd64.tar.gz
go1.18.linux-arm64.tar.gz
go1.18.linux-armv6l.tar.gz
go1.18.linux-ppc64le.tar.gz
go1.18.linux-s390x.tar.gz
go1.18.src.tar.gz
go1.18.windows-386.msi
go1.18.windows-386.zip
go1.18.windows-amd64.msi
go1.18.windows-amd64.zip
go1.18.windows-arm64.msi
go1.18.windows-arm64.zip
`

// These are the files created in the "signed" folder by the signing run for Go 1.18.
const outputs = `
go1.18.darwin-amd64.pkg
go1.18.darwin-amd64.pkg.sha256
go1.18.darwin-amd64.tar.gz
go1.18.darwin-amd64.tar.gz.asc
go1.18.darwin-amd64.tar.gz.asc.sha256
go1.18.darwin-amd64.tar.gz.sha256
go1.18.darwin-arm64.pkg
go1.18.darwin-arm64.pkg.sha256
go1.18.darwin-arm64.tar.gz
go1.18.darwin-arm64.tar.gz.asc
go1.18.darwin-arm64.tar.gz.asc.sha256
go1.18.darwin-arm64.tar.gz.sha256
go1.18.freebsd-386.tar.gz.asc
go1.18.freebsd-386.tar.gz.asc.sha256
go1.18.freebsd-amd64.tar.gz.asc
go1.18.freebsd-amd64.tar.gz.asc.sha256
go1.18.linux-386.tar.gz.asc
go1.18.linux-386.tar.gz.asc.sha256
go1.18.linux-amd64.tar.gz.asc
go1.18.linux-amd64.tar.gz.asc.sha256
go1.18.linux-arm64.tar.gz.asc
go1.18.linux-arm64.tar.gz.asc.sha256
go1.18.linux-armv6l.tar.gz.asc
go1.18.linux-armv6l.tar.gz.asc.sha256
go1.18.linux-ppc64le.tar.gz.asc
go1.18.linux-ppc64le.tar.gz.asc.sha256
go1.18.linux-s390x.tar.gz.asc
go1.18.linux-s390x.tar.gz.asc.sha256
go1.18.src.tar.gz.asc
go1.18.src.tar.gz.asc.sha256
go1.18.windows-386.msi
go1.18.windows-386.msi.sha256
go1.18.windows-amd64.msi
go1.18.windows-amd64.msi.sha256
go1.18.windows-arm64.msi
go1.18.windows-arm64.msi.sha256
`

func TestFakeSign(t *testing.T) {
	dir := t.TempDir()
	for _, f := range strings.Split(strings.TrimSpace(inputs), "\n") {
		if err := ioutil.WriteFile(filepath.Join(dir, f), []byte("hi"), 0777); err != nil {
			t.Fatal(err)
		}
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "ready"), nil, 0777); err != nil {
		t.Fatal(err)
	}
	fakeSignOnce(t, dir, map[string]bool{})
	want := map[string]bool{}
	for _, f := range strings.Split(strings.TrimSpace(outputs), "\n") {
		want[f] = true
	}
	got := map[string]bool{}
	files, err := ioutil.ReadDir(filepath.Join(dir, "signed"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		got[f.Name()] = true
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("signed outputs mismatch (-want +got):\n%v", diff)
	}
}

func fakeCDNLoad(ctx context.Context, t *testing.T, from, to string) {
	seen := map[string]bool{}
	periodicallyDo(ctx, t, 100*time.Millisecond, func() error {
		files, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, f := range files {
			if seen[f.Name()] {
				continue
			}
			seen[f.Name()] = true
			contents, err := os.ReadFile(filepath.Join(from, f.Name()))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(to, f.Name()), contents, 0777); err != nil {
				return err
			}
		}
		return nil
	})
}

func periodicallyDo(ctx context.Context, t *testing.T, period time.Duration, f func() error) {
	var err error
	childCtx, cancel := context.WithCancel(ctx)
	internal.PeriodicallyDo(childCtx, period, func(_ context.Context, _ time.Time) {
		err = f()
		if err != nil {
			cancel()
		}
	})
	// Suppress errors caused by the test finishing before we notice.
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
}
