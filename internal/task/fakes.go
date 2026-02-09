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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/google/go-github/v74/github"
	"github.com/google/uuid"
	"github.com/shurcooL/githubv4"
	pb "go.chromium.org/luci/buildbucket/proto"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/gcsfs"
	"golang.org/x/build/internal/installer/darwinpkg"
	"golang.org/x/build/internal/installer/windowsmsi"
	"golang.org/x/build/internal/relui/sign"
	wf "golang.org/x/build/internal/workflow"
	"google.golang.org/protobuf/types/known/structpb"
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

func NewFakeGerrit(t *testing.T, repos ...*FakeRepo) *FakeGerrit {
	result := &FakeGerrit{
		repos:   make(map[string]*FakeRepo),
		changes: make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{repo}/+archive/{archive}", result.serveArchive) // Serve a revision tarball (.tar.gz) like Gerrit does.
	mux.HandleFunc("GET /{repo}/+/{rev}/{path...}", result.serveGitiles)
	mux.HandleFunc("GET /{repo}/info/refs", result.serveGitInfoRefs) // Serve a git repository over HTTP like Gerrit does.
	mux.HandleFunc("POST /{repo}/git-upload-pack", result.serveGitUploadPack)
	mux.HandleFunc("POST /{repo}/git-receive-pack", result.serveGitReceivePack) // Receive pushes to "refs/for/" over HTTP like Gerrit does.
	server := httptest.NewServer(mux)
	result.serverURL = server.URL
	t.Cleanup(server.Close)

	for _, r := range repos {
		result.repos[r.name] = r
	}
	return result
}

type FakeGerrit struct {
	serverURL string
	repos     map[string]*FakeRepo // Repo name → repo.
	changesMu sync.Mutex
	changes   map[string]string // Change ID → commit hash.
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

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, name)
	if err := os.Mkdir(repoDir, 0700); err != nil {
		t.Fatalf("failed to create repository directory: %s", err)
	}
	r := &FakeRepo{
		t:    t,
		name: name,
		dir:  &GitDir{&Git{}, repoDir},
	}
	t.Cleanup(func() { r.dir.Close() })
	r.runGit("init")
	r.runGit("commit", "--allow-empty", "--allow-empty-message", "-m", "")
	return r
}

// CloneFakeRepo initializes a fresh fake repo by cloning the content of
// another fake repo. It returns the fresh fake repo without any remotes.
func CloneFakeRepo(t *testing.T, name string, from *FakeRepo) *FakeRepo {
	if _, err := exec.LookPath("git"); errors.Is(err, exec.ErrNotFound) {
		t.Skip("test requires git")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, name)
	if err := os.Mkdir(repoDir, 0700); err != nil {
		t.Fatalf("failed to create repository directory: %s", err)
	}
	r := &FakeRepo{
		t:    t,
		name: name,
		dir:  &GitDir{&Git{}, repoDir},
	}
	t.Cleanup(func() { r.dir.Close() })
	r.runGit("clone", from.dir.dir, ".")
	r.runGit("remote", "remove", "origin")
	return r
}

// TODO(rfindley): probably every method on FakeRepo should invoke
// repo.t.Helper(), otherwise it's impossible to see where the test failed.

// SetHook sets a git hook in the fake repo.
func (repo *FakeRepo) SetHook(hook, script string) {
	repo.t.Helper()
	if err := os.WriteFile(filepath.Join(repo.dir.dir, ".git", "hooks", hook), []byte(script), 0777); err != nil {
		repo.t.Fatalf("failed to write git %s hook: %s", hook, err)
	}
}

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

func (repo *FakeRepo) ReadDir(commit, dir string) ([]struct{ Name string }, error) {
	b, err := repo.dir.RunCommand(context.Background(), "show", commit+":"+dir)
	if err != nil && strings.Contains(err.Error(), " does not exist ") {
		return nil, errors.Join(gerrit.ErrResourceNotExist, err)
	} else if err != nil {
		return nil, err
	}
	lines, ok := strings.CutPrefix(string(b), fmt.Sprintf("tree %s:%s\n\n", commit, dir))
	if !ok {
		return nil, fmt.Errorf("not a directory")
	}
	// TODO(dmitshur): After Go 1.24, consider simplifying strings.CutSuffix(…, "\n") + strings.Split(…, "\n") in favor of iterating over strings.Lines or so.
	lines, ok = strings.CutSuffix(lines, "\n")
	if !ok {
		return nil, fmt.Errorf("internal error: FakeRepo.ReadDir: no trailing newline")
	}
	var des []struct{ Name string }
	for _, name := range strings.Split(lines, "\n") {
		des = append(des, struct{ Name string }{name})
	}
	return des, nil
}

var _ GerritClient = (*FakeGerrit)(nil)

func (g *FakeGerrit) GitilesURL() string {
	return g.serverURL
}

