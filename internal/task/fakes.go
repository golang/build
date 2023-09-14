// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/gcsfs"
	"golang.org/x/build/internal/relui/sign"
	"golang.org/x/build/internal/untar"
	wf "golang.org/x/build/internal/workflow"
)

// ServeTarball serves files as a .tar.gz to w, only if path contains pathMatch.
func ServeTarball(pathMatch string, files map[string]string, w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, pathMatch) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	tgz, err := mapToTgz(files)
	if err != nil {
		panic(err)
	}
	if _, err := w.Write(tgz); err != nil {
		panic(err)
	}
}

func mapToTgz(files map[string]string) ([]byte, error) {
	w := &bytes.Buffer{}
	gzw := gzip.NewWriter(w)
	tw := tar.NewWriter(gzw)

	for name, contents := range files {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag:   tar.TypeReg,
			Name:       name,
			Size:       int64(len(contents)),
			Mode:       0777,
			ModTime:    time.Now(),
			AccessTime: time.Now(),
			ChangeTime: time.Now(),
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gzw.Close(); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// NewFakeBuildlets creates a set of fake buildlets.
// httpServer is the base URL of form http://host with no trailing slash
// where PutTarFromURL downloads remote URLs from.
// sysCmds optionally allows overriding the named system commands
// during testing with the given executable content.
func NewFakeBuildlets(t *testing.T, httpServer string, sysCmds map[string]string) *FakeBuildlets {
	var sys map[string]string
	if len(sysCmds) != 0 {
		sys = make(map[string]string)
		sysDir := t.TempDir()
		for name, content := range sysCmds {
			if err := os.WriteFile(filepath.Join(sysDir, name), []byte(content), 0700); err != nil {
				t.Fatal(err)
			}
			sys[name] = filepath.Join(sysDir, name)
		}
	}
	return &FakeBuildlets{
		t:       t,
		dir:     t.TempDir(),
		sys:     sys,
		httpURL: httpServer,
		logs:    map[string][]*[]string{},
	}
}

type FakeBuildlets struct {
	t       *testing.T
	dir     string
	sys     map[string]string // System command name → absolute path.
	httpURL string

	mu     sync.Mutex
	nextID int
	logs   map[string][]*[]string
}

func (b *FakeBuildlets) CreateBuildlet(_ context.Context, kind string) (buildlet.RemoteClient, error) {
	b.mu.Lock()
	buildletDir := filepath.Join(b.dir, kind, fmt.Sprint(b.nextID), "work")
	if err := os.MkdirAll(buildletDir, 0700); err != nil {
		return nil, err
	}
	tempDir := filepath.Join(b.dir, kind, fmt.Sprint(b.nextID), "tmp")
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return nil, err
	}
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
		workDir: buildletDir,
		tempDir: tempDir,
		sys:     b.sys,
		httpURL: b.httpURL,
		logf:    logf,
	}, nil
}

func (b *FakeBuildlets) DumpLogs() {
	for name, logs := range b.logs {
		b.t.Logf("%v buildlets:", name)
		for _, group := range logs {
			for _, line := range *group {
				b.t.Log(line)
			}
		}
	}
}

