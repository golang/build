// Copyright 2022 The Go Authors. All rights reserved.
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
	"errors"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-github/v74/github"
	"github.com/google/uuid"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal"
	"golang.org/x/build/internal/gcsfs"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/build/internal/workflowtest"
	"golang.org/x/build/relmeta"
	"golang.org/x/vulndb/report"
	yaml "gopkg.in/yaml.v3"
)

func TestRelease(t *testing.T) {
	if testing.Short() {
		// These release tests are pretty thorough and
		// take upwards of 50 seconds as of 2025-02-21.
		//
		// Also, the RC & major releases involve doing
		// a 'go run git-generate@version' which needs
		// the internet if that version isn't in cache.
		//
		// Skip in short mode.
		t.Skip("skipping large test in short mode")
	}

	workflowtest.Subtest(t, "minor", func(t *testing.T) {
		testRelease(t, "go1.26", 26, "go1.26.1", task.KindMinor)
	})
	workflowtest.Subtest(t, "beta", func(t *testing.T) {
		testRelease(t, "go1.26", 27, "go1.27beta1", task.KindBeta)
	})
	workflowtest.Subtest(t, "rc", func(t *testing.T) {
		testRelease(t, "go1.26", 27, "go1.27rc1", task.KindRC)
	})
	workflowtest.Subtest(t, "major", func(t *testing.T) {
		if len(build.Default.ReleaseTags) < 26 {
			// The 'Maintain x/repo go directive' task will run
			// 'go get go@1.26.0', which can't be done on older
			// toolchains without involving a toolchain upgrade.
			t.Skip("TestRelease/major needs Go 1.26 or newer to run")
		}
		testRelease(t, "go1.26", 27, "go1.27.0", task.KindMajor)
	})
}

func TestSecurity(t *testing.T) {
	workflowtest.Subtest(t, "success", func(t *testing.T) {
		testSecurity(t, true)
	})
	workflowtest.Subtest(t, "failure", func(t *testing.T) {
		testSecurity(t, false)
	})
}

const fakeGo = `#!/bin/bash -eu

case "$1" in
"get")
  ls go.mod go.sum >/dev/null
  for i in "${@:2}"; do
    echo -e "// pretend we've upgraded to $i" >> go.mod
    echo "$i h1:asdasd" | tr '@' ' ' >> go.sum
  done
  ;;
"mod")
  ls go.mod go.sum >/dev/null
  echo "tidied!" >> go.mod
  ;;
*)
  echo unexpected command $@
  exit 1
  ;;
esac
`

type releaseTestDeps struct {
	ctx            context.Context
	cancel         context.CancelFunc
	buildBucket    *task.FakeBuildBucketClient
	goRepo         *task.FakeRepo
	gerrit         *reviewerCheckGerrit
	goDirectives   map[string]string // repo name -> initial go directive
	versionTasks   *task.VersionTasks
	buildTasks     *BuildReleaseTasks
	milestoneTasks *task.MilestoneTasks
	publishedFiles map[string]task.WebsiteFile
	dlClient       *http.Client
}

func newReleaseTestDeps(t *testing.T, previousTag string, major int, wantVersion string) *releaseTestDeps {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}
	if _, err := exec.LookPath("python3"); errors.Is(err, exec.ErrNotFound) {
		// python3 is used in makeScript.
		t.Skip("Requires python3 to be available in PATH.")
	}

	workflow.MaxRetries = 1
	t.Cleanup(func() { workflow.MaxRetries = 3 })
	ctx, cancel := context.WithCancel(context.Background())

	// Set up the fake CDN publishing process.
	servingDir := t.TempDir()
	dlDir := t.TempDir()
	dlURL, dlClient, dlCleanup := workflowtest.NewInMemoryServer(http.FileServer(http.FS(os.DirFS(dlDir))))
	t.Cleanup(dlCleanup)
	go fakeCDNLoad(ctx, t, servingDir, dlDir)

	// Set up the fake website to publish to.
	var filesMu sync.Mutex
	files := map[string]task.WebsiteFile{}
	publishFile := func(f task.WebsiteFile) error {
		filesMu.Lock()
		defer filesMu.Unlock()
		files[strings.TrimPrefix(f.Filename, wantVersion+".")] = f
		return nil
	}

	goDirectives := make(map[string]string)
	goRepo := task.NewFakeRepo(t, "go")
	base := goRepo.Commit(goFiles)
	goRepo.Tag(previousTag, base)
	goRepo.Branch(fmt.Sprintf("release-branch.go1.%d", major), base)
	dlRepo := task.NewFakeRepo(t, "dl")
	buildRepo := task.NewFakeRepo(t, "build")
	goDirectives["build"] = "go 1.22.0"
	buildRepo.Commit(map[string]string{
		"go.mod":   fmt.Sprintf("module golang.org/x/build\n\n%s\n", goDirectives["build"]),
		"build.go": "package build\n",
	})
	toolsRepo := task.NewFakeRepo(t, "tools")
	goDirectives["tools"] = "go 1.21.0"
	toolsRepo.Commit(map[string]string{
		"go.mod":                    fmt.Sprintf("module golang.org/x/tools\n\n%s\n", goDirectives["tools"]),
		"go.sum":                    "\n",
		"internal/stdlib/stdlib.go": "//go:generate cp gen.out manifest.go\n\npackage stdlib\n",
		"internal/stdlib/gen.out":   "package stdlib\n\n// manifest.go was generated!\n",
	})
	fakeGerrit := task.NewFakeGerrit(t, goRepo, dlRepo, buildRepo, toolsRepo)

	gerrit := &reviewerCheckGerrit{FakeGerrit: fakeGerrit}
	versionTasks := &task.VersionTasks{
		Gerrit:     gerrit,
		CloudBuild: task.NewFakeCloudBuild(t, fakeGerrit, "", nil, task.FakeBinary{Name: "go", Implementation: fakeGo}),
		GoProject:  "go",
		GoDirectiveXReposTasks: task.GoDirectiveXReposTasks{
			ForceRepos: []string{"build", "tools"},
			Gerrit:     fakeGerrit,
			HTTPClient: task.GerritHTTPClient(fakeGerrit),
			CloudBuild: task.NewFakeCloudBuild(t, fakeGerrit, "", nil),
		},
	}
	milestoneTasks := &task.MilestoneTasks{
		Client: &task.FakeGitHub{
			Milestones:       map[int]string{0: "Go1.27", 1: "Go1.26.1"},
			DisallowComments: true,
		},
		RepoOwner: "golang",
		RepoName:  "go",
		ApproveAction: func(ctx *workflow.TaskContext) error {
			return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
		},
	}
	buildBucket := task.NewFakeBuildBucketClient(major, fakeGerrit.GerritURL(), "security-try", []string{"go"})

	const dockerProject, dockerTrigger = "docker-build-project", "docker-build-trigger"

	scratchDir := t.TempDir()

	buildTasks := &BuildReleaseTasks{
		GerritClient:             gerrit,
		GerritProject:            "go",
		GerritHTTPClient:         task.GerritHTTPClient(fakeGerrit),
		Git:                      new(task.Git),
		GCSClient:                nil,
		ScratchFS:                &task.ScratchFS{BaseURL: "file://" + scratchDir},
		SignedURL:                "file://" + scratchDir + "/signed/outputs",
		ServingURL:               "file://" + filepath.ToSlash(servingDir),
		SignService:              task.NewFakeSignService(t, scratchDir+"/signed/outputs"),
		DownloadURL:              dlURL,
		DownloadClient:           dlClient,
		ProxyPrefix:              dlURL,
		PublishFile:              publishFile,
		GoogleDockerBuildProject: dockerProject,
		GoogleDockerBuildTrigger: dockerTrigger,
		BuildBucketClient:        buildBucket,
		CloudBuildClient:         task.NewFakeCloudBuild(t, fakeGerrit, dockerProject, map[string]map[string]string{dockerTrigger: {"_GO_VERSION": wantVersion[2:]}}),
		SwarmingClient:           task.NewFakeSwarmingClient(t, fakeGo),
		GitHub:                   &task.FakeGitHub{},
		ApproveAction: func(ctx *workflow.TaskContext) error {
			switch ctx.TaskName {
			case "Confirm PRIVATE-track security CLs",
				"Wait for Release Coordinator Approval":
				return nil
			default:
				return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
			}
		},
	}
	// Cleanups are called in reverse order, and we need to cancel the context
	// before the temp dirs are deleted.
	t.Cleanup(cancel)
	return &releaseTestDeps{
		ctx:            ctx,
		cancel:         cancel,
		buildBucket:    buildBucket,
		goRepo:         goRepo,
		gerrit:         gerrit,
		goDirectives:   goDirectives,
		versionTasks:   versionTasks,
		buildTasks:     buildTasks,
		milestoneTasks: milestoneTasks,
		publishedFiles: files,
		dlClient:       dlClient,
	}
}