func (g *FakeGerrit) GitRepoURL(project string) string {
	return g.serverURL + "/" + project
}

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
	out, err := repo.dir.RunCommand(ctx, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		// TODO(hxjiang): switch to git show-ref --exists refs/heads/branch after
		// upgrade git to 2.43.0.
		// https://git-scm.com/docs/git-show-ref/2.43.0#Documentation/git-show-ref.txt---exists
		if strings.Contains(err.Error(), "unknown revision or path not in the working tree") {
			return "", gerrit.ErrResourceNotExist
		}
		// Returns empty string if the error is nil to align the same behavior with
		// the real Gerrit client.
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *FakeGerrit) ListBranches(ctx context.Context, project string) ([]gerrit.BranchInfo, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	out, err := repo.dir.RunCommand(ctx, "for-each-ref", "--format=%(refname) %(objectname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var infos []gerrit.BranchInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		branchCommit := strings.Fields(line)
		infos = append(infos, gerrit.BranchInfo{Ref: branchCommit[0], Revision: branchCommit[1]})
	}
	return infos, nil
}

func (g *FakeGerrit) CreateBranch(ctx context.Context, project, branch string, input gerrit.BranchInput) (string, error) {
	repo, err := g.repo(project)
	if err != nil {
		return "", err
	}
	if _, err = repo.dir.RunCommand(ctx, "branch", branch, input.Revision); err != nil {
		return "", err
	}

	return g.ReadBranchHead(ctx, project, branch)
}

func (g *FakeGerrit) ReadFile(ctx context.Context, project, commit, file string) ([]byte, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	return repo.ReadFile(commit, file)
}

func (g *FakeGerrit) ReadDir(ctx context.Context, project, commit, dir string) ([]struct{ Name string }, error) {
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	return repo.ReadDir(commit, dir)
}

func (g *FakeGerrit) ListCommits(ctx context.Context, project, head, base string) ([]CommitInfo, error) {
	switch {
	case head == "":
		return nil, fmt.Errorf("head is empty")
	case base == "":
		return nil, fmt.Errorf("base is empty")
	}
	repo, err := g.repo(project)
	if err != nil {
		return nil, err
	}
	out, err := repo.dir.RunCommand(ctx, "log",
		"--format=tformat:%H%x00%P%x00%B",
		"-z",
		base+".."+head)
	if err != nil {
		return nil, err
	}
	var cis []CommitInfo
	for b := out; len(b) != 0; {
		var (
			// Calls to readLine match exactly what is specified in --format.
			commitHash = readLine(&b)
			parents    = readLine(&b)
			body       = readLine(&b)
		)
		cis = append(cis, CommitInfo{
			Commit:  commitHash,
			Parents: strings.Split(parents, " "),
			Message: body,
		})
	}
	return cis, nil
}

// readLine reads a line until zero byte, then updates b to the byte that immediately follows.
// A zero byte must exist in b, otherwise readLine panics.
func readLine(b *[]byte) string {
	i := bytes.IndexByte(*b, 0)
	s := string((*b)[:i])
	*b = (*b)[i+1:]
	return s
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
	g.changesMu.Lock()
	changeID := fmt.Sprintf("%s~%d", repo.name, len(g.changes)+1)
	g.changes[changeID] = commit
	g.changesMu.Unlock()
	return changeID, nil
}

func (g *FakeGerrit) considerCommitSubmitted(repo *FakeRepo, commit string) (changeID string) {
	if g.repos[repo.name] != repo {
		repo.t.Fatalf("FakeGerrit.createSubmittedChange: provided repo %q isn't a part of this FakeGerrit instance", repo.name)
	}
	g.changesMu.Lock()
	changeID = fmt.Sprintf("%s~%d", repo.name, len(g.changes)+1)
	g.changes[changeID] = commit
	g.changesMu.Unlock()
	return changeID
}

func (g *FakeGerrit) ConsiderChangeSubmitted(repo *FakeRepo, changeID string) {
	if g.repos[repo.name] != repo {
		repo.t.Fatalf("FakeGerrit.ConsiderChangeSubmitted: provided repo %q isn't a part of this FakeGerrit instance", repo.name)
	}
	g.changesMu.Lock()
	g.changes[changeID] = "unknown submitted commit"
	g.changesMu.Unlock()
}

func (g *FakeGerrit) Submitted(ctx context.Context, changeID, baseCommit string) (string, bool, error) {
	g.changesMu.Lock()
	commit, ok := g.changes[changeID]
	g.changesMu.Unlock()
	return commit, ok, nil
}

func (g *FakeGerrit) Tag(ctx context.Context, project, tag, commit string) error {
	repo, err := g.repo(project)
	if err != nil {
		return err
	}
	repo.Tag(tag, commit)
	return nil
}