type fakeBuildlet struct {
	buildlet.Client
	t       *testing.T
	kind    string
	workDir string
	tempDir string
	sys     map[string]string // System command name → absolute path.
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
	// TODO: add support for opts.Path. Previously, setting opts.Path would cause
	// an error here, but that caused unnecessary failures in tests that use mock
	// execution.
	if opts.OnStartExec != nil {
		return nil, fmt.Errorf("opts.OnStartExec option is set, but fakeBuildlet doesn't support it")
	}
	b.logf("exec %v %v\n\twd %q env %v", cmd, opts.Args, opts.Dir, opts.ExtraEnv)
	if absPath, ok := b.sys[cmd]; ok && opts.SystemLevel {
		cmd = absPath
	} else if !strings.HasPrefix(cmd, "/") && !opts.SystemLevel {
		cmd = filepath.Join(b.workDir, cmd)
	}
retry:
	c := exec.CommandContext(ctx, cmd, opts.Args...)
	c.Env = append(os.Environ(), opts.ExtraEnv...)
	c.Env = append(c.Env, "TEMP="+b.tempDir, "TMP="+b.tempDir, "TEMPDIR="+b.tempDir, "TMPDIR="+b.tempDir)
	buf := &bytes.Buffer{}
	var w io.Writer = buf
	if opts.Output != nil {
		w = io.MultiWriter(w, opts.Output)
	}
	c.Stdout = w
	c.Stderr = w
	if opts.Dir == "" && opts.SystemLevel {
		c.Dir = b.workDir
	} else if opts.Dir == "" && !opts.SystemLevel {
		c.Dir = filepath.Dir(cmd)
	} else {
		c.Dir = filepath.Join(b.workDir, opts.Dir)
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
	base := filepath.Join(b.workDir, filepath.FromSlash(dir))
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
	return io.NopCloser(buf), nil
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
	if err := os.MkdirAll(filepath.Dir(filepath.Join(b.workDir, path)), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(b.workDir, path), os.O_CREATE|os.O_RDWR, mode)
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
	return untar.Untar(r, filepath.Join(b.workDir, dir))
}

func (b *fakeBuildlet) PutTarFromURL(ctx context.Context, tarURL string, dir string) error {
	url, err := url.Parse(tarURL)
	if err != nil {
		return err
	}
	rewritten := url.String()
	if !strings.Contains(url.Host, "localhost") && !strings.Contains(url.Host, "127.0.0.1") {
		rewritten = b.httpURL + url.Path
	}
	b.logf("put tar from %v (rewritten to %v) to %q", tarURL, rewritten, dir)

	resp, err := http.Get(rewritten)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status for %q: %v", tarURL, resp.Status)
	}
	defer resp.Body.Close()
	return untar.Untar(resp.Body, filepath.Join(b.workDir, dir))
}

func (b *fakeBuildlet) WorkDir(ctx context.Context) (string, error) {
	return b.workDir, nil
}

func NewFakeGerrit(t *testing.T, repos ...*FakeRepo) *FakeGerrit {
	result := &FakeGerrit{
		repos: map[string]*FakeRepo{},
	}
	server := httptest.NewServer(http.HandlerFunc(result.serveHTTP))
	result.serverURL = server.URL
	t.Cleanup(server.Close)

	for _, r := range repos {
		result.repos[r.name] = r
	}
	return result
}

type FakeGerrit struct {
	repos     map[string]*FakeRepo
	serverURL string
}

type FakeRepo struct {
	t    *testing.T
	name string
	dir  *GitDir
}

func NewFakeRepo(t *testing.T, name string) *FakeRepo {
	if _, err := exec.LookPath("git"); errors.Is(err, exec.ErrNotFound) {
		t.Skip("test requires git")
	}

	r := &FakeRepo{
		t:    t,
		name: name,
		dir:  &GitDir{&Git{}, t.TempDir()},
	}
	t.Cleanup(func() { r.dir.Close() })
	r.runGit("init")
	r.runGit("commit", "--allow-empty", "--allow-empty-message", "-m", "")
	return r
}

// TODO(rfindley): probably every method on FakeRepo should invoke
// repo.t.Helper(), otherwise it's impossible to see where the test failed.

func (repo *FakeRepo) runGit(args ...string) []byte {
	repo.t.Helper()
	configArgs := []string{
		"-c", "init.defaultBranch=master",
		"-c", "user.email=relui@example.com",
		"-c", "user.name=relui",
	}
	out, err := repo.dir.RunCommand(context.Background(), append(configArgs, args...)...)
	if err != nil {
		repo.t.Fatalf("runGit(%v) failed: %v; output:\n%s", args, err, out)
	}
	return out
}

func (repo *FakeRepo) Commit(contents map[string]string) string {
	return repo.CommitOnBranch("master", contents)
}

func (repo *FakeRepo) CommitOnBranch(branch string, contents map[string]string) string {
	repo.runGit("switch", branch)
	for k, v := range contents {
		full := filepath.Join(repo.dir.dir, k)
		if err := os.MkdirAll(filepath.Dir(full), 0777); err != nil {
			repo.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(v), 0777); err != nil {
			repo.t.Fatal(err)
		}
	}
	repo.runGit("add", ".")
	repo.runGit("commit", "--allow-empty-message", "-m", "")
	return strings.TrimSpace(string(repo.runGit("rev-parse", "HEAD")))
}

