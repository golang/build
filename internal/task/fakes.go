package task

import (
	"archive/tar"
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
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/untar"
)

// ServeTarball serves files as a .tar.gz to w, only if path contains pathMatch.
func ServeTarball(pathMatch string, files map[string]string, w http.ResponseWriter, r *http.Request) {
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

func NewFakeBuildlets(t *testing.T, httpServer string) *FakeBuildlets {
	return &FakeBuildlets{
		t:       t,
		dir:     t.TempDir(),
		httpURL: httpServer,
		logs:    map[string][]*[]string{},
	}
}

type FakeBuildlets struct {
	t       *testing.T
	dir     string
	httpURL string

	mu     sync.Mutex
	nextID int
	logs   map[string][]*[]string
}

func (b *FakeBuildlets) CreateBuildlet(_ context.Context, kind string) (buildlet.RemoteClient, error) {
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
	if !strings.HasPrefix(cmd, "/") && !opts.SystemLevel {
		cmd = filepath.Join(b.dir, cmd)
	}
retry:
	c := exec.CommandContext(ctx, cmd, opts.Args...)
	c.Env = append(os.Environ(), opts.ExtraEnv...)
	buf := &bytes.Buffer{}
	var w io.Writer = buf
	if opts.Output != nil {
		w = io.MultiWriter(w, opts.Output)
	}
	c.Stdout = w
	c.Stderr = w
	if opts.Dir == "" {
		c.Dir = filepath.Dir(cmd)
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
	return untar.Untar(resp.Body, filepath.Join(b.dir, dir))
}

func (b *fakeBuildlet) WorkDir(ctx context.Context) (string, error) {
	return b.dir, nil
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
	content := repo.content[commit][file]
	if content == "" {
		return nil, fmt.Errorf("commit/file not found %v at %v: %w", file, commit, gerrit.ErrResourceNotExist)
	}
	return []byte(content), nil
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

func (g *FakeGerrit) CreateAutoSubmitChange(ctx context.Context, input gerrit.ChangeInput, reviewers []string, contents map[string]string) (string, error) {
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
	ServeTarball("", repo.content[rev], w, r)
}