func testRelease(t *testing.T, prevTag string, major int, wantVersion string, kind task.ReleaseKind) {
	deps := newReleaseTestDeps(t, prevTag, major, wantVersion)
	wd := workflow.New(workflow.ACL{})

	deps.gerrit.wantReviewers = []string{"heschi", "dmitshur"}
	v := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, major, kind, workflow.Const(deps.gerrit.wantReviewers))
	workflow.Output(wd, "Published Go version", v)

	w := workflowtest.Start(t, wd, map[string]any{
		"Targets to skip testing (or 'all') (optional)": []string{
			// allScript is intentionally hardcoded to fail on GOOS=js
			// and we confirm here that it's possible to skip that.
			"js-wasm-node18", // Builder used on 1.21 and newer.
			"js-wasm",        // Builder used on 1.20 and older.
		},
	})
	outputs := workflowtest.Run(t, deps.ctx, w, &workflowtest.VerboseListener{T: t, OnStall: func() error { deps.cancel(); return nil }})

	// Create a complete list of expected published files.
	wantPublishedFiles := map[string]string{
		wantVersion + ".src.tar.gz": "source",
	}
	for _, t := range releasetargets.TargetsForGo1Point(major) {
		switch t.GOOS {
		case "darwin":
			wantPublishedFiles[wantVersion+"."+t.Name+".tar.gz"] = "archive"
			wantPublishedFiles[wantVersion+"."+t.Name+".pkg"] = "installer"
		case "windows":
			wantPublishedFiles[wantVersion+"."+t.Name+".zip"] = "archive"
			wantPublishedFiles[wantVersion+"."+t.Name+".msi"] = "installer"
		default:
			wantPublishedFiles[wantVersion+"."+t.Name+".tar.gz"] = "archive"
		}
	}

	dlURL, dlClient, files := deps.buildTasks.DownloadURL, deps.dlClient, deps.publishedFiles
	for _, f := range deps.publishedFiles {
		wantKind, ok := wantPublishedFiles[f.Filename]
		if !ok {
			t.Errorf("got unexpected published file %q", f.Filename)
		} else if got, want := f.Kind, wantKind; got != want {
			t.Errorf("file %s has unexpected kind: got %q, want %q", f.Filename, got, want)
		}
		delete(wantPublishedFiles, f.Filename)

		checkFile(t, dlClient, dlURL, files, strings.TrimPrefix(f.Filename, wantVersion+"."), f, func(t *testing.T, b []byte) {
			if got, want := len(b), int(f.Size); got != want {
				t.Errorf("%s size mismatch with metadata: %v != %v", f.Filename, got, want)
			}
			if got, want := fmt.Sprintf("%x", sha256.Sum256(b)), f.ChecksumSHA256; got != want {
				t.Errorf("%s sha256 mismatch with metadata: %q != %q", f.Filename, got, want)
			}
			if got, want := fmt.Sprintf("%x", sha256.Sum256(b)), string(fetch(t, dlClient, dlURL+"/"+f.Filename+".sha256")); got != want {
				t.Errorf("%s sha256 mismatch with .sha256 file: %q != %q", f.Filename, got, want)
			}
			if strings.HasSuffix(f.Filename, ".tar.gz") {
				if got, want := string(fetch(t, dlClient, dlURL+"/"+f.Filename+".asc")), fmt.Sprintf("I'm a GPG signature for %x!", sha256.Sum256(b)); got != want {
					t.Errorf("%v doesn't have the expected GPG signature: got %s, want %s", f.Filename, got, want)
				}
			}
		})
	}
	if len(wantPublishedFiles) != 0 {
		t.Errorf("missing %d published files: %v", len(wantPublishedFiles), wantPublishedFiles)
	}
	versionFile := outputs["VERSION file"].(string)
	if !strings.Contains(versionFile, wantVersion) {
		t.Errorf("version file should contain %q, got %q", wantVersion, versionFile)
	}
	checkTGZ(t, dlClient, dlURL, files, "src.tar.gz", task.WebsiteFile{
		OS:   "",
		Arch: "",
		Kind: "source",
	}, map[string]string{
		"go/VERSION":       versionFile,
		"go/src/make.bash": makeScript,
	})
	checkContents(t, dlClient, dlURL, files, "windows-amd64.msi", task.WebsiteFile{
		OS:   "windows",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm an MSI!\n-signed <Windows>")
	checkTGZ(t, dlClient, dlURL, files, "linux-amd64.tar.gz", task.WebsiteFile{
		OS:   "linux",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkZip(t, dlClient, dlURL, files, "windows-amd64.zip", task.WebsiteFile{
		OS:   "windows",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlClient, dlURL, files, "linux-armv6l.tar.gz", task.WebsiteFile{
		OS:   "linux",
		Arch: "armv6l",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlClient, dlURL, files, "netbsd-arm.tar.gz", task.WebsiteFile{
		OS:   "netbsd",
		Arch: "arm" + map[int]string{21: "v6l", 22: "v6l"}[major],
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlClient, dlURL, files, "darwin-amd64.tar.gz", task.WebsiteFile{
		OS:   "darwin",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION": versionFile,
		"go/bin/go":  "-signed <macOS>",
	})
	checkContents(t, dlClient, dlURL, files, "darwin-amd64.pkg", task.WebsiteFile{
		OS:   "darwin",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm a PKG! -signed <macOS>")
	modVer := "v0.0.1-" + wantVersion + ".darwin-amd64"
	checkContents(t, dlClient, dlURL, nil, modVer+".mod", task.WebsiteFile{}, "module golang.org/toolchain")
	checkContents(t, dlClient, dlURL, nil, modVer+".info", task.WebsiteFile{}, fmt.Sprintf(`"Version":"%v"`, modVer))
	checkZip(t, dlClient, dlURL, nil, modVer+".zip", task.WebsiteFile{}, map[string]string{
		"golang.org/toolchain@" + modVer + "/bin/go": "-signed <macOS>",
	})

	head, err := deps.gerrit.ReadBranchHead(deps.ctx, "dl", "master")
	if err != nil {
		t.Fatal(err)
	}
	content, err := deps.gerrit.ReadFile(deps.ctx, "dl", head, wantVersion+"/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), fmt.Sprintf("version.Run(%q)", wantVersion)) {
		t.Errorf("unexpected dl content: %v", content)
	}

	tag, err := deps.gerrit.GetTag(deps.ctx, "go", wantVersion)
	if err != nil {
		t.Fatal(err)
	}

	if kind != task.KindBeta {
		version, err := deps.gerrit.ReadFile(deps.ctx, "go", tag.Revision, "VERSION")
		if err != nil {
			t.Fatal(err)
		}
		if string(version) != versionFile {
			t.Errorf("VERSION file is %q, expected %q", version, versionFile)
		}
	}

	// Check for go.dev/issue/54377.
	wantUpdateStdlibIndex := kind == task.KindRC || kind == task.KindMajor
	switch b, err := deps.gerrit.ReadFile(deps.ctx, "tools", "HEAD", "internal/stdlib/manifest.go"); {
	case wantUpdateStdlibIndex && err == nil && string(b) == "package stdlib\n\n// manifest.go was generated!\n",
		!wantUpdateStdlibIndex && errors.Is(err, gerrit.ErrResourceNotExist):
		// OK.
	default:
		t.Errorf("unexpected x/tools/internal/stdlib/manifest.go file: read error = %v, content = %q", err, b)
	}

	// Check for go.dev/issue/69095.
	for _, repo := range [...]string{"build", "tools"} {
		goMod, err := deps.gerrit.ReadFile(deps.ctx, repo, "HEAD", "go.mod")
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("module golang.org/x/%s\n", repo)
		switch kind {
		case task.KindMajor:
			prevGoVer := major - 1 // See https://go.dev/design/69095-x-repo-continuous-go#why-1_n_1_0.
			if new, old := fmt.Sprintf("go 1.%d.0", prevGoVer), deps.goDirectives[repo]; new == old {
				t.Fatalf("ineffective test case: repo %s already had %q, so can't check that upgrading it to %q worked", repo, old, new)
			}
			want += fmt.Sprintf("\ngo 1.%d.0\n", prevGoVer)
		default:
			want += fmt.Sprintf("\n%s\n", deps.goDirectives[repo])
		}
		if diff := cmp.Diff(want, string(goMod)); diff != "" {
			t.Errorf("x/ repo should have a maintained go directive and no toolchain directive: go.mod mismatch (-want +got):\n%s", diff)
		}
	}
}

func testSecurity(t *testing.T, mergeFixes bool) {
	deps := newReleaseTestDeps(t, "go1.26.0", 26, "go1.26.1")

	// Set up a fake private repository with a stack of prepared security fixes
	// on top of the fake public repo content. The workflow will upstream these
	// commits to the fake public repo.
	privateRepo := task.CloneFakeRepo(t, "go-private", deps.goRepo)
	// The private Gerrit mirrors the public release branch at the public release
	// branch head (the clone's current HEAD, which equals the base commit).
	privateRepo.Branch("release-branch.go1.26", privateRepo.History()[0])
	securityFix1 := map[string]string{"security.txt": "This file makes us secure"}
	securityFix2 := map[string]string{"security2.txt": "This file makes us more secure"}
	securityFix3 := map[string]string{"security3.txt": "This file makes us even more secure"}
	privateRepo.Branch("internal-release-branch.go1.26.1", privateRepo.Commit(securityFix1))
	privateRepo.CommitOnBranch("internal-release-branch.go1.26.1", securityFix2)
	privateRepo.CommitOnBranch("internal-release-branch.go1.26.1", securityFix3)
	privateGerrit := task.NewFakeGerrit(t, privateRepo)
	deps.buildBucket.GerritURL = privateGerrit.GerritURL()
	deps.buildBucket.Projects = []string{"go-private"}
	deps.buildTasks.PrivateGerritClient = privateGerrit
	deps.buildTasks.PrivateGerritProject = "go-private"
	deps.buildTasks.GerritHTTPClient = task.GerritHTTPClient(deps.gerrit.FakeGerrit, privateGerrit)

	// Set up the fake CL receival and submission process.
	deps.goRepo.SetHook("pre-receive", `#!/bin/bash -eu
read old new refname
case "$refname $old" in
"refs/for/release-branch.go1.26%l=Auto-Submit+1,l=TryBot-Bypass+1 0000000000000000000000000000000000000000")
	echo "Processing changes: refs: 1, new: 3, done"
	echo
	echo "SUCCESS"
	echo
	echo "  https://go-review.googlesource.com/c/go/+/789 add security3.txt [NEW]"
	echo "  https://go-review.googlesource.com/c/go/+/456 add security2.txt [NEW]"
	echo "  https://go-review.googlesource.com/c/go/+/123 add security.txt [NEW]"
	echo
	;;
*)
	echo "unexpected input $@"
	exit 1
	;;
esac
`)
	if mergeFixes {
		deps.goRepo.SetHook("post-receive", `#!/bin/bash -eu
read old new refname
case "$refname $old" in
"refs/for/release-branch.go1.26%l=Auto-Submit+1,l=TryBot-Bypass+1 0000000000000000000000000000000000000000")
	git update-ref -d "$refname"
	git update-ref refs/heads/release-branch.go1.26 "$new"
	;;
*)
	echo "unexpected input $@"
	exit 1
	;;
esac
`)
	}
	deps.gerrit.ConsiderChangeSubmitted(deps.goRepo, "go~123")
	deps.gerrit.ConsiderChangeSubmitted(deps.goRepo, "go~456")
	deps.gerrit.ConsiderChangeSubmitted(deps.goRepo, "go~789")

	// Run the release.
	wd := workflow.New(workflow.ACL{})
	v := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, 26, task.KindMinor, workflow.Slice[string]())
	workflow.Output(wd, "Published Go version", v)

	w := workflowtest.Start(t, wd, map[string]any{
		"Targets to skip testing (or 'all') (optional)": []string{"js-wasm"},
	})

	if mergeFixes {
		workflowtest.Run(t, deps.ctx, w, nil)
	} else {
		workflowtest.RunToFailure(t, deps.ctx, w, "Check branch state matches source archive", &workflowtest.VerboseListener{T: t})
		return
	}
	checkTGZ(t, deps.dlClient, deps.buildTasks.DownloadURL, deps.publishedFiles, "src.tar.gz", task.WebsiteFile{
		OS:   "",
		Arch: "",
		Kind: "source",
	}, map[string]string{
		"go/security.txt":  "This file makes us secure",
		"go/security2.txt": "This file makes us more secure",
		"go/security3.txt": "This file makes us even more secure",
	})
}

// newMinorCoalesceTestDeps sets up release test dependencies for exercising
// createMinorReleaseWorkflow with PRIVATE-track security patches present.
//
// It extends a base set of single-major deps with a second public release
// branch (so both minors can be released), a second GitHub milestone, and a
// fully-wired private coalesce Gerrit backed by a private clone of the public
// "go" repo and a security-metadata repo holding the milestone YAML.
//
// withPrivatePatches controls whether the milestone has any PRIVATE patches.
func newMinorCoalesceTestDeps(t *testing.T, withPrivatePatches bool) (*releaseTestDeps, *task.FakeGerrit) {
	// currentMajor=26, prevMajor=25. newReleaseTestDeps sets up the 26 series;
	// add the 25 series so GetNextMinorVersions([26,25]) returns the two minors.
	deps := newReleaseTestDeps(t, "go1.26.0", 26, "go1.26.1")

	base, err := deps.gerrit.ReadBranchHead(deps.ctx, "go", "release-branch.go1.26")
	if err != nil {
		t.Fatal(err)
	}
	deps.goRepo.Branch("release-branch.go1.25", base)
	deps.goRepo.Tag("go1.25.0", base)

	// FetchMilestones for go1.25.1 needs a "Go1.25.1" milestone to already exist.
	fakeGitHub, ok := deps.milestoneTasks.Client.(*task.FakeGitHub)
	if !ok {
		t.Fatalf("milestone client is %T, want *task.FakeGitHub", deps.milestoneTasks.Client)
	}
	fakeGitHub.Milestones[2] = "Go1.25.1"
	fakeGitHub.Issues = map[int]*github.Issue{
		70001: {
			Labels:    []*github.Label{{Name: github.Ptr("release-blocker")}, {Name: github.Ptr("Security")}},
			Milestone: &github.Milestone{ID: github.Int64(0)},
		},
	}
	fakeGitHub.Comments = map[int][]*github.IssueComment{
		70001: {{
			User: &github.User{Login: github.Ptr("gopherbot")},
			Body: github.Ptr("Backport issue(s) opened: #70025 (for 1.25), #70026 (for 1.26)."),
		}},
	}

	// Private side: clone the public repo and create the branches the coalesce
	// steps read: "public" and the major release branches.
	privGoRepo := task.CloneFakeRepo(t, "go", deps.goRepo)
	privGoRepo.Branch("public", base)
	privGoRepo.Branch("release-branch.go1.26", base)
	privGoRepo.Branch("release-branch.go1.25", base)

	// security-metadata holds the milestone that lists the security patches.
	smRepo := task.NewFakeRepo(t, "security-metadata")
	smHead := smRepo.History()[0]
	smRepo.Branch("main", smHead)
	var milestoneYAML string
	if withPrivatePatches {
		milestoneYAML = `id: 99915010
security_patches:
    - id: 40027190
      package: crypto/tls
      track: PRIVATE
      github_issue_id: 70001
      cve: CVE-1985-0703
      changelists:
        - https://go-internal-review.git.corp.google.com/c/go/+/1234
        - https://go-internal-review.git.corp.google.com/c/go/+/5678
      target_releases:
        - go1.26.1
        - go1.25.1`
	} else {
		// A milestone with only PUBLIC patches: the coalesce must short-circuit.
		milestoneYAML = `id: 99915010
security_patches:
    - id: 20024001
      package: runtime
      track: PUBLIC
      github_issue_id: 70001
      changelists:
        - https://go.dev/cl/123456
      target_releases:
        - go1.26.1
        - go1.25.1`
	}
	smRepo.CommitOnBranch("main", map[string]string{
		filepath.Join("data", "milestones", "99915010.yaml"): milestoneYAML,
	})

	privGerrit := task.NewFakeGerrit(t, privGoRepo, smRepo)
	if withPrivatePatches {
		privGerrit.AddChange("go", "1234", &gerrit.ChangeInfo{
			ID:           "1234",
			ChangeID:     "1234",
			ChangeNumber: 1234,
			Branch:       "public",
			Submittable:  true,
			Mergeable:    true,
		}, "crypto/tls: fix something")
		privGerrit.AddChange("go", "5678", &gerrit.ChangeInfo{
			ID:           "5678",
			ChangeID:     "5678",
			ChangeNumber: 5678,
			Branch:       "public",
			Submittable:  true,
			Mergeable:    true,
		}, "cmd/compile: fix something else")
	}

	deps.buildTasks.PrivateGerritClient = privGerrit
	deps.buildTasks.PrivateGerritProject = "go"
	deps.buildTasks.GerritHTTPClient = task.GerritHTTPClient(deps.gerrit.FakeGerrit, privGerrit)

	return deps, privGerrit
}

func coalesceRM() *relmeta.ReleaseMilestone {
	return &relmeta.ReleaseMilestone{ID: 99915010, Patches: []*relmeta.SecurityPatch{{
		ID:            40027190,
		Track:         relmeta.Private,
		Package:       "crypto/tls",
		GitHubIssueID: 70001,
		CVE:           "CVE-1985-0703",
		Changelists: []string{
			"https://go-internal-review.git.corp.google.com/c/go/+/1234",
			"https://go-internal-review.git.corp.google.com/c/go/+/5678",
		},
	}}}
}

func coalesceBackports() task.BackportManifest {
	return task.BackportManifest{40027190: {"1.25": 70025, "1.26": 70026}}
}

func seedRiders(g *task.FakeGerrit) {
	g.AddChange("go", "1234", nil, "crypto/tls: fix something\n\nFixes CVE-1985-0703\nFor golang/go#70001")
	g.AddChange("go", "5678", nil, "cmd/compile: fix something else\n\nFixes CVE-1985-0703\nFor golang/go#70001")
}

func approveSecurityCLsOnly(ctx *workflow.TaskContext) error {
	if strings.Contains(ctx.TaskName, "Confirm PRIVATE-track security CLs") {
		return nil
	}
	return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
}

func runMinorReleaseToFailure(t *testing.T, deps *releaseTestDeps, privGerrit *task.FakeGerrit, params map[string]any, failTask string, listener workflow.Listener) string {
	t.Helper()
	comm := task.CommunicationTasks{
		SecurityCommunicationTasks: task.SecurityCommunicationTasks{PrivateGerrit: privGerrit},
	}
	wd, err := createMinorReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, comm, 25, 26)
	if err != nil {
		t.Fatal(err)
	}
	if params == nil {
		params = minorReleaseParams()
	}
	if failTask == "" {
		failTask = "Go 1.26: Wait for Release Coordinator Approval"
	}
	if listener == nil {
		listener = &workflowtest.VerboseListener{T: t}
	}
	w := workflowtest.Start(t, wd, params)
	return workflowtest.RunToFailure(t, deps.ctx, w, failTask, listener)
}

func TestMinorReleaseSecurityCoalesce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)

		// Approve the confirm step; fail any other approval request.
		deps.buildTasks.ApproveAction = approveSecurityCLsOnly

		publicHeadBefore, err := privGerrit.ReadBranchHead(deps.ctx, "go", "public")
		if err != nil {
			t.Fatalf("reading public head before workflow: %v", err)
		}

		// Run until the release coordinator approval is rejected, so we don't
		// have to drive the full build. By then both minors' confirm tasks have
		// finished.
		runMinorReleaseToFailure(t, deps, privGerrit, nil, "", nil)

		branches, err := privGerrit.ListBranches(deps.ctx, "go")
		if err != nil {
			t.Fatalf("listing branches: %v", err)
		}
		branchNames := make(map[string]bool)
		for _, b := range branches {
			name := strings.TrimPrefix(b.Ref, "refs/heads/")
			branchNames[name] = true
		}
		for _, want := range []string{
			"internal-release-branch.go1.26.1",
			"internal-release-branch.go1.25.1",
		} {
			if !branchNames[want] {
				t.Errorf("internal release branch %q not found; branches: %v", want, branchNames)
			}
		}

		for _, ib := range []string{
			"internal-release-branch.go1.26.1",
			"internal-release-branch.go1.25.1",
		} {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", ib)
			if err != nil {
				t.Fatalf("reading head of %s: %v", ib, err)
			}
			if head == publicHeadBefore {
				t.Errorf("internal branch %s head (%s) equals original public head; cherry-picks did not land", ib, head)
			}
		}

		var foundCheckpoint bool
		for name := range branchNames {
			if strings.HasPrefix(name, "go1.26.1-go1.25.1-checkpoint-") {
				foundCheckpoint = true
				break
			}
		}
		if !foundCheckpoint {
			t.Errorf("checkpoint branch matching go1.26.1-go1.25.1-checkpoint-* not found; branches: %v", branchNames)
		}

		for _, clID := range []string{"1234", "5678"} {
			ci, err := privGerrit.GetChange(deps.ctx, clID)
			if err != nil {
				t.Fatalf("GetChange(%s): %v", clID, err)
			}
			if ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("CL %s status = %q, want %q", clID, ci.Status, gerrit.ChangeStatusMerged)
			}
		}

		wantCLCount := 2
		for _, ib := range []string{
			"internal-release-branch.go1.26.1",
			"internal-release-branch.go1.25.1",
		} {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", ib)
			if err != nil {
				t.Fatalf("reading head of %s: %v", ib, err)
			}
			commits, err := privGerrit.ListCommits(deps.ctx, "go", head, publicHeadBefore)
			if err != nil {
				t.Fatalf("ListCommits on %s: %v", ib, err)
			}
			if got := len(commits); got != wantCLCount {
				t.Errorf("branch %s has %d commits above public head, want %d", ib, got, wantCLCount)
			}
			wantPrefix := "[" + majorFromMinor(strings.TrimPrefix(ib, "internal-")) + "]"
			var gotMessages []string
			for _, ci := range commits {
				gotMessages = append(gotMessages, ci.Message)
				if !strings.HasPrefix(ci.Message, wantPrefix) {
					t.Errorf("branch %s commit %s message %q does not start with %q", ib, ci.Commit[:8], ci.Message, wantPrefix)
				}
			}
		}

		branchCPSets := map[string]map[string]bool{}
		branchRiders := map[string]string{
			"internal-release-branch.go1.26.1": "\nFixes golang/go#70026",
			"internal-release-branch.go1.25.1": "\nFixes golang/go#70025",
		}
		for ib, rider := range branchRiders {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", ib)
			if err != nil {
				t.Fatalf("reading head of %s: %v", ib, err)
			}
			commits, err := privGerrit.ListCommits(deps.ctx, "go", head, publicHeadBefore)
			if err != nil {
				t.Fatalf("ListCommits on %s: %v", ib, err)
			}
			msgs := map[string]bool{}
			for _, ci := range commits {
				bare := strings.SplitN(ci.Message, "] ", 2)
				if len(bare) == 2 {
					if !strings.Contains(bare[1], rider) {
						t.Errorf("branch %s cherry-pick %q is missing backport rider %q", ib, bare[1], strings.TrimPrefix(rider, "\n"))
					}
					msgs[strings.ReplaceAll(bare[1], rider, "")] = true
				}
			}
			branchCPSets[ib] = msgs
		}
		set26 := branchCPSets["internal-release-branch.go1.26.1"]
		set25 := branchCPSets["internal-release-branch.go1.25.1"]
		if len(set26) != len(set25) {
			t.Errorf("cherry-pick set sizes differ: go1.26.1 has %d, go1.25.1 has %d", len(set26), len(set25))
		}
		for msg := range set26 {
			if !set25[msg] {
				t.Errorf("cherry-pick %q on go1.26.1 but not go1.25.1", msg)
			}
		}
	})
}

func TestMinorReleaseSecurityCoalesceWithRC(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)

		base, err := deps.gerrit.ReadBranchHead(deps.ctx, "go", "release-branch.go1.26")
		if err != nil {
			t.Fatal(err)
		}
		deps.goRepo.Branch("release-branch.go1.27", base)

		privGoRepo, err := privGerrit.ReadBranchHead(deps.ctx, "go", "public")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := privGerrit.CreateBranch(deps.ctx, "go", "release-branch.go1.27", gerrit.BranchInput{Revision: privGoRepo}); err != nil {
			t.Fatal(err)
		}

		deps.buildTasks.ApproveAction = approveSecurityCLsOnly

		publicHeadBefore, err := privGerrit.ReadBranchHead(deps.ctx, "go", "public")
		if err != nil {
			t.Fatalf("reading public head: %v", err)
		}

		runMinorReleaseToFailure(t, deps, privGerrit, nil, "", nil)

		wantBranches := []string{
			"internal-release-branch.go1.27rc1",
			"internal-release-branch.go1.26.1",
			"internal-release-branch.go1.25.1",
		}
		for _, ib := range wantBranches {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", ib)
			if err != nil {
				t.Fatalf("reading head of %s: %v", ib, err)
			}
			if head == publicHeadBefore {
				t.Errorf("internal branch %s head equals public head; cherry-picks did not land", ib)
			}
			commits, err := privGerrit.ListCommits(deps.ctx, "go", head, publicHeadBefore)
			if err != nil {
				t.Fatalf("ListCommits on %s: %v", ib, err)
			}
			if got := len(commits); got != 2 {
				t.Errorf("branch %s has %d commits above public head, want 2", ib, got)
			}
		}
	})
}