func (repo *FakeRepo) History() []string {
	return strings.Split(string(repo.runGit("log", "--format=%H")), "\n")
}

func (repo *FakeRepo) Tag(tag, commit string) {
	repo.runGit("tag", tag, commit)
}

func (repo *FakeRepo) Branch(branch, commit string) {
	repo.runGit("branch", branch, commit)
}

func (repo *FakeRepo) ReadFile(commit, file string) ([]byte, error) {
	b, err := repo.dir.RunCommand(context.Background(), "show", commit+":"+file)
	if err != nil && strings.Contains(err.Error(), " does not exist ") {
		err = errors.Join(gerrit.ErrResourceNotExist, err)
	}
	return b, err
}

var _ GerritClient = (*FakeGerrit)(nil)

func (g *FakeGerrit) ListProjects(ctx context.Context) ([]string, error) {
	var names []string
	for k := range g.repos {
		names = append(names, k)
	}
	return names, nil
}

func (g *FakeGerrit) repo(name string) (*FakeRepo, error) {
	if r, ok := g.repos[name]; ok {
		return r, nil
	} else {
		return nil, fmt.Errorf("no such repo %v: %w", name, gerrit.ErrResourceNotExist)
	}
}

func (g *FakeGerrit) ReadBranchHead(ctx context.Context, project, branch string) (string, error) {
	repo, err := g.repo(project)
	if err != nil {
		return "", err
	}
	// TODO: If the branch doesn't exist, return an error matching gerrit.ErrResourceNotExist.
	out, err := repo.dir.RunCommand(ctx, "rev-parse", "refs/heads/"+branch)
	return strings.TrimSpace(string(out)), err
}

func (g *FakeGerrit) ReadFile(ctx context.Context, project string, commit string, file string) ([]byte, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	return repo.ReadFile(commit, file)
}

func (g *FakeGerrit) ListTags(ctx context.Context, project string) ([]string, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	out, err := repo.dir.RunCommand(ctx, "tag", "-l")
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil // No tags.
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}

func (g *FakeGerrit) GetTag(ctx context.Context, project string, tag string) (gerrit.TagInfo, error) {
	repo, err := g.repo(project)
	if err != nil {
		return gerrit.TagInfo{}, err
	}
	out, err := repo.dir.RunCommand(ctx, "rev-parse", "refs/tags/"+tag)
	return gerrit.TagInfo{Revision: strings.TrimSpace(string(out))}, err
}

func (g *FakeGerrit) CreateAutoSubmitChange(_ *wf.TaskContext, input gerrit.ChangeInput, reviewers []string, contents map[string]string) (string, error) {
	repo, err := g.repo(input.Project)
	if err != nil {
		return "", err
	}
	commit := repo.CommitOnBranch(input.Branch, contents)
	return "cl_" + commit, nil
}

func (g *FakeGerrit) Submitted(ctx context.Context, changeID, baseCommit string) (string, bool, error) {
	return strings.TrimPrefix(changeID, "cl_"), true, nil
}

func (g *FakeGerrit) Tag(ctx context.Context, project, tag, commit string) error {
	repo, err := g.repo(project)
	if err != nil {
		return err
	}
	repo.Tag(tag, commit)
	return nil
}

func (g *FakeGerrit) GetCommitsInRefs(ctx context.Context, project string, commits, refs []string) (map[string][]string, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	refSet := map[string]bool{}
	for _, ref := range refs {
		refSet[ref] = true
	}

	result := map[string][]string{}
	for _, commit := range commits {
		out, err := repo.dir.RunCommand(ctx, "branch", "--format=%(refname)", "--contains="+commit)
		if err != nil {
			return nil, err
		}
		for _, branch := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			branch := strings.TrimSpace(branch)
			if refSet[branch] {
				result[commit] = append(result[commit], branch)
			}
		}
	}
	return result, nil
}

func (g *FakeGerrit) GerritURL() string {
	return g.serverURL
}

func (g *FakeGerrit) serveHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 4 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	repo, err := g.repo(parts[1])
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rev := strings.TrimSuffix(parts[3], ".tar.gz")
	archive, err := repo.dir.RunCommand(r.Context(), "archive", "--format=tgz", rev)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, parts[3], time.Now(), bytes.NewReader(archive))
}