func (g *FakeGerrit) ForceTag(ctx context.Context, project, tag, commit string) error {
	repo, err := g.repo(project)
	if err != nil {
		return err
	}
	repo.runGit("tag", "--force", tag, commit)
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

func (g *FakeGerrit) serveGitInfoRefs(w http.ResponseWriter, req *http.Request) {
	repo, err := g.repo(req.PathValue("repo"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch req.URL.RawQuery {
	case "service=git-upload-pack":
		cmd := exec.CommandContext(req.Context(), "git", "upload-pack", "--strict", "--advertise-refs", ".")
		cmd.Dir = filepath.Join(repo.dir.dir, ".git")
		cmd.Env = append(os.Environ(), "GIT_PROTOCOL="+req.Header.Get("Git-Protocol"))
		var buf bytes.Buffer
		cmd.Stdout = &buf
		err = cmd.Run()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		io.WriteString(w, "001e# service=git-upload-pack\n0000")
		io.Copy(w, &buf)
	case "service=git-receive-pack":
		cmd := exec.CommandContext(req.Context(), "git", "receive-pack", "--advertise-refs", ".")
		cmd.Dir = filepath.Join(repo.dir.dir, ".git")
		cmd.Env = append(os.Environ(), "GIT_PROTOCOL="+req.Header.Get("Git-Protocol"))
		var buf bytes.Buffer
		cmd.Stdout = &buf
		err = cmd.Run()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		io.WriteString(w, "001f# service=git-receive-pack\n0000")
		io.Copy(w, &buf)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
func (g *FakeGerrit) serveGitUploadPack(w http.ResponseWriter, req *http.Request) {
	repo, err := g.repo(req.PathValue("repo"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if req.Header.Get("Content-Type") != "application/x-git-upload-pack-request" {
		http.Error(w, "unexpected Content-Type", http.StatusBadRequest)
		return
	}
	cmd := exec.CommandContext(req.Context(), "git", "upload-pack", "--strict", "--stateless-rpc", ".")
	cmd.Dir = filepath.Join(repo.dir.dir, ".git")
	cmd.Env = append(os.Environ(), "GIT_PROTOCOL="+req.Header.Get("Git-Protocol"))
	cmd.Stdin = req.Body
	var buf bytes.Buffer
	cmd.Stdout = &buf
	err = cmd.Run()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	io.Copy(w, &buf)
}
func (g *FakeGerrit) serveGitReceivePack(w http.ResponseWriter, req *http.Request) {
	repo, err := g.repo(req.PathValue("repo"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if req.Header.Get("Content-Type") != "application/x-git-receive-pack-request" {
		http.Error(w, "unexpected Content-Type", http.StatusBadRequest)
		return
	}
	cmd := exec.CommandContext(req.Context(), "git", "receive-pack", "--stateless-rpc", ".")
	cmd.Dir = filepath.Join(repo.dir.dir, ".git")
	cmd.Env = append(os.Environ(), "GIT_PROTOCOL="+req.Header.Get("Git-Protocol"))
	cmd.Stdin = req.Body
	var buf bytes.Buffer
	cmd.Stdout = &buf
	err = cmd.Run()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	io.Copy(w, &buf)
}

func (g *FakeGerrit) serveArchive(w http.ResponseWriter, req *http.Request) {
	repo, err := g.repo(req.PathValue("repo"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rev, ok := strings.CutSuffix(req.PathValue("archive"), ".tar.gz")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	archive, err := repo.dir.RunCommand(req.Context(), "archive", "--format=tgz", rev)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, req, req.PathValue("archive"), time.Now(), bytes.NewReader(archive))
}

func (g *FakeGerrit) serveGitiles(w http.ResponseWriter, req *http.Request) {
	repo, err := g.repo(req.PathValue("repo"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rev, path := req.PathValue("rev"), req.PathValue("path")
	switch req.URL.Query().Get("format") {
	case "JSON":
		des, err := repo.ReadDir(rev, path)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, ")]}'\n") // Magic prefix.
		var v struct {
			ID      string `json:"id"`
			Entries []struct {
				Name string `json:"name"`
			}
		}
		v.ID = rev
		for _, de := range des {
			v.Entries = append(v.Entries, struct {
				Name string `json:"name"`
			}(de))
		}
		json.NewEncoder(w).Encode(v)
	case "TEXT":
		b, err := repo.ReadFile(rev, path)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		enc := base64.NewEncoder(base64.StdEncoding, w)
		enc.Write(b)
		enc.Close()
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (*FakeGerrit) QueryChanges(_ context.Context, query string) ([]*gerrit.ChangeInfo, error) {
	return nil, nil
}

func (*FakeGerrit) SetHashtags(_ context.Context, changeID string, _ gerrit.HashtagsInput) error {
	return fmt.Errorf("pretend that SetHashtags failed")
}

func (*FakeGerrit) GetChange(_ context.Context, _ string, _ ...gerrit.QueryChangesOpt) (*gerrit.ChangeInfo, error) {
	return nil, nil
}

func (*FakeGerrit) SubmitChange(ctx context.Context, changeID string) (gerrit.ChangeInfo, error) {
	return gerrit.ChangeInfo{}, nil
}

func (*FakeGerrit) CreateCherryPick(ctx context.Context, changeID string, branch string, message string) (gerrit.ChangeInfo, bool, error) {
	return gerrit.ChangeInfo{}, false, nil
}

func (*FakeGerrit) MoveChange(ctx context.Context, changeID string, branch string) (gerrit.ChangeInfo, error) {
	return gerrit.ChangeInfo{}, nil
}

func (*FakeGerrit) RebaseChange(ctx context.Context, changeID string, baseRev string) (gerrit.ChangeInfo, error) {
	return gerrit.ChangeInfo{}, nil
}

func (*FakeGerrit) GetRevisionActions(ctx context.Context, changeID, revision string) (map[string]*gerrit.ActionInfo, error) {
	return map[string]*gerrit.ActionInfo{}, nil
}

func (*FakeGerrit) GetCommitMessage(ctx context.Context, changeID string) (string, error) {
	return "", nil
}

// NewFakeSignService returns a fake signing service that can sign PKGs, MSIs,
// and generate GPG signatures. MSIs are "signed" by adding a suffix to them.
// PKGs must actually be tarballs with a prefix of "I'm a PKG!\n". Any files
// they contain that look like binaries will be "signed".
func NewFakeSignService(t *testing.T, outputDir string) *FakeSignService {
	return &FakeSignService{
		t:             t,
		outputDir:     outputDir,
		completedJobs: map[string][]string{},
	}
}

type FakeSignService struct {
	t             *testing.T
	outputDir     string
	mu            sync.Mutex
	completedJobs map[string][]string // Job ID → output objectURIs.
}

func (s *FakeSignService) SignArtifact(_ context.Context, bt sign.BuildType, in []string) (jobID string, _ error) {
	s.t.Logf("fakeSignService: doing %s signing of %q", bt, in)
	jobID = uuid.NewString()
	var out []string
	switch bt {
	case sign.BuildMacOSConstructInstallerOnly:
		if len(in) != 2 {
			return "", fmt.Errorf("got %d inputs, want 2", len(in))
		}
		out = []string{s.fakeConstructPKG(jobID, in[0], in[1], fmt.Sprintf("-installer <%s>", bt))}
	case sign.BuildWindowsConstructInstallerOnly:
		if len(in) != 2 {
			return "", fmt.Errorf("got %d inputs, want 2", len(in))
		}
		out = []string{s.fakeConstructMSI(jobID, in[0], in[1], fmt.Sprintf("-installer <%s>", bt))}

	case sign.BuildMacOS:
		if len(in) != 1 {
			return "", fmt.Errorf("got %d inputs, want 1", len(in))
		}
		out = []string{s.fakeSignPKG(jobID, in[0], fmt.Sprintf("-signed <%s>", bt))}
	case sign.BuildWindows:
		if len(in) != 1 {
			return "", fmt.Errorf("got %d inputs, want 1", len(in))
		}
		out = []string{s.fakeSignFile(jobID, in[0], fmt.Sprintf("-signed <%s>", bt))}
	case sign.BuildGPG:
		if len(in) == 0 {
			return "", fmt.Errorf("got 0 inputs, want 1 or more")
		}
		for _, f := range in {
			out = append(out, s.fakeGPGFile(jobID, f))
		}
	default:
		return "", fmt.Errorf("SignArtifact: not implemented for %v", bt)
	}
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

func (s *FakeSignService) fakeConstructPKG(jobID, f, meta, msg string) string {
	// Check installer metadata.
	b, err := os.ReadFile(strings.TrimPrefix(meta, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeConstructPKG: os.ReadFile: %v", err))
	}
	var opt darwinpkg.InstallerOptions
	if err := json.Unmarshal(b, &opt); err != nil {
		panic(fmt.Errorf("fakeConstructPKG: json.Unmarshal: %v", err))
	}
	var errs []error
	switch opt.GOARCH {
	case "amd64", "arm64": // OK.
	default:
		errs = append(errs, fmt.Errorf("unexpected GOARCH value: %q", opt.GOARCH))
	}
	switch min, _ := strconv.Atoi(opt.MinMacOSVersion); {
	case min >= 11: // macOS 11 or greater; OK.
	case opt.MinMacOSVersion == "10.15": // OK.
	case opt.MinMacOSVersion == "10.13": // OK. Go 1.20 has macOS 10.13 as its minimum.
	default:
		errs = append(errs, fmt.Errorf("unexpected MinMacOSVersion value: %q", opt.MinMacOSVersion))
	}
	if err := errors.Join(errs...); err != nil {
		panic(fmt.Errorf("fakeConstructPKG: unexpected installer options %#v: %v", opt, err))
	}

	// Construct fake installer.
	b, err = os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeConstructPKG: os.ReadFile: %v", err))
	}
	return s.writeOutput(jobID, path.Base(f)+".pkg", append([]byte("I'm a PKG!\n"), b...))
}

func (s *FakeSignService) fakeConstructMSI(jobID, f, meta, msg string) string {
	// Check installer metadata.
	b, err := os.ReadFile(strings.TrimPrefix(meta, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeConstructMSI: os.ReadFile: %v", err))
	}
	var opt windowsmsi.InstallerOptions
	if err := json.Unmarshal(b, &opt); err != nil {
		panic(fmt.Errorf("fakeConstructMSI: json.Unmarshal: %v", err))
	}
	var errs []error
	switch opt.GOARCH {
	case "386", "amd64", "arm", "arm64": // OK.
	default:
		errs = append(errs, fmt.Errorf("unexpected GOARCH value: %q", opt.GOARCH))
	}
	if err := errors.Join(errs...); err != nil {
		panic(fmt.Errorf("fakeConstructMSI: unexpected installer options %#v: %v", opt, err))
	}

	// Construct fake installer.
	_, err = os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeConstructMSI: os.ReadFile: %v", err))
	}
	return s.writeOutput(jobID, path.Base(f)+".msi", []byte("I'm an MSI!\n"))
}

func (s *FakeSignService) fakeSignPKG(jobID, f, msg string) string {
	b, err := os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeSignPKG: os.ReadFile: %v", err))
	}
	b, ok := bytes.CutPrefix(b, []byte("I'm a PKG!\n"))
	if !ok {
		panic(fmt.Errorf("fakeSignPKG: input doesn't look like a PKG to be signed"))
	}
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
	return s.writeOutput(jobID, path.Base(f), b)
}

func (s *FakeSignService) writeOutput(jobID, base string, contents []byte) string {
	path := path.Join(s.outputDir, jobID, base)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		panic(fmt.Errorf("fake signing service: os.MkdirAll: %v", err))
	}
	if err := os.WriteFile(path, contents, 0600); err != nil {
		panic(fmt.Errorf("fake signing service: os.WriteFile: %v", err))
	}
	return "file://" + path
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

func (s *FakeSignService) fakeSignFile(jobID, f, msg string) string {
	b, err := os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeSignFile: os.ReadFile: %v", err))
	}
	b = append(b, []byte(msg)...)
	return s.writeOutput(jobID, path.Base(f), b)
}

func (s *FakeSignService) fakeGPGFile(jobID, f string) string {
	b, err := os.ReadFile(strings.TrimPrefix(f, "file://"))
	if err != nil {
		panic(fmt.Errorf("fakeGPGFile: os.ReadFile: %v", err))
	}
	gpg := fmt.Sprintf("I'm a GPG signature for %x!", sha256.Sum256(b))
	return s.writeOutput(jobID, path.Base(f)+".asc", []byte(gpg))
}

var _ CloudBuildClient = (*FakeCloudBuild)(nil)

const fakeGsutil = `
#!/bin/bash -eux

case "$1" in
"cp")
  in=$2
  out=$3
  if [[ $in == '-' ]]; then
    in=/dev/stdin
  fi
  if [[ $out == '-' ]]; then
    out=/dev/stdout
  fi
  dir=$(dirname "$out")
  mkdir -p "${dir#file://}"
  cp "${in#file://}" "${out#file://}"
  ;;
"cat")
  cat "${2#file://}"
  ;;
*)
  echo unexpected command $@ >&2
  exit 1
  ;;
esac
`

const fakeEmptyBinary = `
#!/bin/bash -eux
echo "this binary will always exit without any error"
exit 0
`

type FakeBinary struct {
	Name string
	// Implementation defines the script content. This script is written to the
	// tool directory and executed when the corresponding command is invoked.
	Implementation string
}

func NewFakeCloudBuild(t *testing.T, gerrit *FakeGerrit, project string, allowedTriggers map[string]map[string]string, fakeBinaries ...FakeBinary) *FakeCloudBuild {
	toolDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolDir, "gsutil"), []byte(fakeGsutil), 0777); err != nil {
		t.Fatal(err)
	}

	for _, binary := range fakeBinaries {
		if err := os.WriteFile(filepath.Join(toolDir, binary.Name), []byte(binary.Implementation), 0777); err != nil {
			t.Fatal(err)
		}
	}
	return &FakeCloudBuild{
		t:               t,
		gerrit:          gerrit,
		project:         project,
		allowedTriggers: allowedTriggers,
		toolDir:         toolDir,
		results:         map[string]error{},
	}
}

type FakeCloudBuild struct {
	t               *testing.T
	gerrit          *FakeGerrit
	project         string
	allowedTriggers map[string]map[string]string
	toolDir         string

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
		if err != nil {
			return CloudBuild{}, err
		}
		defer dir.Close()
		wd = dir.dir
	} else {
		wd = cb.t.TempDir()
	}

	cmd := exec.Command("bash", "-eux")
	cmd.Stdin = strings.NewReader(script)
	cmd.Dir = wd
	tempDir := cb.t.TempDir()
	cmd.Env = append(os.Environ(),
		"TEMP="+tempDir, "TMP="+tempDir, "TEMPDIR="+tempDir, "TMPDIR="+tempDir,
		"PATH="+cb.toolDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf

	runErr := cmd.Run()
	if runErr != nil {
		runErr = fmt.Errorf("script failed: %v output:\n%s", runErr, buf.String())
	}
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

func (cb *FakeCloudBuild) GenerateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string) (changeID string, _ error) {
	if input.Project == "" {
		return "", fmt.Errorf("input.Project must be specified")
	} else if input.Branch == "" {
		return "", fmt.Errorf("input.Branch must be specified")
	} else if !strings.Contains(input.Subject, "\n[git-generate]\n") {
		return "", fmt.Errorf("a commit message with a [git-generate] script must be provided")
	}

	r, err := cb.gerrit.repo(input.Project)
	if err != nil {
		return "", err
	}
	if input.Branch != "master" {
		return "", fmt.Errorf("FakeCloudBuild.GenerateAutoSubmitChange: not implemented for branch %q", input.Branch)
	}

	// Create an empty commit with the git-generate script in commit message.
	r.runGit("commit", "--allow-empty", "-m", input.Subject)

	// Run git-generate.
	//
	// Note: The upside of go run here is that it's more like the real GenerateAutoSubmitChange implementation,
	// but an unintentional side-effect is that it breaks our ability to provide a fake go binary. This is because
	// go run always prepends its own GOROOT/bin to PATH to override any other preexisting 'go' in PATH (issue 68005).
	// To make it possible to use a fake go binary with tasks that use GenerateAutoSubmitChange this probably
	// needs to switch to using go install. Resolving this hasn't been a priority so far because it worked
	// okay to stay with the normal go binary, since most GenerateAutoSubmitChange-using tasks can be faked
	// by providing a fake go:generate directive.
	cmd := exec.CommandContext(ctx, "go", "run", "rsc.io/rf/git-generate@"+gitGenerateVersion)
	cmd.Dir = r.dir.dir
	tempDir := cb.t.TempDir()
	cmd.Env = append(os.Environ(),
		"TEMP="+tempDir, "TMP="+tempDir, "TEMPDIR="+tempDir, "TMPDIR="+tempDir,
		"PATH="+cb.toolDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git-generate failed: %v output:\n%s", err, out)
	}

	if len(r.runGit("status", "--porcelain=v2")) == 0 {
		// Generated content is empty, drop the empty
		// in-progress commit and return empty string.
		r.runGit("reset", "HEAD^")
		return "", nil
	}

	// Update the empty commit with generated content.
	r.runGit("commit", "--amend", "--no-edit")

	if testing.Verbose() {
		cb.t.Logf("FakeCloudBuild.GenerateAutoSubmitChange: generated commit content:\n%s", r.runGit("show", "HEAD"))
	}

	commit := strings.TrimSpace(string(r.runGit("rev-parse", "HEAD")))
	return cb.gerrit.considerCommitSubmitted(r, commit), nil
}

func (cb *FakeCloudBuild) RunCustomSteps(ctx context.Context, steps func(resultURL string) []*cloudbuildpb.BuildStep, _ *CloudBuildOptions) (CloudBuild, error) {
	var gerritProject, fullScript string
	resultURL := "file://" + cb.t.TempDir()
	for i, step := range steps(resultURL) {
		// Cloud Build support docker hub images like "bash". See more details:
		// https://cloud.google.com/build/docs/interacting-with-dockerhub-images
		// Currently, the Bash script is solely for downloading binaries like go.
		// The binaries are included when calling NewFakeCloudBuild() , allowing
		// us to bypass the Bash script for now.
		if step.Name == "bash" {
			continue
		}
		tool, found := strings.CutPrefix(step.Name, "gcr.io/cloud-builders/")
		if !found {
			return CloudBuild{}, fmt.Errorf("does not support custom image: %s", step.Name)
		}
		if tool == "git" && len(step.Args) > 0 && step.Args[0] == "clone" {
			for _, arg := range step.Args {
				project, found := strings.CutPrefix(arg, "https://go.googlesource.com/")
				if found {
					gerritProject = project
					break
				}
			}
			continue
		}

		// As documented by the cloudbuildpb.BuildStep, when the script field is
		// provided, the user cannot specify the entrypoint or args.
		if step.Script != "" && len(step.Args) > 0 {
			return CloudBuild{}, fmt.Errorf("step[%v] can not have script and arguments", i)
		}
		if step.Script != "" && step.Entrypoint != "" {
			return CloudBuild{}, fmt.Errorf("step[%v] can not have script and entrypoint", i)
		}

		// RunCustomSteps allows execution of commands or scripts in any directory,
		// while RunScript always executes in the repo's root directory.
		// To use RunScript within RunCustomSteps, we must first navigate to the
		// target directory if it differs from the repo root.
		if relative := strings.TrimPrefix(step.Dir, gerritProject+"/"); step.Dir != gerritProject && relative != "" {
			fullScript += "pushd " + relative + "\n"
		}

		if len(step.Args) > 0 {
			fullScript += tool + " " + strings.Join(step.Args, " ") + "\n"
		}
		if step.Script != "" {
			fullScript += step.Script + "\n"
		}

		// Return to the previous dir after finish the commands or scripts execution.
		if relative := strings.TrimPrefix(step.Dir, gerritProject+"/"); step.Dir != gerritProject && relative != "" {
			fullScript += "popd\n"
		}
	}

	// In real CloudBuild client, the RunScript calls this lower level method.
	build, err := cb.RunScript(ctx, fullScript, gerritProject, nil)
	if err != nil {
		return CloudBuild{}, err
	}
	// Overwrites the ResultURL as the actual output is written to a unique result
	// directory generated by this method.
	// Unit tests should verify the contents of this directory.
	// The ResultURL returned by RunScript is not used for output and will always
	// point to a new, empty directory.
	return CloudBuild{ID: build.ID, Project: build.Project, ResultURL: resultURL}, nil
}

type FakeSwarmingClient struct {
	t       *testing.T
	toolDir string

	mu      sync.Mutex
	results map[string]error
}

func NewFakeSwarmingClient(t *testing.T, fakeGo string) *FakeSwarmingClient {
	toolDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolDir, "go"), []byte(fakeGo), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "gsutil"), []byte(fakeGsutil), 0777); err != nil {
		t.Fatal(err)
	}
	return &FakeSwarmingClient{
		t:       t,
		toolDir: toolDir,
		results: map[string]error{},
	}
}

var _ SwarmingClient = (*FakeSwarmingClient)(nil)

func (c *FakeSwarmingClient) RunTask(ctx context.Context, dims map[string]string, script string, env map[string]string) (string, error) {
	tempDir := c.t.TempDir()
	cmd := exec.Command("bash", "-eux")
	cmd.Stdin = strings.NewReader("set -o pipefail\n" + script)
	cmd.Dir = c.t.TempDir()
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "TEMP="+tempDir, "TMP="+tempDir, "TEMPDIR="+tempDir, "TMPDIR="+tempDir)
	cmd.Env = append(cmd.Env, "PATH="+c.toolDir+string(filepath.ListSeparator)+os.Getenv("PATH")+
		string(filepath.ListSeparator)+".") // Note: '.' is on PATH to help with Windows compatibility.
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf

	runErr := cmd.Run()
	if runErr != nil {
		runErr = fmt.Errorf("script failed: %v output:\n%s", runErr, buf.String())
	}
	id := fmt.Sprintf("build-%v", rand.Int63())
	c.mu.Lock()
	c.results[id] = runErr
	c.mu.Unlock()
	return id, nil
}

func (c *FakeSwarmingClient) Completed(ctx context.Context, id string) (string, bool, error) {
	c.mu.Lock()
	result, ok := c.results[id]
	c.mu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("unknown task ID %q", id)
	}
	return "here's some build detail", true, result
}

func NewFakeBuildBucketClient(major int, url, bucket string, projects []string) *FakeBuildBucketClient {
	return &FakeBuildBucketClient{
		Bucket:    bucket,
		major:     major,
		GerritURL: url,
		Projects:  projects,
		results:   map[int64]error{},
	}
}

type FakeBuildBucketClient struct {
	Bucket            string
	FailBuilds        []string
	MissingBuilds     []string
	major             int
	GerritURL, Branch string
	Projects          []string

	mu      sync.Mutex
	results map[int64]error
}

var _ BuildBucketClient = (*FakeBuildBucketClient)(nil)

func (c *FakeBuildBucketClient) ListBuilders(ctx context.Context, bucket string) (map[string]*pb.BuilderConfig, error) {
	if bucket != c.Bucket {
		return nil, fmt.Errorf("unexpected bucket %q", bucket)
	}
	res := map[string]*pb.BuilderConfig{}
	for _, proj := range c.Projects {
		prefix := ""
		if proj != "go" {
			prefix = "x_" + proj + "-"
		}
		for _, v := range []string{"gotip", fmt.Sprintf("go1.%v", c.major)} {
			for _, b := range []string{"linux-amd64", "linux-amd64-longtest", "darwin-amd64_13"} {
				parts := strings.FieldsFunc(b, func(r rune) bool { return r == '-' || r == '_' })
				res[prefix+v+"-"+b] = &pb.BuilderConfig{
					Properties: fmt.Sprintf(`{"project":%q, "is_google":true, "target":{"goos":%q, "goarch":%q}}`, proj, parts[0], parts[1]),
				}
			}
		}
	}
	return res, nil
}

func (c *FakeBuildBucketClient) RunBuild(ctx context.Context, bucket string, builder string, commit *pb.GitilesCommit, properties map[string]*structpb.Value) (int64, error) {
	if bucket != c.Bucket {
		return 0, fmt.Errorf("unexpected bucket %q", bucket)
	}
	match := regexp.MustCompile(`.*://(.+)`).FindStringSubmatch(c.GerritURL)
	if commit.Host != match[1] || !slices.Contains(c.Projects, commit.Project) {
		return 0, fmt.Errorf("unexpected host or project: got %q, %q want %q, %q", commit.Host, commit.Project, match[1], c.Projects)
	}
	// It would be nice to validate the commit hash and branch, but it's
	// tricky to get the right value because it depends on the release type.
	// At least validate the commit is a commit.
	if len(commit.Id) != 40 {
		return 0, fmt.Errorf("malformed Git commit hash %q", commit.Id)
	}
	var runErr error
	for _, failBuild := range c.FailBuilds {
		if strings.Contains(builder, failBuild) {
			runErr = fmt.Errorf("run of %q is specified to fail", builder)
		}
	}

	id := rand.Int63()
	c.mu.Lock()
	c.results[id] = runErr
	c.mu.Unlock()
	return id, nil
}

func (c *FakeBuildBucketClient) Completed(ctx context.Context, id int64) (string, bool, error) {
	c.mu.Lock()
	result, ok := c.results[id]
	c.mu.Unlock()
	if !ok {
		return "", false, fmt.Errorf("unknown task ID %d", id)
	}
	return "here's some build detail", true, result
}

func (c *FakeBuildBucketClient) SearchBuilds(ctx context.Context, pred *pb.BuildPredicate) ([]int64, error) {
	if slices.Contains(c.MissingBuilds, pred.GetBuilder().GetBuilder()) {
		return nil, nil
	}
	return []int64{rand.Int63()}, nil
}

type FakeGitHub struct {
	// Milestones is a map from milestone ID to milestone name.
	Milestones map[int]string
	// Issues is a map from issue number to issue details.
	// this map contains all the Issues attached to all milestones and Issues that
	// does not attach to milestone.
	Issues map[int]*github.Issue

	// Releases stores releases created via CreateRelease so we can validate
	// the content during testing.
	Releases []*github.RepositoryRelease

	// The following fields modify behavior of the fake to test
	// certain special scenarios.

	DisallowComments bool // if set, return an error from PostComment
	lastIssueNumber  int  // last issue number created by nextIssueNumber, or 0
	lastMilestoneID  int  // last milestone ID created by nextMilestoneID, or 0
}

func (f *FakeGitHub) nextMilestoneID() int {
	for {
		f.lastMilestoneID++
		if _, ok := f.Milestones[f.lastMilestoneID]; !ok {
			return f.lastMilestoneID
		}
	}
}

func (f *FakeGitHub) nextIssueNumber() int {
	for {
		f.lastIssueNumber++
		if _, ok := f.Issues[f.lastIssueNumber]; !ok {
			return f.lastIssueNumber
		}
	}
}

func (f *FakeGitHub) FetchMilestone(_ context.Context, owner, repo, name string, create bool) (int, error) {
	for id, n := range f.Milestones {
		if n == name {
			return id, nil
		}
	}

	if create {
		newID := f.nextMilestoneID()
		if f.Milestones == nil {
			f.Milestones = map[int]string{}
		}
		f.Milestones[newID] = name
		return newID, nil
	}
	return 0, fmt.Errorf("milestone %q not found and create parameter is false", name)
}

func (f *FakeGitHub) FetchMilestoneIssues(_ context.Context, owner, repo string, milestoneID int) (map[int]map[string]bool, error) {
	if _, ok := f.Milestones[milestoneID]; !ok {
		return nil, fmt.Errorf("milestone %v not found", milestoneID)
	}
	issueLabels := map[int]map[string]bool{}
	for number, issue := range f.Issues {
		if issue.Milestone == nil {
			continue
		}

		if *issue.Milestone.ID != int64(milestoneID) {
			continue
		}

		issueLabels[number] = map[string]bool{}
		for _, label := range issue.Labels {
			issueLabels[number][*label.Name] = true
		}
	}
	return issueLabels, nil
}

func (*FakeGitHub) UploadReleaseAsset(ctx context.Context, owner, repo string, releaseID int64, fileName string, file fs.File) (*github.ReleaseAsset, error) {
	return nil, nil
}

func (f *FakeGitHub) CreateRelease(ctx context.Context, owner, repo string, release *github.RepositoryRelease) (*github.RepositoryRelease, error) {
	f.Releases = append(f.Releases, release)
	return release, nil
}

func (*FakeGitHub) PublishRelease(ctx context.Context, owner, repo string, release *github.RepositoryRelease) (*github.RepositoryRelease, error) {
	return nil, nil
}

func (*FakeGitHub) EditIssue(_ context.Context, owner, repo string, number int, issue *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return nil, nil, nil
}

func (f *FakeGitHub) CreateIssue(ctx context.Context, owner, repo string, request *github.IssueRequest) (*github.Issue, *github.Response, error) {
	if f.Issues == nil {
		f.Issues = map[int]*github.Issue{}
	}

	issueNumber := f.nextIssueNumber()
	f.Issues[issueNumber] = &github.Issue{Number: &issueNumber, Title: request.Title, Body: request.Body}
	if request.Labels != nil {
		for _, l := range *request.Labels {
			f.Issues[issueNumber].Labels = append(f.Issues[issueNumber].Labels, &github.Label{Name: &l})
		}
	}
	if request.Milestone != nil {
		if _, ok := f.Milestones[*request.Milestone]; !ok {
			return nil, nil, fmt.Errorf("the milestone does not exist: %v", *request.Milestone)
		}
		f.Issues[issueNumber].Milestone = &github.Milestone{ID: github.Int64(int64(*request.Milestone))}
	}
	return f.GetIssue(ctx, owner, repo, issueNumber)
}

func (f *FakeGitHub) GetIssue(_ context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error) {
	if issue, ok := f.Issues[number]; !ok {
		return nil, nil, fmt.Errorf("the issue %v does not exist", number)
	} else {
		return issue, nil, nil
	}
}

func (*FakeGitHub) EditMilestone(_ context.Context, owner, repo string, number int, milestone *github.Milestone) (*github.Milestone, *github.Response, error) {
	return nil, nil, nil
}

func (f *FakeGitHub) PostComment(_ context.Context, _ githubv4.ID, _ string) error {
	if f.DisallowComments {
		return fmt.Errorf("pretend that PostComment failed")
	}
	return nil
}