func TestMinorReleaseCoalesceNoPrivatePatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, false)

		// There are no PRIVATE patches, so each release's confirm task takes the "no
		// security fix" path. Allow those approvals; fail any other approval request.
		deps.buildTasks.ApproveAction = approveSecurityCLsOnly

		// Run until the release coordinator approval is rejected, so we can check
		// the coalesce's side effects without driving the full build. By then the
		// checkpoint and internal release branches would have been created (if the
		// coalesce didn't short-circuit).
		runMinorReleaseToFailure(t, deps, privGerrit, nil, "", nil)

		// The coalesce must not have created any security branches.
		branches, err := privGerrit.ListBranches(deps.ctx, "go")
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range branches {
			name := strings.TrimPrefix(b.Ref, "refs/heads/")
			if strings.Contains(name, "checkpoint") || strings.HasPrefix(name, "internal-") {
				t.Errorf("coalesce created branch %q despite there being no PRIVATE-track patches", name)
			}
		}
	})
}

func TestMinorReleaseSecurityCoalesceCherryPickConflict(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)

		workflow.MaxRetries = 3

		privGerrit.AddChange("go", "1234", &gerrit.ChangeInfo{
			ID:                   "1234",
			ChangeID:             "1234",
			ChangeNumber:         1234,
			Branch:               "public",
			Submittable:          true,
			Mergeable:            true,
			ContainsGitConflicts: true,
		}, "crypto/tls: fix something")
		privGerrit.AddChange("go", "5678", &gerrit.ChangeInfo{
			ID:                   "5678",
			ChangeID:             "5678",
			ChangeNumber:         5678,
			Branch:               "public",
			Submittable:          true,
			Mergeable:            true,
			ContainsGitConflicts: true,
		}, "cmd/compile: fix something else")

		deps.buildTasks.ApproveAction = approveSecurityCLsOnly

		tracker := &taskStartTracker{Listener: &workflowtest.VerboseListener{T: t}}
		errMsg := runMinorReleaseToFailure(t, deps, privGerrit, nil, "Create cherry-picks", tracker)

		var (
			changes    []*gerrit.ChangeInfo
			conflicted = map[string]*gerrit.ChangeInfo{}
			branches   = map[string]bool{}
		)
		for _, num := range []string{"1234", "5678"} {
			if !strings.Contains(errMsg, "go-internal-review.git.corp.google.com/c/go/+/"+num) {
				t.Errorf("error does not mention source CL %s: %s", num, errMsg)
			}
			ci, err := privGerrit.GetChange(deps.ctx, num)
			if err != nil {
				t.Fatalf("GetChange(%s): %v", num, err)
			}
			changes = append(changes, ci)
			existing, err := privGerrit.QueryChanges(deps.ctx, "change:"+num)
			if err != nil {
				t.Fatalf("QueryChanges(%s): %v", num, err)
			}
			var created int
			for _, ci := range existing {
				if strings.HasPrefix(ci.Branch, "internal-release-branch.go1.") {
					created++
					conflicted[ci.ID] = ci
					branches[ci.Branch] = true
				}
			}
			if created != 2 {
				t.Errorf("change %s: got %d conflicted cherry-picks left on internal branches, want 2", num, created)
			}
		}

		var internalBranches []string
		for b := range branches {
			internalBranches = append(internalBranches, b)
		}
		for id, ci := range conflicted {
			resolved := *ci
			resolved.ContainsGitConflicts = false
			resolved.Submittable = true
			privGerrit.AddChange("go", id, &resolved, "")
		}

		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "cherry-picks"}}
		retried, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, internalBranches, []*task.PatchChanges{{Patch: coalesceRM().Patches[0], Changes: changes}}, coalesceBackports())
		if err != nil {
			t.Fatalf("createSecurityCherryPicks after resolving conflicts: %v", err)
		}
		if len(retried) != len(conflicted) {
			t.Fatalf("retry returned %d cherry-picks, want %d", len(retried), len(conflicted))
		}
		for _, cp := range retried {
			if _, ok := conflicted[cp.ID]; !ok {
				t.Errorf("retry created new cherry-pick %s instead of reusing the resolved CL", cp.ID)
			}
		}
		submitted, err := deps.buildTasks.submitCherryPicks(taskCtx, retried)
		if err != nil {
			t.Fatalf("submitCherryPicks after resolving conflicts: %v", err)
		}
		for _, cp := range retried {
			ci, err := privGerrit.GetChange(deps.ctx, cp.ID)
			if err != nil {
				t.Fatalf("GetChange(%s): %v", cp.ID, err)
			}
			if ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("cherry-pick %s status = %q, want %q; submitted = %v", cp.ID, ci.Status, gerrit.ChangeStatusMerged, submitted)
			}
		}
		if !strings.Contains(errMsg, "internal-release-branch.go1.") {
			t.Errorf("error does not mention target branch: %s", errMsg)
		}
		if !strings.Contains(errMsg, "merge conflicts") {
			t.Errorf("error does not mention merge conflicts: %s", errMsg)
		}
		if _, started := tracker.started.Load("Submit cherry-picks"); started {
			t.Error("Submit cherry-picks ran despite cherry-pick conflict")
		}
	})
}

func TestMinorReleaseSecurityCoalesceRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		// First run: establish a prior-iteration checkpoint branch.
		first, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("first createSecurityCheckpoint: %v", err)
		}
		if !strings.HasPrefix(first.Branch, bi.CheckpointName+"-") {
			t.Errorf("checkpoint name %q is not prefixed with %q", first.Branch, bi.CheckpointName+"-")
		}
		firstHead, err := privGerrit.ReadBranchHead(deps.ctx, "go", first.Branch)
		if err != nil {
			t.Fatalf("reading first checkpoint head: %v", err)
		}

		// Second run: a restart forks a new checkpoint. The branch name embeds a
		// second-resolution timestamp, so a same-second restart collides on the
		// branch name (real Gerrit 409). When the second has rolled over, the
		// restart succeeds with a distinct name; either way, the first run's
		// checkpoint branch must remain exactly as it was.
		second, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			var httpErr *gerrit.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Res.StatusCode != http.StatusConflict {
				t.Fatalf("second createSecurityCheckpoint: %v", err)
			}
			t.Logf("same-second restart collided on the timestamped checkpoint name (expected): %v", err)
		} else if second.Branch == first.Branch {
			t.Errorf("restart reused checkpoint name %q; want a distinct timestamped branch", second.Branch)
		}

		// The first run's checkpoint branch is left untouched.
		gotHead, err := privGerrit.ReadBranchHead(deps.ctx, "go", first.Branch)
		if err != nil {
			t.Fatalf("re-reading first checkpoint head: %v", err)
		}
		if gotHead != firstHead {
			t.Errorf("first checkpoint head moved: was %q, now %q", firstHead, gotHead)
		}
	})
}

func TestRestartInternalBranchesMergedCherryPicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		seedRiders(privGerrit)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		branches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("first createInternalReleaseBranches: %v", err)
		}
		cps, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, branches, cls, coalesceBackports())
		if err != nil {
			t.Fatalf("first createSecurityCherryPicks: %v", err)
		}
		if _, err := deps.buildTasks.submitCherryPicks(taskCtx, cps); err != nil {
			t.Fatalf("submitCherryPicks: %v", err)
		}

		coalescedHeads := map[string]string{}
		for _, b := range branches {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
			if err != nil {
				t.Fatalf("reading head of %s: %v", b, err)
			}
			coalescedHeads[b] = head
		}

		branches2, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("restart createInternalReleaseBranches with merged CPs: %v", err)
		}
		if len(branches2) != len(branches) {
			t.Fatalf("branch count mismatch: first=%d, restart=%d", len(branches), len(branches2))
		}
		cps2, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, branches2, cls, coalesceBackports())
		if err != nil {
			t.Fatalf("restart createSecurityCherryPicks with merged CPs: %v", err)
		}
		if got, want := len(cps2), len(cls[0].Changes)*len(branches); got != want {
			t.Fatalf("restart cherry-picks: got %d, want %d", got, want)
		}
		for _, cp := range cps2 {
			if cp.Status != gerrit.ChangeStatusMerged {
				t.Errorf("restart cherry-pick CL %d status = %q, want %q", cp.ChangeNumber, cp.Status, gerrit.ChangeStatusMerged)
			}
		}
		if _, err := deps.buildTasks.submitCherryPicks(taskCtx, cps2); err != nil {
			t.Fatalf("restart submitCherryPicks: %v", err)
		}

		for _, b := range branches2 {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
			if err != nil {
				t.Fatalf("reading head of %s after restart: %v", b, err)
			}
			if head != coalescedHeads[b] {
				t.Errorf("branch %s head changed after restart: got %s, want %s", b, head, coalescedHeads[b])
			}
			publicHead, err := privGerrit.ReadBranchHead(deps.ctx, "go", majorFromMinor(strings.TrimPrefix(b, "internal-")))
			if err != nil {
				t.Fatal(err)
			}
			commits, err := privGerrit.ListCommits(deps.ctx, "go", head, publicHead)
			if err != nil {
				t.Fatalf("ListCommits(%s): %v", b, err)
			}
			if len(commits) != len(cls[0].Changes) {
				t.Errorf("branch %s has %d security commits above public head, want %d", b, len(commits), len(cls[0].Changes))
			}
		}
	})
}

func TestReadSecurityRefRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		branches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatal(err)
		}

		for _, b := range branches {
			version := strings.TrimPrefix(b, "internal-release-branch.")
			commit, err := deps.buildTasks.readSecurityRef(taskCtx, version)
			if err != nil {
				t.Fatalf("readSecurityRef(%s): %v", version, err)
			}
			wantHead, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
			if err != nil {
				t.Fatalf("ReadBranchHead(%s): %v", b, err)
			}
			if commit != wantHead {
				t.Errorf("readSecurityRef(%s) = %q, want %q (branch head of %s)", version, commit, wantHead, b)
			}
		}

		_, err = deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatal(err)
		}

		for _, b := range branches {
			version := strings.TrimPrefix(b, "internal-release-branch.")
			commit, err := deps.buildTasks.readSecurityRef(taskCtx, version)
			if err != nil {
				t.Fatalf("readSecurityRef(%s) after restart: %v", version, err)
			}
			wantHead, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
			if err != nil {
				t.Fatalf("ReadBranchHead(%s) after restart: %v", b, err)
			}
			if commit != wantHead {
				t.Errorf("readSecurityRef(%s) after restart = %q, want %q", version, commit, wantHead)
			}
		}

		commit, err := deps.buildTasks.readSecurityRef(taskCtx, "go1.99.99")
		if err != nil {
			t.Fatalf("readSecurityRef for nonexistent version: %v", err)
		}
		if commit != "" {
			t.Errorf("readSecurityRef for nonexistent version = %q, want empty", commit)
		}
	})
}

func newPublicizeTestDeps(t *testing.T) (*BuildReleaseTasks, *task.FakeGerrit, *task.FakeGerrit, string, string) {
	t.Helper()

	pubRepo := task.NewFakeRepo(t, "go")
	base := pubRepo.Commit(map[string]string{"README": "hello"})
	pubRepo.Branch("release-branch.go1.26", base)

	privRepo := task.CloneFakeRepo(t, "go", pubRepo)
	privRepo.Branch("internal-release-branch.go1.26.1", base)
	privRepo.CommitOnBranchWithMessage("internal-release-branch.go1.26.1",
		"crypto/tls: fix vuln\n\nFixes CVE-2026-1234\n\nChange-Id: I0000000000000000000000000000000000000001",
		map[string]string{"security1.txt": "fix1"})
	privRepo.CommitOnBranchWithMessage("internal-release-branch.go1.26.1",
		"cmd/compile: fix another vuln\n\nFixes CVE-2026-5678\n\nChange-Id: I0000000000000000000000000000000000000002",
		map[string]string{"security2.txt": "fix2"})

	pubGerrit := task.NewFakeGerrit(t, pubRepo)
	privGerrit := task.NewFakeGerrit(t, privRepo)

	securityCommit, err := privGerrit.ReadBranchHead(context.Background(), "go", "internal-release-branch.go1.26.1")
	if err != nil {
		t.Fatal(err)
	}

	build := &BuildReleaseTasks{
		GerritClient:         pubGerrit,
		GerritProject:        "go",
		PrivateGerritClient:  privGerrit,
		PrivateGerritProject: "go",
		Git:                  new(task.Git),
	}
	return build, pubGerrit, privGerrit, base, securityCommit
}

func TestPublicizeIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("Requires bash shell scripting support.")
		}

		build, pubGerrit, _, base, securityCommit := newPublicizeTestDeps(t)
		taskCtx := &workflow.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: t.Name()}}

		pubGerrit.AddChange("go", "pub-1", &gerrit.ChangeInfo{
			ID:           "pub-1",
			ChangeID:     "I0000000000000000000000000000000000000001",
			ChangeNumber: 9001,
			Branch:       "release-branch.go1.26",
			Status:       "NEW",
		}, "crypto/tls: fix vuln")
		pubGerrit.AddChange("go", "pub-2", &gerrit.ChangeInfo{
			ID:           "pub-2",
			ChangeID:     "I0000000000000000000000000000000000000002",
			ChangeNumber: 9002,
			Branch:       "release-branch.go1.26",
			Status:       "NEW",
		}, "cmd/compile: fix another vuln")

		cls, err := build.publicizePrivateSecurityCLs(taskCtx,
			"go1.26.1", "release-branch.go1.26", base, securityCommit, nil)
		if err != nil {
			t.Fatalf("publicize with existing CLs: %v", err)
		}
		if len(cls) != 2 {
			t.Fatalf("publicize returned %d CL IDs, want 2", len(cls))
		}
		for _, cl := range cls {
			if !strings.Contains(cl, "go~") {
				t.Errorf("unexpected CL ID format: %q", cl)
			}
		}
	})
}

func TestPublicizePartialFailsOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("Requires bash shell scripting support.")
		}

		build, pubGerrit, _, base, securityCommit := newPublicizeTestDeps(t)
		taskCtx := &workflow.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: t.Name()}}

		pubGerrit.AddChange("go", "pub-1", &gerrit.ChangeInfo{
			ID:           "pub-1",
			ChangeID:     "I0000000000000000000000000000000000000001",
			ChangeNumber: 9001,
			Branch:       "release-branch.go1.26",
			Status:       "NEW",
		}, "crypto/tls: fix vuln")

		_, err := build.publicizePrivateSecurityCLs(taskCtx,
			"go1.26.1", "release-branch.go1.26", base, securityCommit, nil)
		if err == nil {
			t.Fatal("expected error for partial publicize, got nil")
		}
		if !strings.Contains(err.Error(), "partial publicize") {
			t.Errorf("error does not mention partial publicize: %v", err)
		}
		if !strings.Contains(err.Error(), "manual intervention") {
			t.Errorf("error does not mention manual intervention: %v", err)
		}
	})
}

func TestFetchSecurityMilestone(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	ctx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "milestone"}}

	t.Run("nil client", func(t *testing.T) {
		b := *deps.buildTasks
		b.PrivateGerritClient = nil
		b.PrivateGerritProject = ""
		rm, err := b.fetchSecurityMilestone(ctx, "99915010")
		if err != nil {
			t.Fatalf("fetchSecurityMilestone: %v", err)
		}
		if rm != nil {
			t.Errorf("got %+v, want nil milestone", rm)
		}
	})

	t.Run("empty milestone", func(t *testing.T) {
		rm, err := deps.buildTasks.fetchSecurityMilestone(ctx, "")
		if err != nil {
			t.Fatalf("fetchSecurityMilestone(%q): %v", "", err)
		}
		if rm != nil {
			t.Errorf("fetchSecurityMilestone(%q): got %+v, want nil milestone", "", rm)
		}
		if rm, err := deps.buildTasks.fetchSecurityMilestone(ctx, "0"); err == nil {
			t.Errorf("fetchSecurityMilestone(%q): got %+v, want error for nonexistent milestone", "0", rm)
		}
	})

	t.Run("happy", func(t *testing.T) {
		rm, err := deps.buildTasks.fetchSecurityMilestone(ctx, "99915010")
		if err != nil {
			t.Fatalf("fetchSecurityMilestone: %v", err)
		}
		if len(rm.Patches) != 1 {
			t.Fatalf("got %d patches, want 1", len(rm.Patches))
		}
		if got := rm.Patches[0].Track; got != relmeta.Private {
			t.Errorf("patch track = %q, want %q", got, relmeta.Private)
		}
	})

	t.Run("read branch head error", func(t *testing.T) {
		// A private Gerrit with no security-metadata repo makes ReadBranchHead fail.
		b := *deps.buildTasks
		b.PrivateGerritClient = task.NewFakeGerrit(t, deps.goRepo)
		_, err := b.fetchSecurityMilestone(ctx, "99915010")
		if err == nil {
			t.Fatal("fetchSecurityMilestone with no security-metadata repo: got nil error")
		}
	})

	t.Run("read file error", func(t *testing.T) {
		// A milestone number with no corresponding YAML file makes ReadFile fail.
		_, err := deps.buildTasks.fetchSecurityMilestone(ctx, "00000000")
		if err == nil {
			t.Fatal("fetchSecurityMilestone for a missing milestone file: got nil error")
		}
	})

	t.Run("unmarshal error", func(t *testing.T) {
		// Commit a milestone file with invalid YAML to drive the Unmarshal error.
		if _, err := privGerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
			Project: "security-metadata",
			Branch:  "main",
		}, nil, map[string]string{
			filepath.Join("data", "milestones", "12345678.yaml"): "\tnot: [valid yaml",
		}); err != nil {
			t.Fatal(err)
		}
		_, err := deps.buildTasks.fetchSecurityMilestone(ctx, "12345678")
		if err == nil {
			t.Fatal("fetchSecurityMilestone for invalid YAML: got nil error")
		}
		if !strings.Contains(err.Error(), "YAML unmarshal") {
			t.Errorf("error = %v, want a YAML unmarshal error", err)
		}
	})
}

func TestComputeSecurityBranchInfoWithRC(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := newMinorCoalesceTestDeps(t, true)
		ctx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "branchinfo"}}

		// The base deps set up go1.25 and go1.26 release branches but no go1.27. Add a
		// go1.27 release branch on the public repo so ReadBranchHead succeeds and the
		// RC path fires (currentMajor=26 -> looks for release-branch.go1.27).
		base, err := deps.gerrit.ReadBranchHead(deps.ctx, "go", "release-branch.go1.26")
		if err != nil {
			t.Fatal(err)
		}
		deps.goRepo.Branch("release-branch.go1.27", base)

		bi, err := computeSecurityBranchInfo(ctx, deps.versionTasks, 26, mustGetNextMinors(t, deps))
		if err != nil {
			t.Fatal(err)
		}

		nextRC, err := deps.versionTasks.GetNextVersion(deps.ctx, 27, task.KindRC)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(bi.CheckpointName, nextRC+"-") {
			t.Errorf("checkpoint name = %q, want it prefixed with %q", bi.CheckpointName, nextRC+"-")
		}
		wantRCBranch := "release-branch." + nextRC
		if len(bi.PublicReleaseBranches) == 0 || bi.PublicReleaseBranches[0] != wantRCBranch {
			t.Errorf("public release branches = %v, want %q first", bi.PublicReleaseBranches, wantRCBranch)
		}
	})
}

func TestCheckPrivateChangesLint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		ctx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "lint"}}

		privGerrit.AddChange("go", "1234", nil, "crypto/tls: fix\n\nFixes CVE-1985-0703\nFixes golang/go#1")

		rm := &relmeta.ReleaseMilestone{
			Patches: []*relmeta.SecurityPatch{{
				Track:       relmeta.Private,
				Package:     "crypto/tls",
				Changelists: []string{"https://go-internal-review.git.corp.google.com/c/go/+/1234"},
			}},
		}
		_, err := deps.buildTasks.checkPrivateChanges(ctx, rm)
		if err == nil {
			t.Fatal("checkPrivateChanges with metadata in the commit message: got nil error")
		}
		for _, want := range []string{"must not contain a CVE reference", "must not contain a GitHub issue reference"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}

		privGerrit.AddChange("go", "1234", nil, "crypto/tls: fix something\n\nNo references here.")
		if _, err := deps.buildTasks.checkPrivateChanges(ctx, rm); err != nil {
			t.Errorf("checkPrivateChanges with a clean message: %v", err)
		}
	})
}

// mustGetNextMinors returns the next minor versions for the 26 and 25 series.
func mustGetNextMinors(t *testing.T, deps *releaseTestDeps) []string {
	t.Helper()
	next, err := deps.versionTasks.GetNextMinorVersions(deps.ctx, []int{26, 25})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mustSecuritySetup(t *testing.T, deps *releaseTestDeps, privGerrit *task.FakeGerrit) (*workflow.TaskContext, securityBranchInfo, []*task.PatchChanges) {
	t.Helper()
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: t.Name()}}
	bi, err := computeSecurityBranchInfo(taskCtx, deps.versionTasks, 26, mustGetNextMinors(t, deps))
	if err != nil {
		t.Fatal(err)
	}
	var cls []*gerrit.ChangeInfo
	for _, num := range []string{"1234", "5678"} {
		ci, err := privGerrit.GetChange(deps.ctx, num)
		if err != nil {
			t.Fatalf("GetChange(%s): %v", num, err)
		}
		cls = append(cls, ci)
	}
	return taskCtx, bi, []*task.PatchChanges{{Patch: coalesceRM().Patches[0], Changes: cls}}
}

// minorReleaseParams returns the parameters needed to start the workflow built
// by createMinorReleaseWorkflow(.., 25, 26). Each minor's sub-workflow
// contributes its own prefixed "Targets to skip testing" parameter.
func minorReleaseParams() map[string]any {
	return map[string]any{
		"Release Coordinator Usernames (optional)":               []string(nil),
		task.SecurityReviewersParameter.Name:                     []string{"reviewer@google.com"},
		task.SecurityMilestoneParameter.Name:                     "99915010",
		"Go 1.26: Targets to skip testing (or 'all') (optional)": []string{"all"},
		"Go 1.25: Targets to skip testing (or 'all') (optional)": []string{"all"},
	}
}

func TestAdvisoryTestsFail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps := newReleaseTestDeps(t, "go1.26.0", 26, "go1.26.1")
		deps.buildBucket.FailBuilds = append(deps.buildBucket.FailBuilds, "linux-amd64-longtest")
		defaultApprove := deps.buildTasks.ApproveAction
		var testApprovals atomic.Int32
		deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
			if strings.Contains(ctx.TaskName, "Run advisory") {
				testApprovals.Add(1)
				return nil
			}
			return defaultApprove(ctx)
		}

		// Run the release.
		wd := workflow.New(workflow.ACL{})
		v := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, 26, task.KindMinor, workflow.Slice[string]())
		workflow.Output(wd, "Published Go version", v)

		w := workflowtest.Start(t, wd, map[string]any{
			"Targets to skip testing (or 'all') (optional)": []string(nil),
		})
		workflowtest.Run(t, deps.ctx, w, nil)
		if testApprovals.Load() != 1 {
			t.Errorf("failed advisory builder didn't need approval")
		}
	})
}