func (*FakeGerrit) QueryChanges(_ context.Context, query string) ([]*gerrit.ChangeInfo, error) {
	return nil, nil
}

func (*FakeGerrit) SetHashtags(_ context.Context, changeID string, _ gerrit.HashtagsInput) error {
	return fmt.Errorf("pretend that SetHashtags failed")
}

// NewFakeSignService returns a fake signing service that can sign PKGs, MSIs,
// and generate GPG signatures. MSIs are "signed" by adding a suffix to them.
// PKGs must actually be tarballs with a prefix of "I'm a PKG!\n". Any files
// they contain that look like binaries will be "signed".
func NewFakeSignService(t *testing.T) *FakeSignService {
	return &FakeSignService{
		t:             t,
		completedJobs: map[string][]string{},
	}
}

type FakeSignService struct {
	t             *testing.T
	mu            sync.Mutex
	completedJobs map[string][]string // Job ID → output objectURIs.
}

func (s *FakeSignService) SignArtifact(_ context.Context, bt sign.BuildType, in []string) (jobID string, _ error) {
	s.t.Logf("fakeSignService: doing %s signing of %q", bt, in)
	var out []string
	switch bt {
	case sign.BuildMacOS:
		if len(in) != 1 {
			return "", fmt.Errorf("got %d inputs, want 1", len(in))
		}
		out = []string{fakeSignPKG(in[0], fmt.Sprintf("-signed <%s>", bt))}
	case sign.BuildWindows:
		if len(in) != 1 {
			return "", fmt.Errorf("got %d inputs, want 1", len(in))
		}
		out = []string{fakeSignFile(in[0], fmt.Sprintf("-signed <%s>", bt))}
	case sign.BuildGPG:
		if len(in) == 0 {
			return "", fmt.Errorf("got 0 inputs, want 1 or more")
		}
		for _, f := range in {
			out = append(out, fakeGPGFile(f))
		}
	default:
		return "", fmt.Errorf("SignArtifact: not implemented for %v", bt)
	}
	jobID = uuid.NewString()
	s.mu.Lock()
	s.completedJobs[jobID] = out
	s.mu.Unlock()
	return jobID, nil
}

func (s *FakeSignService) ArtifactSigningStatus(_ context.Context, jobID string) (_ sign.Status, desc string, out []string, _ error) {
	s.mu.Lock()
	out, ok := s.completedJobs[jobID]
	s.mu.Unlock()
	if !ok {
		return sign.StatusNotFound, fmt.Sprintf("job %q not found", jobID), nil, nil
	}
	return sign.StatusCompleted, "", out, nil
}

func (s *FakeSignService) CancelSigning(_ context.Context, jobID string) error {
	s.t.Errorf("CancelSigning was called unexpectedly")
	return fmt.Errorf("intentional fake error")
}

func fakeSignPKG(f, msg string) string {
	b, err := os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeSignPKG: os.ReadFile: %v", err))
	}
	b = bytes.TrimPrefix(b, []byte("I'm a PKG!\n"))
	files, err := tgzToMap(bytes.NewReader(b))
	if err != nil {
		panic(fmt.Errorf("fakeSignPKG: tgzToMap: %v", err))
	}
	for fn, contents := range files {
		if !strings.Contains(fn, "go/bin") && !strings.Contains(fn, "go/pkg/tool") {
			continue
		}
		files[fn] = contents + msg
	}
	b, err = mapToTgz(files)
	if err != nil {
		panic(fmt.Errorf("fakeSignPKG: mapToTgz: %v", err))
	}
	b = append([]byte("I'm a PKG! "+msg+"\n"), b...)
	err = os.WriteFile(strings.TrimPrefix(f, "file://")+".signed", b, 0600)
	if err != nil {
		panic(fmt.Errorf("fakeSignPKG: os.WriteFile: %v", err))
	}
	return f + ".signed"
}

func tgzToMap(r io.Reader) (map[string]string, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gzr.Close()

	result := map[string]string{}
	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		result[h.Name] = string(b)
	}
	return result, nil
}

func fakeSignFile(f, msg string) string {
	b, err := os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeSignFile: os.ReadFile: %v", err))
	}
	b = append(b, []byte(msg)...)
	err = os.WriteFile(strings.TrimPrefix(f, "file://")+".signed", b, 0600)
	if err != nil {
		panic(fmt.Errorf("fakeSignFile: os.WriteFile: %v", err))
	}
	return f + ".signed"
}

