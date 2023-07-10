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
	"fmt"
	"io"
	"io/ioutil"
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
	if opts.Path != nil {
		return nil, fmt.Errorf("opts.Path option is set, but fakeBuildlet doesn't support it")
	} else if opts.OnStartExec != nil {
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
	t       *testing.T
	name    string
	seq     int
	history []string // oldest to newest
	content map[string]map[string]string
	tags    map[string]string
}

func NewFakeRepo(t *testing.T, name string) *FakeRepo {
	return &FakeRepo{
		t:       t,
		name:    name,
		content: map[string]map[string]string{},
		tags:    map[string]string{},
	}
}

func (repo *FakeRepo) Commit(contents map[string]string) string {
	rev := fmt.Sprintf("%v~%v", repo.name, repo.seq)
	repo.seq++

	newContent := map[string]string{}
	if len(repo.history) != 0 {
		for k, v := range repo.content[repo.history[len(repo.history)-1]] {
			newContent[k] = v
		}
	}
	for k, v := range contents {
		newContent[k] = v
	}
	repo.content[rev] = newContent
	repo.history = append(repo.history, rev)
	return rev
}

func (repo *FakeRepo) Tag(tag, commit string) {
	if _, ok := repo.content[commit]; !ok {
		repo.t.Fatalf("commit %q does not exist on repo %q", commit, repo.name)
	}
	if _, ok := repo.tags[tag]; ok {
		repo.t.Fatalf("tag %q already exists on repo %q", commit, repo.name)
	}
	repo.tags[tag] = commit
}

// GetRepoContent returns the content of repo based on the value of commit:
// - commit is "master": return content of the most recent revision
// - commit is tag: return content of the repo associating with the commit that the tag maps to
// - commit is neither "master" or tag: return content of the repo associated with that commit
func (repo *FakeRepo) GetRepoContent(commit string) (map[string]string, error) {
	rev := commit
	if commit == "master" {
		l := len(repo.history)
		if l == 0 {
			return nil, fmt.Errorf("repo %v history is empty", repo.name)
		}
		rev = repo.history[l-1]
	} else if val, ok := repo.tags[commit]; ok {
		rev = val
	}
	return repo.content[rev], nil
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
	return repo.history[len(repo.history)-1], nil
}

func (g *FakeGerrit) ReadFile(ctx context.Context, project string, commit string, file string) ([]byte, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	repoContent, err := repo.GetRepoContent(commit)
	if err != nil {
		return nil, err
	}
	fileContent := repoContent[file]
	if fileContent == "" {
		return nil, fmt.Errorf("commit/file not found %v at %v: %w", file, commit, gerrit.ErrResourceNotExist)
	}
	return []byte(fileContent), nil
}

func (g *FakeGerrit) ListTags(ctx context.Context, project string) ([]string, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	var tags []string
	for k := range repo.tags {
		tags = append(tags, k)
	}
	return tags, nil
}

func (g *FakeGerrit) GetTag(ctx context.Context, project string, tag string) (gerrit.TagInfo, error) {
	repo, err := g.repo(project)
	if err != nil {
		return gerrit.TagInfo{}, err
	}
	if commit, ok := repo.tags[tag]; ok {
		return gerrit.TagInfo{Revision: commit}, nil
	} else {
		return gerrit.TagInfo{}, fmt.Errorf("tag not found: %w", gerrit.ErrResourceNotExist)
	}
}

func (g *FakeGerrit) CreateAutoSubmitChange(_ *wf.TaskContext, input gerrit.ChangeInput, reviewers []string, contents map[string]string) (string, error) {
	repo, err := g.repo(input.Project)
	if err != nil {
		return "", err
	}
	commit := repo.Commit(contents)
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
	result := map[string][]string{}
	for _, commit := range repo.history {
		result[commit] = []string{"master"}
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
	repoContent, err := repo.GetRepoContent(rev)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	ServeTarball("", repoContent, w, r)
}

func (g *FakeGerrit) QueryChanges(ctx context.Context, query string) ([]string, error) {
	return nil, nil
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

type FakeCloudBuild struct {
	Project       string
	AllowedBuilds map[string]map[string]string
}

const fakeBuildID = "build-12345"

func (cb *FakeCloudBuild) RunBuildTrigger(ctx context.Context, project string, trigger string, substitutions map[string]string) (string, error) {
	if project != cb.Project {
		return "", fmt.Errorf("unexpected project %v, want %v", project, cb.Project)
	}
	if allowedSubs, ok := cb.AllowedBuilds[trigger]; !ok || !reflect.DeepEqual(allowedSubs, substitutions) {
		return "", fmt.Errorf("unexpected trigger %v: got params %#v, want %#v", trigger, substitutions, allowedSubs)
	}
	return fakeBuildID, nil
}

func (cb *FakeCloudBuild) Completed(ctx context.Context, project string, id string) (string, bool, error) {
	if project != cb.Project || id != fakeBuildID {
		return "", false, fmt.Errorf("unexpected build project/id: got %v %v, want %v %v", project, id, cb.Project, fakeBuildID)
	}
	return "here's some build detail", true, nil
}