// makeScript pretends to be make.bash. It creates a fake go command that
// knows how to fake the commands the release process runs.
const makeScript = `#!/bin/bash -eu

GO=../
VERSION=$(head -n 1 $GO/VERSION)

if [[ $# >0 && $1 == "-distpack" ]]; then
	mkdir -p $GO/pkg/distpack
	tmp=$(mktemp $TMPDIR/buildrel.XXXXXXXX).tar
	(cd $GO/.. && find . | xargs touch -t 202301010000 && find . | xargs chmod 0777 && tar cf $tmp go)
	# On macOS, tar -czf puts a timestamp in the gzip header. Do it ourselves with --no-name to suppress it.
	gzip --no-name $tmp
	mv $tmp.gz $GO/pkg/distpack/$VERSION.src.tar.gz
fi

mkdir -p $GO/bin

cat <<'EOF' >$GO/bin/go
#!/bin/bash -eu
case "$@" in
"install -race")
	# Installing the race mode stdlib. Doesn't matter where it's run.
	mkdir -p $(dirname $0)/../pkg/something_orother/
	touch $(dirname $0)/../pkg/something_orother/race.a
	;;
"tool dist test -compile-only")
	# Testing with -compile-only flag set.
	exit 0
	;;
*)
	echo "unexpected command $@"
	exit 1
	;;
esac
EOF
chmod 0755 $GO/bin/go

# We don't know what GOOS_GOARCH we're "building" for, write some junk for
# versimilitude.
mkdir -p $GO/tool/something_orother/
touch $GO/tool/something_orother/compile

if [[ $# >0 && $1 == "-distpack" ]]; then
	case $GOOS in
	"windows")
		tmp=$(mktemp $TMPDIR/buildrel.XXXXXXXX).zip
		# The zip command isn't installed on our buildlets. Python is.
		(cd $GO/.. && find . | xargs touch -t 202301010000 && find . | xargs chmod 0777 && python3 -m zipfile -c $tmp go/)
		mv $tmp $GO/pkg/distpack/$VERSION-$GOOS-$GOARCH.zip
		;;
	*)
		tmp=$(mktemp $TMPDIR/buildrel.XXXXXXXX).tar
		(cd $GO/.. && find . | xargs touch -t 202301010000 && find . | xargs chmod 0777 && tar cf $tmp go)
		# On macOS, tar -czf puts a timestamp in the gzip header. Do it ourselves with --no-name to suppress it.
		gzip --no-name $tmp
		mv $tmp.gz $GO/pkg/distpack/$VERSION-$GOOS-$GOARCH.tar.gz
		;;
	esac

	MODVER=v0.0.1-$VERSION.$GOOS-$GOARCH
	echo "module golang.org/toolchain" > $GO/pkg/distpack/$MODVER.mod
	echo -e "{\"Version\":\"$MODVER\", \"Timestamp\":\"fake timestamp\"}" > $GO/pkg/distpack/$MODVER.info
	MODTMP=$(mktemp -d $TMPDIR/buildrel.XXXXXXXX)
	MODDIR=$MODTMP/golang.org/toolchain@$MODVER
	mkdir -p $MODDIR
	cp -r $GO $MODDIR
	tmp=$(mktemp -d $TMPDIR/buildrel.XXXXXXXX).zip
	(cd $MODTMP && find . | xargs touch -t 202301010000 && find . | xargs chmod 0777 && python3 -m zipfile -c $tmp .)
	mv $tmp $GO/pkg/distpack/$MODVER.zip
fi
`

// allScript pretends to be all.bash. It's hardcoded
// to fail on GOOS=js and pass on all other builders.
const allScript = `#!/bin/bash -eu

echo "I'm a test! :D"

if [[ ${GOOS:-} = "js" ]]; then
  echo "Oh no, JavaScript is broken."
  exit 1
fi

exit 0
`

// raceScript pretends to be race.bash.
const raceScript = `#!/bin/bash -eu

echo "I'm a race test. Zoom zoom!"

exit 0
`

var goFiles = map[string]string{
	"src/make.bash": makeScript,
	"src/make.bat":  makeScript,
	"src/all.bash":  allScript,
	"src/all.bat":   allScript,
	"src/race.bash": raceScript,
	"src/race.bat":  raceScript,
}

func checkFile(t *testing.T, client *http.Client, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, check func(*testing.T, []byte)) {
	t.Helper()
	resolvedName := filename
	if files != nil {
		f, ok := files[filename]
		if !ok {
			t.Fatalf("file %q not published", filename)
		}
		if diff := cmp.Diff(meta, f, cmpopts.IgnoreFields(task.WebsiteFile{}, "Filename", "Version", "ChecksumSHA256", "Size")); diff != "" {
			t.Errorf("file %v metadata mismatch (-want +got):\n%v", filename, diff)
		}
		resolvedName = f.Filename
	}
	body := fetch(t, client, dlURL+"/"+resolvedName)
	check(t, body)
}

func fetch(t *testing.T, client *http.Client, url string) []byte {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("getting %v: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getting %v: non-200 OK status code %v", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %v: %v", url, err)
	}
	return b
}

func checkContents(t *testing.T, client *http.Client, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, contents string) {
	checkFile(t, client, dlURL, files, filename, meta, func(t *testing.T, b []byte) {
		if got, want := string(b), contents; !strings.Contains(got, want) {
			t.Errorf("%v contains %q, want %q", filename, got, want)
		}
	})
}

func checkTGZ(t *testing.T, client *http.Client, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, contents map[string]string) {
	checkFile(t, client, dlURL, files, filename, meta, func(t *testing.T, b []byte) {
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
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			delete(contents, h.Name)
			if got := string(b); !strings.Contains(got, want) {
				t.Errorf("%v contains %q, want %q", filename, got, want)
			}
		}
		if len(contents) != 0 {
			t.Errorf("not all files were found: missing %v", contents)
		}
	})
}

func checkZip(t *testing.T, client *http.Client, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, contents map[string]string) {
	checkFile(t, client, dlURL, files, filename, meta, func(t *testing.T, b []byte) {
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
			b, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			delete(contents, f.Name)
			if got := string(b); !strings.Contains(got, want) {
				t.Errorf("%v contains %q, want %q", filename, got, want)
			}
		}
		if len(contents) != 0 {
			t.Errorf("not all files were found: missing %v", contents)
		}
	})
}

type reviewerCheckGerrit struct {
	wantReviewers []string
	*task.FakeGerrit
}

func (g *reviewerCheckGerrit) CreateAutoSubmitChange(ctx *workflow.TaskContext, input gerrit.ChangeInput, reviewers []string, contents map[string]string) (string, error) {
	if diff := cmp.Diff(g.wantReviewers, reviewers, cmpopts.EquateEmpty()); diff != "" {
		return "", fmt.Errorf("unexpected reviewers for CL: %v", diff)
	}
	return g.FakeGerrit.CreateAutoSubmitChange(ctx, input, reviewers, contents)
}

type taskStartTracker struct { // TODO(nealpatel): Fold into internal/workflowtest
	started sync.Map
	workflow.Listener
}

func (l *taskStartTracker) TaskStateChanged(id uuid.UUID, taskID string, st *workflow.TaskState) error {
	if st.Started && !st.Finished {
		l.started.Store(st.Name, true)
	}
	return l.Listener.TaskStateChanged(id, taskID, st)
}

func fakeCDNLoad(ctx context.Context, t *testing.T, from, to string) {
	fromFS, toFS := gcsfs.DirFS(from), gcsfs.DirFS(to)
	seen := map[string]bool{}
	periodicallyDo(ctx, t, 100*time.Millisecond, func() error {
		files, err := fs.ReadDir(fromFS, ".")
		if err != nil {
			return err
		}
		for _, f := range files {
			if seen[f.Name()] {
				continue
			}
			seen[f.Name()] = true
			contents, err := fs.ReadFile(fromFS, f.Name())
			if err != nil {
				return err
			}
			if err := gcsfs.WriteFile(toFS, f.Name(), contents); err != nil {
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

func TestCreateInternalReleaseBranchesOpenCherryPicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		seedRiders(privGerrit)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		branches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("first createInternalReleaseBranches: %v", err)
		}
		if len(branches) == 0 {
			t.Fatal("first run created no internal release branches")
		}

		freshCPs, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, branches, cls, coalesceBackports())
		if err != nil {
			t.Fatalf("createSecurityCherryPicks: %v", err)
		}
		wantCPCount := len(cls[0].Changes) * len(branches)
		if got := len(freshCPs); got != wantCPCount {
			t.Fatalf("fresh cherry-picks: got %d, want %d", got, wantCPCount)
		}

		reusedHeads := map[string]string{}
		for _, b := range branches {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
			if err != nil {
				t.Fatalf("reading head of %s: %v", b, err)
			}
			reusedHeads[b] = head
		}

		branches2, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("restart createInternalReleaseBranches with open CPs: %v", err)
		}
		if len(branches2) != len(branches) {
			t.Fatalf("branch count mismatch: first=%d, restart=%d", len(branches), len(branches2))
		}

		for _, b := range branches2 {
			head, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
			if err != nil {
				t.Fatalf("reading head of %s after restart: %v", b, err)
			}
			if head != reusedHeads[b] {
				t.Errorf("branch %s head changed after restart: got %s, want %s", b, head, reusedHeads[b])
			}
		}

		restartCPs, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, branches2, cls, coalesceBackports())
		if err != nil {
			t.Fatalf("restart createSecurityCherryPicks: %v", err)
		}
		if got := len(restartCPs); got != wantCPCount {
			t.Fatalf("restart cherry-picks: got %d, want %d", got, wantCPCount)
		}

		freshNums := map[int]bool{}
		for _, cp := range freshCPs {
			freshNums[cp.ChangeNumber] = true
		}
		for _, cp := range restartCPs {
			if !freshNums[cp.ChangeNumber] {
				t.Errorf("restart returned unknown cherry-pick CL %d; want reuse of existing CL", cp.ChangeNumber)
			}
		}

		for _, b := range branches2 {
			existing, err := privGerrit.QueryChanges(deps.ctx,
				fmt.Sprintf("project:go branch:%s -is:abandoned", b))
			if err != nil {
				t.Fatalf("QueryChanges for %s: %v", b, err)
			}
			for _, ci := range existing {
				if !freshNums[ci.ChangeNumber] {
					t.Errorf("orphaned CL %d on branch %s after restart; fixed-name strategy must not orphan cherry-picks", ci.ChangeNumber, b)
				}
			}
		}
	})
}

func TestCreateSecurityCherryPicksPartialDedup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		seedRiders(privGerrit)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		releaseBranches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatal(err)
		}

		// Pre-seed a cherry-pick for the first CL onto the first branch only.
		// This simulates a partial prior run.
		firstBranch := releaseBranches[0]
		preseeded := &gerrit.ChangeInfo{
			ID:           "pre-cp-1",
			ChangeID:     cls[0].Changes[0].ChangeID, // same Change-Id as original
			ChangeNumber: 9999,
			Branch:       firstBranch,
			Submittable:  true,
			Mergeable:    true,
			Status:       "NEW",
		}
		privGerrit.AddChange("go", "pre-cp-1", preseeded, "preseeded cherry-pick")

		cps, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, releaseBranches, cls, coalesceBackports())
		if err != nil {
			t.Fatalf("partial createSecurityCherryPicks: %v", err)
		}
		wantCount := len(cls[0].Changes) * len(releaseBranches)
		if got := len(cps); got != wantCount {
			t.Fatalf("partial cherry-picks: got %d, want %d", got, wantCount)
		}

		// The preseeded cherry-pick must be reused (its ChangeNumber is 9999).
		found := false
		for _, cp := range cps {
			if cp.ChangeNumber == 9999 {
				found = true
				break
			}
		}
		if !found {
			t.Error("preseeded cherry-pick (CL 9999) was not reused")
		}
	})
}

