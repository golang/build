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

func testRelease(t *testing.T, wantVersion string, kind task.ReleaseKind) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if runtime.GOOS != "linux" {
		t.Skip("Requires bash shell scripting support.")
	}

	// Set up a server that will be used to serve inputs to the build.
	tarballServer := httptest.NewServer(http.HandlerFunc(serveTarballs))
	defer tarballServer.Close()
	fakeBuildlets := &fakeBuildlets{
		t:       t,
		dir:     t.TempDir(),
		httpURL: tarballServer.URL,
		logs:    map[string][]*[]string{},
	}

	// Set up the fake signing process.
	stagingDir := t.TempDir()
	go fakeSign(ctx, t, filepath.Join(stagingDir, wantVersion))
	signingPollDuration = 100 * time.Millisecond

	// Set up the fake CDN publishing process.
	servingDir := t.TempDir()
	dlDir := t.TempDir()
	dlServer := httptest.NewServer(http.FileServer(http.FS(os.DirFS(dlDir))))
	defer dlServer.Close()
	go fakeCDNLoad(ctx, t, servingDir, dlDir)
	uploadPollDuration = 100 * time.Millisecond

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
	buildTasks := &BuildReleaseTasks{
		GerritURL:      tarballServer.URL,
		GCSClient:      nil,
		ScratchURL:     "file://" + filepath.ToSlash(t.TempDir()),
		StagingURL:     "file://" + filepath.ToSlash(stagingDir),
		ServingURL:     "file://" + filepath.ToSlash(servingDir),
		CreateBuildlet: fakeBuildlets.createBuildlet,
		DownloadURL:    dlServer.URL,
		PublishFile:    publishFile,
	}
	wd := workflow.New()
	if err := addSingleReleaseWorkflow(buildTasks, milestoneTasks, versionTasks, wd, "go1.18", kind); err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Start(wd, map[string]interface{}{
		"Targets to skip testing (or 'all') (optional)": []string(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Run(ctx, &verboseListener{t})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.ChecksumSHA256 == "" || f.Size < 1 || f.Filename == "" || f.Kind == "" {
			t.Errorf("release process produced an invalid artifact: %#v", f)
		}
	}

	checkTGZ(t, dlDir, files, "src.tar.gz", &WebsiteFile{
		OS:   "",
		Arch: "",
		Kind: "source",
	}, map[string]string{
		"go/VERSION":       wantVersion,
		"go/src/make.bash": makeScript,
	})
	checkContents(t, dlDir, files, "windows-amd64.msi", &WebsiteFile{
		OS:   "windows",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm an MSI!\n")
	checkTGZ(t, dlDir, files, "linux-amd64.tar.gz", &WebsiteFile{
		OS:   "linux",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        wantVersion,
		"go/tool/something_orother/compile": "",
		"go/pkg/something_orother/race.a":   "",
	})
	checkZip(t, dlDir, files, "windows-arm64.zip", &WebsiteFile{
		OS:   "windows",
		Arch: "arm64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        wantVersion,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlDir, files, "linux-armv6l.tar.gz", &WebsiteFile{
		OS:   "linux",
		Arch: "armv6l",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        wantVersion,
		"go/tool/something_orother/compile": "",
	})
	checkContents(t, dlDir, files, "darwin-amd64.pkg", &WebsiteFile{
		OS:   "darwin",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm a .pkg!\n")

	wantCLs := 2 // VERSION bump, DL
	if kind == task.KindBeta {
		wantCLs--
	}
	if gerrit.changesCreated != wantCLs {
		t.Errorf("workflow sent %v changes to Gerrit, want %v", gerrit.changesCreated, wantCLs)
	}

	if len(gerrit.createdTags) != 1 {
		t.Errorf("workflow created %v tags, want 1", gerrit.createdTags)
	}

	// TODO: consider logging this to golden files?
	for name, logs := range fakeBuildlets.logs {
		t.Logf("%v buildlets:", name)
		for _, group := range logs {
			for _, line := range *group {
				t.Log(line)
			}
		}
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
exit 0
`

// serveTarballs serves the files the release process relies on.
// PutTarFromURL is hardcoded to read from this server.
func serveTarballs(w http.ResponseWriter, r *http.Request) {
	gzw := gzip.NewWriter(w)
	tw := tar.NewWriter(gzw)
	writeFile := func(name, contents string) {
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

	switch {
	case strings.Contains(r.URL.Path, "+archive"):
		writeFile("src/make.bash", makeScript)
		writeFile("src/make.bat", makeScript)
		writeFile("src/all.bash", allScript)
		writeFile("src/all.bat", allScript)
	case strings.Contains(r.URL.Path, "go-builder-data/go"):
		writeFile("bin/go", "I'm a dummy bootstrap go command!")
	default:
		panic("unknown url requested: " + r.URL.String())
	}

	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gzw.Close(); err != nil {
		panic(err)
	}
}

func checkFile(t *testing.T, dlDir string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, check func([]byte)) {
	t.Run(filename, func(t *testing.T) {
		f, ok := files[filename]
		if !ok {
			t.Fatalf("file %q not published", filename)
		}
		if diff := cmp.Diff(meta, f, cmpopts.IgnoreFields(WebsiteFile{}, "Filename", "Version", "ChecksumSHA256", "Size")); diff != "" {
			t.Errorf("file metadata mismatch (-want +got):\n%v", diff)
		}
		b, err := ioutil.ReadFile(filepath.Join(dlDir, f.Filename))
		if err != nil {
			t.Fatalf("reading %v: %v", f.Filename, err)
		}
		check(b)
	})
}

func checkContents(t *testing.T, dlDir string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, contents string) {
	checkFile(t, dlDir, files, filename, meta, func(b []byte) {
		if got, want := string(b), contents; got != want {
			t.Errorf("%v contains %q, want %q", filename, got, want)
		}
	})
}

func checkTGZ(t *testing.T, dlDir string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, contents map[string]string) {
	checkFile(t, dlDir, files, filename, meta, func(b []byte) {
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

func checkZip(t *testing.T, dlDir string, files map[string]*WebsiteFile, filename string, meta *WebsiteFile, contents map[string]string) {
	checkFile(t, dlDir, files, filename, meta, func(b []byte) {
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

func (g *fakeGerrit) AwaitSubmit(ctx context.Context, changeID string) (string, error) {
	return "fakehash", nil
}

func (g *fakeGerrit) ListTags(ctx context.Context, project string) ([]string, error) {
	return []string{"go1.17"}, nil
}

func (g *fakeGerrit) Tag(ctx context.Context, project, tag, commit string) error {
	g.createdTags[tag] = commit
	return nil
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

func (b *fakeBuildlets) createBuildlet(kind string) (buildlet.Client, error) {
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

type verboseListener struct{ t *testing.T }

func (l *verboseListener) TaskStateChanged(_ uuid.UUID, _ string, st *workflow.TaskState) error {
	switch {
	case !st.Finished:
		l.t.Logf("task %-10v: started", st.Name)
	case st.Error != "":
		l.t.Logf("task %-10v: error: %v", st.Name, st.Error)
	default:
		l.t.Logf("task %-10v: done: %v", st.Name, st.Result)
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
	internal.PeriodicallyDo(ctx, 100*time.Millisecond, func(_ context.Context, _ time.Time) {
		fakeSignOnce(t, dir, seen)
	})
}

func fakeSignOnce(t *testing.T, dir string, seen map[string]bool) {
	contents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
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

		writeSignedWithHash := func(filename string, contents []byte) {
			path := filepath.Join(dir, "signed", filename)
			if err := ioutil.WriteFile(path, contents, 0777); err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(contents))
			if err := ioutil.WriteFile(path+".sha256", []byte(hash), 0777); err != nil {
				t.Fatal(err)
			}
		}

		if copy {
			bytes, err := ioutil.ReadFile(filepath.Join(dir, fn))
			if err != nil {
				t.Fatal(err)
			}
			writeSignedWithHash(fn, bytes)
		}
		if makePkg {
			writeSignedWithHash(strings.ReplaceAll(fn, ".tar.gz", ".pkg"), []byte("I'm a .pkg!\n"))
		}
		if gpgSign {
			writeSignedWithHash(fn+".asc", []byte("gpg signature"))
		}
		seen[fn] = true
	}
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
	internal.PeriodicallyDo(ctx, 100*time.Millisecond, func(_ context.Context, _ time.Time) {
		files, err := os.ReadDir(from)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if seen[f.Name()] {
				continue
			}
			seen[f.Name()] = true
			contents, err := os.ReadFile(filepath.Join(from, f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(to, f.Name()), contents, 0777); err != nil {
				t.Fatal(err)
			}
		}
	})
}
