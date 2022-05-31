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

	"github.com/google/uuid"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/internal/untar"
	"golang.org/x/build/internal/workflow"
)

func TestRelease(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Requires bash shell scripting support.")
	}
	s := httptest.NewServer(http.HandlerFunc(serveTarballs))
	defer s.Close()
	fakeBuildlets := &fakeBuildlets{
		t:       t,
		dir:     t.TempDir(),
		httpURL: s.URL,
		logs:    map[string][]*[]string{},
	}
	stagingDir := t.TempDir()
	tasks := BuildReleaseTasks{
		GerritURL:      s.URL,
		GCSClient:      nil,
		ScratchURL:     "file://" + filepath.ToSlash(t.TempDir()),
		StagingURL:     "file://" + filepath.ToSlash(stagingDir),
		CreateBuildlet: fakeBuildlets.createBuildlet,
	}
	wd, err := tasks.newBuildReleaseWorkflow("go1.18")
	if err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Start(wd, map[string]interface{}{
		"Revision": "0",
		"Version":  "go1.18releasetest1",
		"Targets to skip testing (or 'all') (optional)": []string(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Run(context.TODO(), &verboseListener{t})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := out["Staged artifacts"].([]artifact)
	byName := map[string]artifact{}
	for _, a := range artifacts {
		byName[a.filename] = a
	}

	checkTGZ(t, stagingDir, byName["go1.18releasetest1.src.tar.gz"], map[string]string{
		"go/VERSION":       "go1.18releasetest1",
		"go/src/make.bash": makeScript,
	})
	checkMSI(t, stagingDir, byName["go1.18releasetest1.windows-amd64.msi"])
	checkTGZ(t, stagingDir, byName["go1.18releasetest1.linux-amd64.tar.gz"], map[string]string{
		"go/VERSION":                        "go1.18releasetest1",
		"go/tool/something_orother/compile": "",
		"go/pkg/something_orother/race.a":   "",
	})
	checkZip(t, stagingDir, byName["go1.18releasetest1.windows-arm64.zip"], map[string]string{
		"go/VERSION":                        "go1.18releasetest1",
		"go/tool/something_orother/compile": "",
	})
	checkZip(t, stagingDir, byName["go1.18releasetest1.windows-386.zip"], map[string]string{
		"go/VERSION":                        "go1.18releasetest1",
		"go/tool/something_orother/compile": "",
	})

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

func checkMSI(t *testing.T, stagingDir string, a artifact) {
	t.Run(a.filename, func(t *testing.T) {
		b, err := ioutil.ReadFile(filepath.Join(stagingDir, a.stagingPath))
		if err != nil {
			t.Fatalf("reading %v: %v", a.filename, err)
		}
		if got, want := string(b), "I'm an MSI!\n"; got != want {
			t.Fatalf("%v contains %q, want %q", a.filename, got, want)
		}
	})
}

func checkTGZ(t *testing.T, stagingDir string, a artifact, contents map[string]string) {
	t.Run(a.filename, func(t *testing.T) {
		b, err := ioutil.ReadFile(filepath.Join(stagingDir, a.stagingPath))
		if err != nil {
			t.Fatalf("reading %v: %v", a.filename, err)
		}
		gzr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("unzipping %v: %v", a.filename, err)
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
			t.Fatalf("not all files were found: missing %v", contents)
		}
	})
}

func checkZip(t *testing.T, stagingDir string, a artifact, contents map[string]string) {
	t.Run(a.filename, func(t *testing.T) {
		b, err := ioutil.ReadFile(filepath.Join(stagingDir, a.stagingPath))
		if err != nil {
			t.Fatalf("reading %v: %v", a.filename, err)
		}
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
			t.Fatalf("not all files were found: missing %v", contents)
		}
	})
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