func TestMoveAndRebasePrivateChanges(t *testing.T) {
	workflowtest.Subtest(t, "fresh", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		moved, err := deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}
		if len(moved[0].Changes) != len(cls[0].Changes) {
			t.Fatalf("got %d CLs, want %d", len(moved[0].Changes), len(cls[0].Changes))
		}
		for _, ci := range moved[0].Changes {
			if ci.Branch != checkpoint.Branch {
				t.Errorf("CL %d branch = %q, want %q", ci.ChangeNumber, ci.Branch, checkpoint.Branch)
			}
		}
	})

	workflowtest.Subtest(t, "restart_already_moved", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		// Simulate the CLs having already been moved to the checkpoint branch
		// by a prior run, so moveAndRebasePrivateChanges sees them as already
		// on the correct branch and tolerates the 409.
		for _, ci := range cls[0].Changes {
			ci.Branch = checkpoint.Branch
		}

		moved, err := deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges on already-moved CLs: %v", err)
		}
		if len(moved[0].Changes) != len(cls[0].Changes) {
			t.Fatalf("got %d CLs, want %d", len(moved[0].Changes), len(cls[0].Changes))
		}
	})

	workflowtest.Subtest(t, "restart_already_merged", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		// Simulate CL 1234 having already been merged by a prior run,
		// so moveAndRebasePrivateChanges sees it as merged and tolerates the 409.
		cls[0].Changes[0].Status = gerrit.ChangeStatusMerged
		cls[0].Changes[0].Submittable = false

		moved, err := deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges with merged CL: %v", err)
		}
		if len(moved[0].Changes) != len(cls[0].Changes) {
			t.Fatalf("got %d CLs, want %d", len(moved[0].Changes), len(cls[0].Changes))
		}
		for _, ci := range moved[0].Changes {
			if ci.ChangeNumber == 1234 && ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("merged CL 1234 status = %q, want %q", ci.Status, gerrit.ChangeStatusMerged)
			}
		}
	})
}

func TestSubmitPrivateChanges(t *testing.T) {
	workflowtest.Subtest(t, "happy", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		cls, err = deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}

		submitted, err := task.SubmitPrivateChanges(taskCtx, privGerrit, "go", cls)
		if err != nil {
			t.Fatalf("submitPrivateChanges: %v", err)
		}
		if len(submitted[0].Changes) != len(cls[0].Changes) {
			t.Fatalf("got %d CLs, want %d", len(submitted[0].Changes), len(cls[0].Changes))
		}
		for _, ci := range submitted[0].Changes {
			if ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("CL %d status = %q, want %q", ci.ChangeNumber, ci.Status, gerrit.ChangeStatusMerged)
			}
		}
	})

	workflowtest.Subtest(t, "already_merged_skip", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		cls, err = deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}

		// Simulate CL 1234 having been merged by a prior run. Update both the
		// canonical state (via GetChange's returned pointer) and the local slice
		// so submitPrivateChanges sees the CL as already merged.
		merged1234, err := privGerrit.GetChange(deps.ctx, "1234")
		if err != nil {
			t.Fatalf("GetChange(1234): %v", err)
		}
		merged1234.Status = gerrit.ChangeStatusMerged
		merged1234.Submittable = false
		cls[0].Changes[0].Status = gerrit.ChangeStatusMerged
		cls[0].Changes[0].Submittable = false

		submitted, err := task.SubmitPrivateChanges(taskCtx, privGerrit, "go", cls)
		if err != nil {
			t.Fatalf("submitPrivateChanges with pre-merged CL: %v", err)
		}
		if len(submitted[0].Changes) != len(cls[0].Changes) {
			t.Fatalf("got %d CLs, want %d", len(submitted[0].Changes), len(cls[0].Changes))
		}
		for _, ci := range submitted[0].Changes {
			if ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("CL %d status = %q, want %q", ci.ChangeNumber, ci.Status, gerrit.ChangeStatusMerged)
			}
		}
	})
}

func TestCreateVulnReportsStdCmd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := newMinorCoalesceTestDeps(t, true)

		vulndbRepo := task.NewFakeRepo(t, "vulndb")
		vulndbRepo.CommitOnBranch("master", map[string]string{"README": "vulndb"})
		pubGerrit := task.NewFakeGerrit(t, vulndbRepo)
		deps.buildTasks.GerritClient = pubGerrit

		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "vu1"}}

		const announceURL = "https://groups.google.com/g/golang-announce/c/test-minor"

		rm := &relmeta.ReleaseMilestone{
			Patches: []*relmeta.SecurityPatch{
				{
					ID:             40027190,
					Track:          relmeta.Private,
					Package:        "crypto/tls",
					Changelists:    []string{"https://go-internal-review.git.corp.google.com/c/go/+/1234"},
					TargetReleases: []string{"go1.25.1", "go1.26.1"},
					ReleaseNote:    "crypto/tls: bad handshake causes panic.\n\nA specially crafted ClientHello triggers a nil pointer dereference.",
					GitHubIssueID:  99999,
					VulnReportID:   "GO-2026-9001",
					CVE:            "CVE-2026-9001",
					Credits:        []string{"Alice"},
				},
				{
					ID:             40027191,
					Track:          relmeta.Private,
					Package:        "cmd/go",
					Changelists:    []string{"https://go-internal-review.git.corp.google.com/c/go/+/5678"},
					TargetReleases: []string{"go1.26.1"},
					ReleaseNote:    "cmd/go: module download executes arbitrary code.\n\nA crafted go.sum allows execution of untrusted binaries.",
					GitHubIssueID:  99998,
					VulnReportID:   "GO-2026-9002",
					CVE:            "CVE-2026-9002",
					Credits:        []string{"Bob"},
				},
			},
		}

		wantReviewers := []string{"vuln-reviewer-a@google.com", "vuln-reviewer-b@google.com"}
		changeID, err := deps.buildTasks.createVulnReports(taskCtx, rm, announceURL, wantReviewers)
		if err != nil {
			t.Fatalf("createVulnReports: %v", err)
		}
		if changeID == "" {
			t.Fatal("createVulnReports returned empty change ID")
		}
		if !reflect.DeepEqual(pubGerrit.LastReviewers, wantReviewers) {
			t.Errorf("vulndb reviewers = %v, want %v", pubGerrit.LastReviewers, wantReviewers)
		}

		vulndbHead, err := pubGerrit.ReadBranchHead(deps.ctx, "vulndb", "master")
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range rm.Patches {
			reportPath := path.Join("data", "reports", p.VulnReportID+".yaml")
			b, err := pubGerrit.ReadFile(deps.ctx, "vulndb", vulndbHead, reportPath)
			if err != nil {
				t.Fatalf("reading %s: %v", reportPath, err)
			}

			if !bytes.Contains(b, []byte(announceURL)) {
				t.Errorf("report %s does not contain announcement URL %s", p.VulnReportID, announceURL)
			}

			var vr report.Report
			if err := yaml.Unmarshal(b, &vr); err != nil {
				t.Fatalf("unmarshal %s: %v", reportPath, err)
			}

			if len(vr.Modules) != 1 {
				t.Errorf("%s: got %d modules, want 1", p.VulnReportID, len(vr.Modules))
				continue
			}
			wantModule := task.VulnModule(p.Package)
			if vr.Modules[0].Module != wantModule {
				t.Errorf("%s: module = %q, want %q", p.VulnReportID, vr.Modules[0].Module, wantModule)
			}

			if vr.Modules[0].VulnerableAt == nil {
				t.Errorf("%s: VulnerableAt is nil", p.VulnReportID)
			}
		}
	})
}

func TestCreateVulnReportsNilMilestone(t *testing.T) {
	deps, _ := newMinorCoalesceTestDeps(t, false)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "vu1-noop"}}

	t.Run("nil milestone", func(t *testing.T) {
		got, err := deps.buildTasks.createVulnReports(taskCtx, nil, "https://example.com", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got change ID %q, want empty", got)
		}
	})

	t.Run("empty patches", func(t *testing.T) {
		got, err := deps.buildTasks.createVulnReports(taskCtx, &relmeta.ReleaseMilestone{}, "https://example.com", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got change ID %q, want empty", got)
		}
	})
}

func TestConvertInternalChangelists(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		pubGerrit := deps.gerrit.FakeGerrit
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "pc1"}}

		privGerrit.AddChange("go", "1234", nil, "crypto/tls: fix something\n\nFixes CVE-1985-0703\nFixes golang/go#1\n\nChange-Id: I0000000000000000000000000000000000000001")
		privGerrit.AddChange("go", "5678", nil, "cmd/compile: fix something else\n\nFixes CVE-1970-0001\nFixes golang/go#2\n\nChange-Id: I0000000000000000000000000000000000000002")
		pubGerrit.AddChange("go", "pub-1", &gerrit.ChangeInfo{
			ID:           "pub-1",
			ChangeID:     "I0000000000000000000000000000000000000001",
			ChangeNumber: 700001,
			Branch:       "master",
			Status:       gerrit.ChangeStatusMerged,
		}, "crypto/tls: fix something\n\nChange-Id: I0000000000000000000000000000000000000001")
		pubGerrit.AddChange("go", "pub-2", &gerrit.ChangeInfo{
			ID:           "pub-2",
			ChangeID:     "I0000000000000000000000000000000000000002",
			ChangeNumber: 700002,
			Branch:       "master",
			Status:       gerrit.ChangeStatusMerged,
		}, "cmd/compile: fix something else\n\nChange-Id: I0000000000000000000000000000000000000002")

		rm, err := deps.buildTasks.fetchSecurityMilestone(taskCtx, "99915010")
		if err != nil {
			t.Fatal(err)
		}
		wantReviewers := []string{"vuln-reviewer-a@google.com"}
		converted, err := deps.buildTasks.convertInternalChangelists(taskCtx, rm, wantReviewers)
		if err != nil {
			t.Fatalf("convertInternalChangelists: %v", err)
		}
		if got, want := converted.Patches[0].Changelists, []string{"https://go.dev/cl/700001", "https://go.dev/cl/700002"}; !reflect.DeepEqual(got, want) {
			t.Errorf("changelists = %v, want %v", got, want)
		}
		if !reflect.DeepEqual(privGerrit.LastReviewers, wantReviewers) {
			t.Errorf("metadata reviewers = %v, want %v", privGerrit.LastReviewers, wantReviewers)
		}
		head, err := privGerrit.ReadBranchHead(deps.ctx, "security-metadata", "main")
		if err != nil {
			t.Fatal(err)
		}
		b, err := privGerrit.ReadFile(deps.ctx, "security-metadata", head, path.Join("data", "milestones", "99915010.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("go-internal-review")) {
			t.Errorf("milestone at head still has private links:\n%s", b)
		}
	})
}

func TestConvertInternalChangelistsEmptyMilestone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, false)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "pc3"}}

		empty := &relmeta.ReleaseMilestone{}
		got, err := deps.buildTasks.convertInternalChangelists(taskCtx, empty, []string{"vuln-reviewer-a@google.com"})
		if err != nil {
			t.Fatal(err)
		}
		if got != empty {
			t.Errorf("milestone = %p, want passthrough of %p", got, empty)
		}
		if privGerrit.LastReviewers != nil {
			t.Errorf("mailed a change with reviewers %v, want none", privGerrit.LastReviewers)
		}
	})
}

func TestConvertInternalChangelistsMissingChangeID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, _ := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "pc2"}}

		rm, err := deps.buildTasks.fetchSecurityMilestone(taskCtx, "99915010")
		if err != nil {
			t.Fatal(err)
		}
		_, err = deps.buildTasks.convertInternalChangelists(taskCtx, rm, nil)
		if err == nil || !strings.Contains(err.Error(), "no Change-Id footer") {
			t.Fatalf("convertInternalChangelists error = %v, want Change-Id footer error", err)
		}
	})
}