func fakeGPGFile(f string) string {
	b, err := os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeGPGFile: os.ReadFile: %v", err))
	}
	gpg := fmt.Sprintf("I'm a GPG signature for %x!", sha256.Sum256(b))
	err = os.WriteFile(strings.TrimPrefix(f, "file://")+".asc", []byte(gpg), 0600)
	if err != nil {
		panic(fmt.Errorf("fakeGPGFile: os.WriteFile: %v", err))
	}
	return f + ".asc"
}

var _ CloudBuildClient = (*FakeCloudBuild)(nil)

func NewFakeCloudBuild(t *testing.T, gerrit *FakeGerrit, project string, allowedTriggers map[string]map[string]string, fakeGo string) *FakeCloudBuild {
	goDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(goDir, "go"), []byte(fakeGo), 0777); err != nil {
		t.Fatal(err)
	}
	return &FakeCloudBuild{
		t:               t,
		gerrit:          gerrit,
		project:         project,
		allowedTriggers: allowedTriggers,
		goDir:           goDir,
		results:         map[string]error{},
	}
}

type FakeCloudBuild struct {
	t               *testing.T
	gerrit          *FakeGerrit
	project         string
	allowedTriggers map[string]map[string]string
	goDir           string

	mu      sync.Mutex
	results map[string]error
}

func (cb *FakeCloudBuild) RunBuildTrigger(ctx context.Context, project string, trigger string, substitutions map[string]string) (CloudBuild, error) {
	if project != cb.project {
		return CloudBuild{}, fmt.Errorf("unexpected project %v, want %v", project, cb.project)
	}
	if allowedSubs, ok := cb.allowedTriggers[trigger]; !ok || !reflect.DeepEqual(allowedSubs, substitutions) {
		return CloudBuild{}, fmt.Errorf("unexpected trigger %v: got params %#v, want %#v", trigger, substitutions, allowedSubs)
	}
	id := fmt.Sprintf("build-%v", rand.Int63())
	cb.mu.Lock()
	cb.results[id] = nil
	cb.mu.Unlock()
	return CloudBuild{Project: project, ID: id}, nil
}

func (cb *FakeCloudBuild) Completed(ctx context.Context, build CloudBuild) (string, bool, error) {
	if build.Project != cb.project {
		return "", false, fmt.Errorf("unexpected build project: got %q, want %q", build.Project, cb.project)
	}
	cb.mu.Lock()
	result, ok := cb.results[build.ID]
	cb.mu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("unknown build ID %q", build.ID)
	}
	return "here's some build detail", true, result
}

func (c *FakeCloudBuild) ResultFS(ctx context.Context, build CloudBuild) (fs.FS, error) {
	return gcsfs.FromURL(ctx, nil, build.ResultURL)
}

func (cb *FakeCloudBuild) RunScript(ctx context.Context, script string, gerritProject string, outputs []string) (CloudBuild, error) {
	var wd string
	if gerritProject != "" {
		repo, err := cb.gerrit.repo(gerritProject)
		if err != nil {
			return CloudBuild{}, err
		}
		dir, err := (&Git{}).Clone(ctx, repo.dir.dir)
		defer dir.Close()
		wd = dir.dir
	} else {
		wd = cb.t.TempDir()
	}

	cmd := exec.Command("bash", "-eux")
	cmd.Stdin = strings.NewReader(script)
	cmd.Dir = wd
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PATH=/bin:/usr/bin:"+cb.goDir)
	// redirect stdout/err
	_, runErr := cmd.Output()

	id := fmt.Sprintf("build-%v", rand.Int63())
	resultDir := cb.t.TempDir()
	if runErr == nil {
		for _, out := range outputs {
			target := filepath.Join(resultDir, out)
			os.MkdirAll(filepath.Dir(target), 0777)
			if err := os.Rename(filepath.Join(wd, out), target); err != nil {
				runErr = fmt.Errorf("collecting outputs: %v", err)
				break
			}
		}
	}
	cb.mu.Lock()
	cb.results[id] = runErr
	cb.mu.Unlock()
	return CloudBuild{Project: cb.project, ID: id, ResultURL: "file://" + resultDir}, nil
}