func TestSubmitCherryPicks(t *testing.T) {
	workflowtest.Subtest(t, "happy", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		cls, err = deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}

		submitted, err := task.SubmitPrivateChanges(taskCtx, privGerrit, "go", cls)
		if err != nil {
			t.Fatalf("submitPrivateChanges: %v", err)
		}

		internalBranches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, submitted)
		if err != nil {
			t.Fatalf("createInternalReleaseBranches: %v", err)
		}

		cherryPicks, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, internalBranches, submitted, coalesceBackports())
		if err != nil {
			t.Fatalf("createSecurityCherryPicks: %v", err)
		}

		result, err := deps.buildTasks.submitCherryPicks(taskCtx, cherryPicks)
		if err != nil {
			t.Fatalf("submitCherryPicks: %v", err)
		}
		if len(result) == 0 {
			t.Fatal("submitCherryPicks returned empty map")
		}
		for branch, urls := range result {
			if len(urls) == 0 {
				t.Errorf("branch %s has no submitted CL URLs", branch)
			}
			for _, url := range urls {
				if !strings.Contains(url, "go-internal-review.git.corp.google.com") {
					t.Errorf("branch %s URL %q does not look like a private CL URL", branch, url)
				}
			}
		}

		for _, cp := range cherryPicks {
			ci, err := privGerrit.GetChange(deps.ctx, cp.ID)
			if err != nil {
				t.Fatalf("GetChange(%s): %v", cp.ID, err)
			}
			if ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("cherry-pick %s status = %q, want %q", cp.ID, ci.Status, gerrit.ChangeStatusMerged)
			}
		}
	})

	workflowtest.Subtest(t, "already_merged_skip", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		cls, err = deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}

		submitted, err := task.SubmitPrivateChanges(taskCtx, privGerrit, "go", cls)
		if err != nil {
			t.Fatalf("submitPrivateChanges: %v", err)
		}

		internalBranches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, submitted)
		if err != nil {
			t.Fatalf("createInternalReleaseBranches: %v", err)
		}

		cherryPicks, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, internalBranches, submitted, coalesceBackports())
		if err != nil {
			t.Fatalf("createSecurityCherryPicks: %v", err)
		}

		for _, cp := range cherryPicks {
			stored, err := privGerrit.GetChange(deps.ctx, cp.ID)
			if err != nil {
				t.Fatalf("GetChange(%s): %v", cp.ID, err)
			}
			stored.Status = gerrit.ChangeStatusMerged
			stored.Submittable = false
			cp.Status = gerrit.ChangeStatusMerged
			cp.Submittable = false
		}

		result, err := deps.buildTasks.submitCherryPicks(taskCtx, cherryPicks)
		if err != nil {
			t.Fatalf("submitCherryPicks with pre-merged CPs: %v", err)
		}
		if len(result) == 0 {
			t.Fatal("submitCherryPicks returned empty map")
		}
	})
}

func TestCheckPrivateChangesErrors(t *testing.T) {
	workflowtest.Subtest(t, "get_change_error", func(t *testing.T) {
		deps, _ := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "check-err"}}

		deps.buildTasks.PrivateGerritClient = task.NewFakeGerrit(t, task.NewFakeRepo(t, "empty"))

		rm := &relmeta.ReleaseMilestone{
			Patches: []*relmeta.SecurityPatch{{
				Track:       relmeta.Private,
				Changelists: []string{"https://go-internal-review.git.corp.google.com/c/go/+/9999"},
			}},
		}
		_, err := deps.buildTasks.checkPrivateChanges(taskCtx, rm)
		if err == nil {
			t.Fatal("expected error from GetChange on missing CL")
		}
	})

	workflowtest.Subtest(t, "not_submittable", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "check-notsub"}}

		stored, err := privGerrit.GetChange(deps.ctx, "1234")
		if err != nil {
			t.Fatal(err)
		}
		stored.Submittable = false

		rm := &relmeta.ReleaseMilestone{
			Patches: []*relmeta.SecurityPatch{{
				Track:       relmeta.Private,
				Changelists: []string{"https://go-internal-review.git.corp.google.com/c/go/+/1234"},
			}},
		}
		_, err = deps.buildTasks.checkPrivateChanges(taskCtx, rm)
		if err == nil {
			t.Fatal("expected error for non-submittable CL")
		}
		if !strings.Contains(err.Error(), "not submittable") {
			t.Errorf("error = %v, want 'not submittable'", err)
		}
	})
}

func TestMoveAndRebasePrivateChangesErrors(t *testing.T) {
	workflowtest.Subtest(t, "get_change_error", func(t *testing.T) {
		deps, _ := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &workflowtest.Logger{T: t, Task: "move-err"}}

		fakeCL := &gerrit.ChangeInfo{
			ID:          "nonexistent",
			ChangeID:    "nonexistent",
			Branch:      "public",
			Submittable: true,
		}

		_, err := deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, task.Checkpoint{Branch: "whatever"}, []*task.PatchChanges{{Patch: coalesceRM().Patches[0], Changes: []*gerrit.ChangeInfo{fakeCL}}})
		if err == nil {
			t.Fatal("expected error for nonexistent CL")
		}
	})
}

func TestSubmitPrivateChangesError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		cls, err = deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}

		for _, ci := range cls[0].Changes {
			stored, err := privGerrit.GetChange(deps.ctx, ci.ID)
			if err != nil {
				t.Fatalf("GetChange(%s): %v", ci.ID, err)
			}
			stored.Submittable = false
		}

		errCtx, cancel := context.WithTimeout(deps.ctx, 2*time.Second)
		defer cancel()
		errTaskCtx := &workflow.TaskContext{Context: errCtx, Logger: &workflowtest.Logger{T: t, Task: "submit-err"}}

		_, err = task.SubmitPrivateChanges(errTaskCtx, privGerrit, "go", cls)
		if err == nil {
			t.Fatal("expected error from submitPrivateChanges with non-submittable CLs")
		}
	})
}

func TestCreateInternalReleaseBranchesError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)
		bi.PublicReleaseBranches = []string{"release-branch.go1.99"}

		_, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err == nil {
			t.Fatal("expected error for nonexistent release branch")
		}
	})
}

func TestCreateSecurityCherryPicksConflictError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		seedRiders(privGerrit)
		taskCtx, bi, cls := mustSecuritySetup(t, deps, privGerrit)

		releaseBranches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
		if err != nil {
			t.Fatal(err)
		}

		privGerrit.AddChange("go", "1234", &gerrit.ChangeInfo{
			ID:                   "1234",
			ChangeID:             "1234",
			ChangeNumber:         1234,
			Branch:               "public",
			Submittable:          true,
			Mergeable:            true,
			ContainsGitConflicts: true,
		}, "crypto/tls: fix something\n\nFixes CVE-1985-0703\nFor golang/go#70001")

		_, err = deps.buildTasks.createSecurityCherryPicks(taskCtx, releaseBranches, cls, coalesceBackports())
		if err == nil {
			t.Fatal("expected error from cherry-pick conflict")
		}
		if !strings.Contains(err.Error(), "merge conflicts") {
			t.Errorf("error = %v, want 'merge conflicts'", err)
		}
	})
}

func TestPublicizeErrors(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}

	workflowtest.Subtest(t, "public_head_mismatch", func(t *testing.T) {
		build, _, _, _, securityCommit := newPublicizeTestDeps(t)
		taskCtx := &workflow.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: "pub-mismatch"}}

		fakeOldHead := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		_, err := build.publicizePrivateSecurityCLs(taskCtx,
			"go1.26.1", "release-branch.go1.26", fakeOldHead, securityCommit, nil)
		if err == nil {
			t.Fatal("expected error for public head mismatch")
		}
		if !strings.Contains(err.Error(), "restart the release workflow") {
			t.Errorf("error = %v, want mention of restarting workflow", err)
		}
	})

	workflowtest.Subtest(t, "private_head_mismatch", func(t *testing.T) {
		build, _, _, base, _ := newPublicizeTestDeps(t)
		taskCtx := &workflow.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: "priv-mismatch"}}

		fakeOldCommit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		_, err := build.publicizePrivateSecurityCLs(taskCtx,
			"go1.26.1", "release-branch.go1.26", base, fakeOldCommit, nil)
		if err == nil {
			t.Fatal("expected error for private head mismatch")
		}
		if !strings.Contains(err.Error(), "restart the release workflow") {
			t.Errorf("error = %v, want mention of restarting workflow", err)
		}
	})

	workflowtest.Subtest(t, "public_branch_read_error", func(t *testing.T) {
		build, _, _, _, securityCommit := newPublicizeTestDeps(t)
		taskCtx := &workflow.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: "pub-read-err"}}

		build.GerritClient = task.NewFakeGerrit(t, task.NewFakeRepo(t, "empty"))

		_, err := build.publicizePrivateSecurityCLs(taskCtx,
			"go1.26.1", "release-branch.go1.26", "anything", securityCommit, nil)
		if err == nil {
			t.Fatal("expected error for missing public branch")
		}
		if !strings.Contains(err.Error(), "reading public branch head") {
			t.Errorf("error = %v, want mention of reading public branch head", err)
		}
	})

	workflowtest.Subtest(t, "private_branch_read_error", func(t *testing.T) {
		build, _, _, base, _ := newPublicizeTestDeps(t)
		taskCtx := &workflow.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: "priv-read-err"}}

		build.PrivateGerritClient = task.NewFakeGerrit(t, task.NewFakeRepo(t, "empty"))

		_, err := build.publicizePrivateSecurityCLs(taskCtx,
			"go1.26.1", "release-branch.go1.26", base, "anything", nil)
		if err == nil {
			t.Fatal("expected error for missing private branch")
		}
		if !strings.Contains(err.Error(), "reading private branch head") {
			t.Errorf("error = %v, want mention of reading private branch head", err)
		}
	})

	workflowtest.Subtest(t, "empty_security_commit_no_error", func(t *testing.T) {
		build, _, _, base, _ := newPublicizeTestDeps(t)
		taskCtx := &workflow.TaskContext{Context: context.Background(), Logger: &workflowtest.Logger{T: t, Task: "pub-noop"}}

		cls, err := build.publicizePrivateSecurityCLs(taskCtx,
			"go1.26.1", "release-branch.go1.26", base, "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cls) != 0 {
			t.Errorf("got %d CL IDs, want 0", len(cls))
		}
	})
}

func TestMoveAndRebaseRebaseSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		taskCtx := &workflow.TaskContext{Context: ctx, Logger: &workflowtest.Logger{T: t, Task: "rebase-success"}}

		pubRepo := task.NewFakeRepo(t, "go")
		base := pubRepo.Commit(map[string]string{"README": "hello"})
		pubRepo.Branch("public", base)

		privGerrit := task.NewFakeGerrit(t, pubRepo)

		privGerrit.AddChange("go", "rebase-cl", &gerrit.ChangeInfo{
			ID:           "rebase-cl",
			ChangeID:     "rebase-cl",
			ChangeNumber: 4242,
			Branch:       "public",
			Submittable:  true,
			Mergeable:    true,
		}, "test: rebase target")

		pubRepo.CommitOnBranch("public", map[string]string{"advance.txt": "advance"})

		newHead, err := privGerrit.ReadBranchHead(ctx, "go", "public")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := privGerrit.CreateBranch(ctx, "go", "checkpoint-rebase-test", gerrit.BranchInput{Revision: newHead}); err != nil {
			t.Fatal(err)
		}

		build := &BuildReleaseTasks{
			PrivateGerritClient:  privGerrit,
			PrivateGerritProject: "go",
		}

		ci, err := privGerrit.GetChange(ctx, "rebase-cl")
		if err != nil {
			t.Fatal(err)
		}

		moved, err := build.moveAndRebasePrivateChanges(taskCtx, task.Checkpoint{Branch: "checkpoint-rebase-test"}, []*task.PatchChanges{{
			Patch: &relmeta.SecurityPatch{
				ID:            1,
				Track:         relmeta.Private,
				GitHubIssueID: 70001,
				CVE:           "CVE-1985-0703",
				Changelists:   []string{"https://go-internal-review.git.corp.google.com/c/go/+/4242"},
			},
			Changes: []*gerrit.ChangeInfo{ci},
		}})
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}
		if len(moved) != 1 || len(moved[0].Changes) != 1 {
			t.Fatalf("unexpected result shape: %v", moved)
		}
		if moved[0].Changes[0].Branch != "checkpoint-rebase-test" {
			t.Errorf("CL branch = %q, want %q", moved[0].Changes[0].Branch, "checkpoint-rebase-test")
		}
	})
}
