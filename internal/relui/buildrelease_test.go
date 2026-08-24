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
	"net/http/httptest"
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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal"
	"golang.org/x/build/internal/gcsfs"
	"golang.org/x/build/internal/releasetargets"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
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

	t.Run("minor", func(t *testing.T) {
		testRelease(t, "go1.26", 26, "go1.26.1", task.KindMinor)
	})
	t.Run("beta", func(t *testing.T) {
		testRelease(t, "go1.26", 27, "go1.27beta1", task.KindBeta)
	})
	t.Run("rc", func(t *testing.T) {
		testRelease(t, "go1.26", 27, "go1.27rc1", task.KindRC)
	})
	t.Run("major", func(t *testing.T) {
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
	t.Run("success", func(t *testing.T) {
		testSecurity(t, true)
	})
	t.Run("failure", func(t *testing.T) {
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
}

func newReleaseTestDeps(t *testing.T, previousTag string, major int, wantVersion string) *releaseTestDeps {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}
	if _, err := exec.LookPath("python3"); errors.Is(err, exec.ErrNotFound) {
		// python3 is used in makeScript.
		t.Skip("Requires python3 to be available in PATH.")
	}

	task.AwaitDivisor, workflow.MaxRetries = 100, 1
	t.Cleanup(func() { task.AwaitDivisor, workflow.MaxRetries = 1, 3 })
	ctx, cancel := context.WithCancel(context.Background())

	// Set up a server that will be used to serve inputs to the build.
	bootstrapServer := httptest.NewServer(http.HandlerFunc(serveBootstrap))
	t.Cleanup(bootstrapServer.Close)

	// Set up the fake CDN publishing process.
	servingDir := t.TempDir()
	dlDir := t.TempDir()
	dlServer := httptest.NewServer(http.FileServer(http.FS(os.DirFS(dlDir))))
	t.Cleanup(dlServer.Close)
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
		GerritHTTPClient:         http.DefaultClient,
		Git:                      new(task.Git),
		GCSClient:                nil,
		ScratchFS:                &task.ScratchFS{BaseURL: "file://" + scratchDir},
		SignedURL:                "file://" + scratchDir + "/signed/outputs",
		ServingURL:               "file://" + filepath.ToSlash(servingDir),
		SignService:              task.NewFakeSignService(t, scratchDir+"/signed/outputs"),
		DownloadURL:              dlServer.URL,
		ProxyPrefix:              dlServer.URL,
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
	}
}

func testRelease(t *testing.T, prevTag string, major int, wantVersion string, kind task.ReleaseKind) {
	deps := newReleaseTestDeps(t, prevTag, major, wantVersion)
	wd := workflow.New(workflow.ACL{})

	deps.gerrit.wantReviewers = []string{"heschi", "dmitshur"}
	v := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, major, kind, workflow.Const(deps.gerrit.wantReviewers))
	workflow.Output(wd, "Published Go version", v)

	w, err := workflow.Start(wd, map[string]any{
		"Targets to skip testing (or 'all') (optional)": []string{
			// allScript is intentionally hardcoded to fail on GOOS=js
			// and we confirm here that it's possible to skip that.
			"js-wasm-node18", // Builder used on 1.21 and newer.
			"js-wasm",        // Builder used on 1.20 and older.
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := w.Run(deps.ctx, &verboseListener{t: t, onStall: deps.cancel})
	if err != nil {
		t.Fatal(err)
	}

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

	dlURL, files := deps.buildTasks.DownloadURL, deps.publishedFiles
	for _, f := range deps.publishedFiles {
		wantKind, ok := wantPublishedFiles[f.Filename]
		if !ok {
			t.Errorf("got unexpected published file %q", f.Filename)
		} else if got, want := f.Kind, wantKind; got != want {
			t.Errorf("file %s has unexpected kind: got %q, want %q", f.Filename, got, want)
		}
		delete(wantPublishedFiles, f.Filename)

		checkFile(t, dlURL, files, strings.TrimPrefix(f.Filename, wantVersion+"."), f, func(t *testing.T, b []byte) {
			if got, want := len(b), int(f.Size); got != want {
				t.Errorf("%s size mismatch with metadata: %v != %v", f.Filename, got, want)
			}
			if got, want := fmt.Sprintf("%x", sha256.Sum256(b)), f.ChecksumSHA256; got != want {
				t.Errorf("%s sha256 mismatch with metadata: %q != %q", f.Filename, got, want)
			}
			if got, want := fmt.Sprintf("%x", sha256.Sum256(b)), string(fetch(t, dlURL+"/"+f.Filename+".sha256")); got != want {
				t.Errorf("%s sha256 mismatch with .sha256 file: %q != %q", f.Filename, got, want)
			}
			if strings.HasSuffix(f.Filename, ".tar.gz") {
				if got, want := string(fetch(t, dlURL+"/"+f.Filename+".asc")), fmt.Sprintf("I'm a GPG signature for %x!", sha256.Sum256(b)); got != want {
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
	checkTGZ(t, dlURL, files, "src.tar.gz", task.WebsiteFile{
		OS:   "",
		Arch: "",
		Kind: "source",
	}, map[string]string{
		"go/VERSION":       versionFile,
		"go/src/make.bash": makeScript,
	})
	checkContents(t, dlURL, files, "windows-amd64.msi", task.WebsiteFile{
		OS:   "windows",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm an MSI!\n-signed <Windows>")
	checkTGZ(t, dlURL, files, "linux-amd64.tar.gz", task.WebsiteFile{
		OS:   "linux",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkZip(t, dlURL, files, "windows-amd64.zip", task.WebsiteFile{
		OS:   "windows",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlURL, files, "linux-armv6l.tar.gz", task.WebsiteFile{
		OS:   "linux",
		Arch: "armv6l",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlURL, files, "netbsd-arm.tar.gz", task.WebsiteFile{
		OS:   "netbsd",
		Arch: "arm" + map[int]string{21: "v6l", 22: "v6l"}[major],
		Kind: "archive",
	}, map[string]string{
		"go/VERSION":                        versionFile,
		"go/tool/something_orother/compile": "",
	})
	checkTGZ(t, dlURL, files, "darwin-amd64.tar.gz", task.WebsiteFile{
		OS:   "darwin",
		Arch: "amd64",
		Kind: "archive",
	}, map[string]string{
		"go/VERSION": versionFile,
		"go/bin/go":  "-signed <macOS>",
	})
	checkContents(t, dlURL, files, "darwin-amd64.pkg", task.WebsiteFile{
		OS:   "darwin",
		Arch: "amd64",
		Kind: "installer",
	}, "I'm a PKG! -signed <macOS>")
	modVer := "v0.0.1-" + wantVersion + ".darwin-amd64"
	checkContents(t, dlURL, nil, modVer+".mod", task.WebsiteFile{}, "module golang.org/toolchain")
	checkContents(t, dlURL, nil, modVer+".info", task.WebsiteFile{}, fmt.Sprintf(`"Version":"%v"`, modVer))
	checkZip(t, dlURL, nil, modVer+".zip", task.WebsiteFile{}, map[string]string{
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

	w, err := workflow.Start(wd, map[string]any{
		"Targets to skip testing (or 'all') (optional)": []string{"js-wasm"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if mergeFixes {
		_, err = w.Run(deps.ctx, &verboseListener{t: t})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		runToFailure(t, deps.ctx, w, "Check branch state matches source archive", &verboseListener{t: t})
		return
	}
	checkTGZ(t, deps.buildTasks.DownloadURL, deps.publishedFiles, "src.tar.gz", task.WebsiteFile{
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
		}, "crypto/tls: fix something\n\nFixes CVE-1985-0703\nFixes golang/go#1")
		privGerrit.AddChange("go", "5678", &gerrit.ChangeInfo{
			ID:           "5678",
			ChangeID:     "5678",
			ChangeNumber: 5678,
			Branch:       "public",
			Submittable:  true,
			Mergeable:    true,
		}, "cmd/compile: fix something else\n\nFixes CVE-1970-0001\nFixes #2")
	}

	deps.buildTasks.PrivateGerritClient = privGerrit
	deps.buildTasks.PrivateGerritProject = "go"

	return deps, privGerrit
}

func TestMinorReleaseSecurityCoalesce(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)

	// Approve the confirm step; fail any other approval request.
	deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
		if strings.Contains(ctx.TaskName, "Confirm PRIVATE-track security CLs") {
			return nil
		}
		return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
	}

	// Stop the workflow once both minors' confirm tasks have finished, so we
	// don't have to drive the full build. Canceling on finish (not on approval)
	// keeps the test robust: if the bug makes a confirm task error instead of
	// reaching the approval, the test still unblocks rather than stalling.
	runCtx, stop := context.WithCancel(deps.ctx)
	t.Cleanup(stop)
	listener := &verboseListener{t: t, onStall: stop}

	comm := task.CommunicationTasks{
		SecurityCommunicationTasks: task.SecurityCommunicationTasks{PrivateGerrit: privGerrit},
	}

	publicHeadBefore, err := privGerrit.ReadBranchHead(deps.ctx, "go", "public")
	if err != nil {
		t.Fatalf("reading public head before workflow: %v", err)
	}

	wd, err := createMinorReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, comm, 25, 26)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Start(wd, minorReleaseParams())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Run(runCtx, listener); err != nil && runCtx.Err() == nil {
		t.Fatalf("workflow failed before confirming security CLs: %v", err)
	}

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
}

func TestMinorReleaseCoalesceNoPrivatePatches(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, false)

	// There are no PRIVATE patches, so each release's confirm task takes the "no
	// security fix" path. Allow those approvals; fail any other approval request.
	deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
		if strings.Contains(ctx.TaskName, "Confirm PRIVATE-track security CLs") {
			return nil
		}
		return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
	}

	// Stop the workflow once the coalesce reaches its terminal step, so we can
	// check its side effects without driving the full build. By the time "Create
	// cherry-picks" finishes, the checkpoint and internal release branches would
	// have been created (if the coalesce didn't short-circuit).
	runCtx, stop := context.WithCancel(deps.ctx)
	t.Cleanup(stop)
	listener := &verboseListener{t: t, onStall: stop}

	comm := task.CommunicationTasks{
		SecurityCommunicationTasks: task.SecurityCommunicationTasks{PrivateGerrit: privGerrit},
	}
	wd, err := createMinorReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, comm, 25, 26)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Start(wd, minorReleaseParams())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Run(runCtx, listener); err != nil && runCtx.Err() == nil {
		t.Fatalf("workflow failed: %v", err)
	}

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
}

func TestMinorReleaseNoMilestoneApproval(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, false)

	var approvedNoMilestone bool
	deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
		if strings.Contains(ctx.TaskName, "Confirm no-milestone run") {
			approvedNoMilestone = true
			return nil
		}
		if strings.Contains(ctx.TaskName, "Confirm PRIVATE-track security CLs") {
			return nil
		}
		return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
	}

	runCtx, stop := context.WithCancel(deps.ctx)
	t.Cleanup(stop)
	listener := &verboseListener{t: t, onStall: stop}

	comm := task.CommunicationTasks{
		SecurityCommunicationTasks: task.SecurityCommunicationTasks{PrivateGerrit: privGerrit},
	}
	wd, err := createMinorReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, comm, 25, 26)
	if err != nil {
		t.Fatal(err)
	}
	params := minorReleaseParams()
	params["Release Milestone (optional)"] = ""
	w, err := workflow.Start(wd, params)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Run(runCtx, listener); err != nil && runCtx.Err() == nil {
		t.Fatalf("workflow failed: %v", err)
	}
	if !approvedNoMilestone {
		t.Errorf("no-milestone approval gate did not fire for empty milestone")
	}
}

func TestMinorReleaseMilestoneSkipsApproval(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, false)

	deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
		if strings.Contains(ctx.TaskName, "Confirm no-milestone run") {
			t.Errorf("no-milestone approval gate fired for non-empty milestone")
			return nil
		}
		if strings.Contains(ctx.TaskName, "Confirm PRIVATE-track security CLs") {
			return nil
		}
		return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
	}

	runCtx, stop := context.WithCancel(deps.ctx)
	t.Cleanup(stop)
	listener := &verboseListener{t: t, onStall: stop}

	comm := task.CommunicationTasks{
		SecurityCommunicationTasks: task.SecurityCommunicationTasks{PrivateGerrit: privGerrit},
	}
	wd, err := createMinorReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, comm, 25, 26)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Start(wd, minorReleaseParams())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Run(runCtx, listener); err != nil && runCtx.Err() == nil {
		t.Fatalf("workflow failed: %v", err)
	}
}

func TestMinorReleaseSecurityCoalesceCherryPickConflict(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)

	privGerrit.AddChange("go", "1234", &gerrit.ChangeInfo{
		ID:                   "1234",
		ChangeID:             "1234",
		ChangeNumber:         1234,
		Branch:               "public",
		Submittable:          true,
		Mergeable:            true,
		ContainsGitConflicts: true,
	}, "crypto/tls: fix something\n\nFixes CVE-1985-0703\nFixes golang/go#1")
	privGerrit.AddChange("go", "5678", &gerrit.ChangeInfo{
		ID:                   "5678",
		ChangeID:             "5678",
		ChangeNumber:         5678,
		Branch:               "public",
		Submittable:          true,
		Mergeable:            true,
		ContainsGitConflicts: true,
	}, "cmd/compile: fix something else\n\nFixes CVE-1970-0001\nFixes #2")

	deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
		if strings.Contains(ctx.TaskName, "Confirm PRIVATE-track security CLs") {
			return nil
		}
		return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
	}

	comm := task.CommunicationTasks{
		SecurityCommunicationTasks: task.SecurityCommunicationTasks{PrivateGerrit: privGerrit},
	}
	wd, err := createMinorReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, comm, 25, 26)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Start(wd, minorReleaseParams())
	if err != nil {
		t.Fatal(err)
	}

	tracker := &taskStartTracker{Listener: &verboseListener{t: t}}
	errMsg := runToFailure(t, deps.ctx, w, "Create cherry-picks", tracker)

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

	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "cherry-picks"}}
	retried, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, internalBranches, changes)
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
}

func TestMinorReleaseSecurityCoalesceRestart(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "coalesce"}}

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

	// First run: establish a prior-iteration checkpoint branch.
	first, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
	if err != nil {
		t.Fatalf("first createSecurityCheckpoint: %v", err)
	}
	if !strings.HasPrefix(first, bi.CheckpointName+"-") {
		t.Errorf("checkpoint name %q is not prefixed with %q", first, bi.CheckpointName+"-")
	}
	firstHead, err := privGerrit.ReadBranchHead(deps.ctx, "go", first)
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
	} else if second == first {
		t.Errorf("restart reused checkpoint name %q; want a distinct timestamped branch", second)
	}

	// The first run's checkpoint branch is left untouched.
	gotHead, err := privGerrit.ReadBranchHead(deps.ctx, "go", first)
	if err != nil {
		t.Fatalf("re-reading first checkpoint head: %v", err)
	}
	if gotHead != firstHead {
		t.Errorf("first checkpoint head moved: was %q, now %q", firstHead, gotHead)
	}
}

func TestPublicizeIdempotent(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	taskCtx := &workflow.TaskContext{Context: ctx, Logger: &testLogger{t: t, task: "publicize"}}

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

	securityCommit, err := privGerrit.ReadBranchHead(ctx, "go", "internal-release-branch.go1.26.1")
	if err != nil {
		t.Fatal(err)
	}

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

	build := &BuildReleaseTasks{
		GerritClient:         pubGerrit,
		GerritProject:        "go",
		PrivateGerritClient:  privGerrit,
		PrivateGerritProject: "go",
		Git:                  new(task.Git),
	}

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
}

func TestCheckAlreadyPublicizedIgnoresAbandoned(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	taskCtx := &workflow.TaskContext{Context: ctx, Logger: &testLogger{t: t, task: "publicize"}}

	pubRepo := task.NewFakeRepo(t, "go")
	base := pubRepo.Commit(map[string]string{"README": "hello"})
	pubRepo.Branch("release-branch.go1.26", base)

	privRepo := task.CloneFakeRepo(t, "go", pubRepo)
	privRepo.Branch("internal-release-branch.go1.26.1", base)
	privRepo.CommitOnBranchWithMessage("internal-release-branch.go1.26.1",
		"crypto/tls: fix vuln\n\nFixes CVE-2026-1234\n\nChange-Id: I0000000000000000000000000000000000000001",
		map[string]string{"security1.txt": "fix1"})

	pubGerrit := task.NewFakeGerrit(t, pubRepo)
	privGerrit := task.NewFakeGerrit(t, privRepo)

	securityCommit, err := privGerrit.ReadBranchHead(ctx, "go", "internal-release-branch.go1.26.1")
	if err != nil {
		t.Fatal(err)
	}

	pubGerrit.AddChange("go", "pub-1", &gerrit.ChangeInfo{
		ID:           "pub-1",
		ChangeID:     "I0000000000000000000000000000000000000001",
		ChangeNumber: 9001,
		Branch:       "release-branch.go1.26",
		Status:       gerrit.ChangeStatusAbandoned,
	}, "crypto/tls: fix vuln")

	build := &BuildReleaseTasks{
		GerritClient:         pubGerrit,
		GerritProject:        "go",
		PrivateGerritClient:  privGerrit,
		PrivateGerritProject: "go",
		Git:                  new(task.Git),
	}

	repo, err := build.Git.CloneBranch(taskCtx, pubGerrit.GitRepoURL("go"), "release-branch.go1.26")
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.RunCommand(taskCtx, "fetch", privGerrit.GitRepoURL("go"), "refs/heads/internal-release-branch.go1.26.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RunCommand(taskCtx, "cherry-pick", base+".."+securityCommit); err != nil {
		t.Fatal(err)
	}

	existing, err := build.checkAlreadyPublicized(taskCtx, repo, "release-branch.go1.26", base)
	if err != nil {
		t.Fatalf("checkAlreadyPublicized: %v", err)
	}
	if len(existing) != 0 {
		t.Errorf("checkAlreadyPublicized treated abandoned CLs as already publicized: %v", existing)
	}
}

func TestPublicizePartialFailsOpen(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	taskCtx := &workflow.TaskContext{Context: ctx, Logger: &testLogger{t: t, task: "publicize"}}

	pubRepo := task.NewFakeRepo(t, "go")
	base := pubRepo.Commit(map[string]string{"README": "hello"})
	pubRepo.Branch("release-branch.go1.26", base)

	privRepo := task.CloneFakeRepo(t, "go", pubRepo)
	privRepo.Branch("internal-release-branch.go1.26.1", base)
	privRepo.CommitOnBranchWithMessage("internal-release-branch.go1.26.1",
		"crypto/tls: fix vuln\n\nChange-Id: I0000000000000000000000000000000000000001",
		map[string]string{"security1.txt": "fix1"})
	privRepo.CommitOnBranchWithMessage("internal-release-branch.go1.26.1",
		"cmd/compile: fix another\n\nChange-Id: I0000000000000000000000000000000000000002",
		map[string]string{"security2.txt": "fix2"})

	pubGerrit := task.NewFakeGerrit(t, pubRepo)
	privGerrit := task.NewFakeGerrit(t, privRepo)

	securityCommit, err := privGerrit.ReadBranchHead(ctx, "go", "internal-release-branch.go1.26.1")
	if err != nil {
		t.Fatal(err)
	}

	pubGerrit.AddChange("go", "pub-1", &gerrit.ChangeInfo{
		ID:           "pub-1",
		ChangeID:     "I0000000000000000000000000000000000000001",
		ChangeNumber: 9001,
		Branch:       "release-branch.go1.26",
		Status:       "NEW",
	}, "crypto/tls: fix vuln")

	build := &BuildReleaseTasks{
		GerritClient:         pubGerrit,
		GerritProject:        "go",
		PrivateGerritClient:  privGerrit,
		PrivateGerritProject: "go",
		Git:                  new(task.Git),
	}

	_, err = build.publicizePrivateSecurityCLs(taskCtx,
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
}

func TestFetchSecurityMilestone(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	ctx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "milestone"}}

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
	deps, _ := newMinorCoalesceTestDeps(t, true)
	ctx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "branchinfo"}}

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
}

func TestCheckPrivateChangesLint(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	ctx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "lint"}}

	// Replace the well-formed commit messages with ones missing both a CVE
	// reference and a GitHub issue reference.
	privGerrit.AddChange("go", "1234", nil, "crypto/tls: fix something\n\nNo references here.")
	privGerrit.AddChange("go", "5678", nil, "cmd/compile: fix something else\n\nStill nothing.")

	rm := &relmeta.ReleaseMilestone{
		Patches: []*relmeta.SecurityPatch{{
			Track:       relmeta.Private,
			Package:     "crypto/tls",
			Changelists: []string{"https://go-internal-review.git.corp.google.com/c/go/+/1234"},
		}},
	}
	_, err := deps.buildTasks.checkPrivateChanges(ctx, rm)
	if err == nil {
		t.Fatal("checkPrivateChanges with bad commit messages: got nil error")
	}
	for _, want := range []string{"missing CVE reference", "missing GitHub issue reference"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// A well-formed commit message produces no lint errors.
	privGerrit.AddChange("go", "1234", nil, "crypto/tls: fix\n\nFixes CVE-1985-0703\nFixes golang/go#1")
	if _, err := deps.buildTasks.checkPrivateChanges(ctx, rm); err != nil {
		t.Errorf("checkPrivateChanges with a well-formed message: %v", err)
	}
}

func TestMinorReleaseSecurityCoalesceMetadata(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)

	comm := task.CommunicationTasks{
		SecurityCommunicationTasks: task.SecurityCommunicationTasks{PrivateGerrit: privGerrit},
	}

	deps.buildTasks.ApproveAction = func(ctx *workflow.TaskContext) error {
		if strings.Contains(ctx.TaskName, "Confirm PRIVATE-track security CLs") {
			return nil
		}
		return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
	}

	runCtx, stop := context.WithCancel(deps.ctx)
	t.Cleanup(stop)
	listener := &verboseListener{t: t, onStall: stop}

	wd, err := createMinorReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, comm, 25, 26)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workflow.Start(wd, minorReleaseParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(runCtx, listener); err != nil && runCtx.Err() == nil {
		t.Fatalf("workflow failed before the metadata tasks finished: %v", err)
	}
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

// minorReleaseParams returns the parameters needed to start the workflow built
// by createMinorReleaseWorkflow(.., 25, 26). Each minor's sub-workflow
// contributes its own prefixed "Targets to skip testing" parameter.
func minorReleaseParams() map[string]any {
	return map[string]any{
		"Release Coordinator Usernames (optional)":               []string(nil),
		task.SecurityReviewersParameter.Name:                     []string{"reviewer@google.com"},
		"Release Milestone (optional)":                           "99915010",
		"Go 1.26: Targets to skip testing (or 'all') (optional)": []string{"all"},
		"Go 1.25: Targets to skip testing (or 'all') (optional)": []string{"all"},
	}
}

func TestAdvisoryTestsFail(t *testing.T) {
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

	w, err := workflow.Start(wd, map[string]any{
		"Targets to skip testing (or 'all') (optional)": []string(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(deps.ctx, &verboseListener{t: t}); err != nil {
		t.Fatal(err)
	}
	if testApprovals.Load() != 1 {
		t.Errorf("failed advisory builder didn't need approval")
	}
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

func serveBootstrap(w http.ResponseWriter, r *http.Request) {
	task.ServeTarball("go-builder-data/go", map[string]string{
		"bin/go": fakeGo,
	}, w, r)
}

func checkFile(t *testing.T, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, check func(*testing.T, []byte)) {
	t.Run(filename, func(t *testing.T) {
		resolvedName := filename
		if files != nil {
			f, ok := files[filename]
			if !ok {
				t.Fatalf("file %q not published", filename)
			}
			if diff := cmp.Diff(meta, f, cmpopts.IgnoreFields(task.WebsiteFile{}, "Filename", "Version", "ChecksumSHA256", "Size")); diff != "" {
				t.Errorf("file metadata mismatch (-want +got):\n%v", diff)
			}
			resolvedName = f.Filename
		}
		body := fetch(t, dlURL+"/"+resolvedName)
		check(t, body)
	})
}

func fetch(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
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

func checkContents(t *testing.T, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, contents string) {
	checkFile(t, dlURL, files, filename, meta, func(t *testing.T, b []byte) {
		if got, want := string(b), contents; !strings.Contains(got, want) {
			t.Errorf("%v contains %q, want %q", filename, got, want)
		}
	})
}

func checkTGZ(t *testing.T, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, contents map[string]string) {
	checkFile(t, dlURL, files, filename, meta, func(t *testing.T, b []byte) {
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

func checkZip(t *testing.T, dlURL string, files map[string]task.WebsiteFile, filename string, meta task.WebsiteFile, contents map[string]string) {
	checkFile(t, dlURL, files, filename, meta, func(t *testing.T, b []byte) {
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

type verboseListener struct {
	t       *testing.T
	onStall func()
}

func (l *verboseListener) WorkflowStalled(workflowID uuid.UUID) error {
	l.t.Logf("workflow %q: stalled", workflowID.String())
	if l.onStall != nil {
		l.onStall()
	}
	return nil
}

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

func (l *testLogger) Printf(format string, v ...any) {
	l.t.Logf("task %-10v: LOG: %s", l.task, fmt.Sprintf(format, v...))
}

func runToFailure(t *testing.T, ctx context.Context, w *workflow.Workflow, task string, wrap workflow.Listener) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t.Helper()
	var message string
	listener := &errorListener{
		taskName: task,
		callback: func(m string) {
			message = m
			cancel()
		},
		Listener: wrap,
	}
	_, err := w.Run(ctx, listener)
	if err == nil {
		t.Fatalf("workflow unexpectedly succeeded")
	}
	return message
}

type errorListener struct {
	taskName string
	callback func(string)
	workflow.Listener
}

func (l *errorListener) TaskStateChanged(id uuid.UUID, taskID string, st *workflow.TaskState) error {
	if st.Name == l.taskName && st.Finished && st.Error != "" {
		l.callback(st.Error)
	}
	l.Listener.TaskStateChanged(id, taskID, st)
	return nil
}

type taskStartTracker struct {
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

func TestCreateInternalReleaseBranchesIdempotent(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "id8"}}

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

	// First run: creates internal release branches.
	branches1, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
	if err != nil {
		t.Fatalf("first createInternalReleaseBranches: %v", err)
	}
	if len(branches1) == 0 {
		t.Fatal("first run created no internal release branches")
	}

	// Record the first run's branch heads.
	firstHeads := map[string]string{}
	for _, b := range branches1 {
		head, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
		if err != nil {
			t.Fatalf("reading head of %s: %v", b, err)
		}
		firstHeads[b] = head
	}

	// Second run (restart): must succeed, not 409.
	branches2, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
	if err != nil {
		t.Fatalf("second createInternalReleaseBranches: %v (expected idempotent success)", err)
	}
	if len(branches2) != len(branches1) {
		t.Fatalf("branch count mismatch: first=%d, second=%d", len(branches1), len(branches2))
	}

	// Verify the recreated branches point at the same public heads.
	for _, b := range branches2 {
		head, err := privGerrit.ReadBranchHead(deps.ctx, "go", b)
		if err != nil {
			t.Fatalf("reading head of %s after restart: %v", b, err)
		}
		if head != firstHeads[b] {
			t.Errorf("branch %s head after restart = %q, want %q (same public head)", b, head, firstHeads[b])
		}
	}
}

func TestCreateInternalReleaseBranchesOpenCherryPicks(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "id8-opencp"}}

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

	branches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
	if err != nil {
		t.Fatalf("first createInternalReleaseBranches: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("first run created no internal release branches")
	}

	freshCPs, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, branches, cls)
	if err != nil {
		t.Fatalf("createSecurityCherryPicks: %v", err)
	}
	wantCPCount := len(cls) * len(branches)
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

	restartCPs, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, branches2, cls)
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
}

func TestCreateSecurityCherryPicksDedup(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "id9"}}

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

	// Create internal release branches so cherry-picks have somewhere to land.
	releaseBranches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
	if err != nil {
		t.Fatal(err)
	}

	// (a) Fresh run: cherry-picks ALL CLs onto each internal branch.
	freshCPs, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, releaseBranches, cls)
	if err != nil {
		t.Fatalf("fresh createSecurityCherryPicks: %v", err)
	}
	wantCount := len(cls) * len(releaseBranches)
	if got := len(freshCPs); got != wantCount {
		t.Fatalf("fresh cherry-picks: got %d, want %d (cls=%d * branches=%d)", got, wantCount, len(cls), len(releaseBranches))
	}

	// (b) Restart: all cherry-picks already exist. The function must skip
	// duplicates and still return the same number of cherry-picks (the
	// existing ones).
	restartCPs, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, releaseBranches, cls)
	if err != nil {
		t.Fatalf("restart createSecurityCherryPicks: %v", err)
	}
	if got := len(restartCPs); got != wantCount {
		t.Fatalf("restart cherry-picks: got %d, want %d", got, wantCount)
	}

	// Verify the restart reused the existing CLs (same change numbers).
	freshNums := map[int]bool{}
	for _, cp := range freshCPs {
		freshNums[cp.ChangeNumber] = true
	}
	for _, cp := range restartCPs {
		if !freshNums[cp.ChangeNumber] {
			t.Errorf("restart returned unknown cherry-pick CL %d; want an existing CL", cp.ChangeNumber)
		}
	}
}

func TestCreateSecurityCherryPicksPartialDedup(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "id9-partial"}}

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

	releaseBranches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, cls)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-seed a cherry-pick for the first CL onto the first branch only.
	// This simulates a partial prior run.
	firstBranch := releaseBranches[0]
	preseeded := &gerrit.ChangeInfo{
		ID:           "pre-cp-1",
		ChangeID:     cls[0].ChangeID, // same Change-Id as original
		ChangeNumber: 9999,
		Branch:       firstBranch,
		Submittable:  true,
		Mergeable:    true,
		Status:       "NEW",
	}
	privGerrit.AddChange("go", "pre-cp-1", preseeded, "preseeded cherry-pick")

	cps, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, releaseBranches, cls)
	if err != nil {
		t.Fatalf("partial createSecurityCherryPicks: %v", err)
	}
	wantCount := len(cls) * len(releaseBranches)
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
}

func TestMoveAndRebasePrivateChanges(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "move-fresh"}}

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

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		moved, err := deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}
		if len(moved) != len(cls) {
			t.Fatalf("got %d CLs, want %d", len(moved), len(cls))
		}
		for _, ci := range moved {
			if ci.Branch != checkpoint {
				t.Errorf("CL %d branch = %q, want %q", ci.ChangeNumber, ci.Branch, checkpoint)
			}
		}
	})

	t.Run("restart_already_moved", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "move-restart"}}

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

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		// Simulate the CLs having already been moved to the checkpoint branch
		// by a prior run, so moveAndRebasePrivateChanges sees them as already
		// on the correct branch and tolerates the 409.
		for _, ci := range cls {
			ci.Branch = checkpoint
		}

		moved, err := deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges on already-moved CLs: %v", err)
		}
		if len(moved) != len(cls) {
			t.Fatalf("got %d CLs, want %d", len(moved), len(cls))
		}
	})

	t.Run("restart_already_merged", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "move-merged"}}

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

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		// Simulate CL 1234 having already been merged by a prior run,
		// so moveAndRebasePrivateChanges sees it as merged and tolerates the 409.
		cls[0].Status = gerrit.ChangeStatusMerged
		cls[0].Submittable = false

		moved, err := deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges with merged CL: %v", err)
		}
		if len(moved) != len(cls) {
			t.Fatalf("got %d CLs, want %d", len(moved), len(cls))
		}
		for _, ci := range moved {
			if ci.ChangeNumber == 1234 && ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("merged CL 1234 status = %q, want %q", ci.Status, gerrit.ChangeStatusMerged)
			}
		}
	})
}

func TestSubmitPrivateChanges(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "submit-happy"}}

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

		checkpoint, err := deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, cls)
		if err != nil {
			t.Fatalf("createSecurityCheckpoint: %v", err)
		}

		cls, err = deps.buildTasks.moveAndRebasePrivateChanges(taskCtx, checkpoint, cls)
		if err != nil {
			t.Fatalf("moveAndRebasePrivateChanges: %v", err)
		}

		submitted, err := deps.buildTasks.submitPrivateChanges(taskCtx, cls)
		if err != nil {
			t.Fatalf("submitPrivateChanges: %v", err)
		}
		if len(submitted) != len(cls) {
			t.Fatalf("got %d CLs, want %d", len(submitted), len(cls))
		}
		for _, ci := range submitted {
			if ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("CL %d status = %q, want %q", ci.ChangeNumber, ci.Status, gerrit.ChangeStatusMerged)
			}
		}
	})

	t.Run("already_merged_skip", func(t *testing.T) {
		deps, privGerrit := newMinorCoalesceTestDeps(t, true)
		taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "submit-skip"}}

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
		cls[0].Status = gerrit.ChangeStatusMerged
		cls[0].Submittable = false

		submitted, err := deps.buildTasks.submitPrivateChanges(taskCtx, cls)
		if err != nil {
			t.Fatalf("submitPrivateChanges with pre-merged CL: %v", err)
		}
		if len(submitted) != len(cls) {
			t.Fatalf("got %d CLs, want %d", len(submitted), len(cls))
		}
		for _, ci := range submitted {
			if ci.Status != gerrit.ChangeStatusMerged {
				t.Errorf("CL %d status = %q, want %q", ci.ChangeNumber, ci.Status, gerrit.ChangeStatusMerged)
			}
		}
	})
}

func TestCreateVulnReportsStdCmd(t *testing.T) {
	deps, _ := newMinorCoalesceTestDeps(t, true)

	vulndbRepo := task.NewFakeRepo(t, "vulndb")
	vulndbRepo.CommitOnBranch("master", map[string]string{"README": "vulndb"})
	pubGerrit := task.NewFakeGerrit(t, vulndbRepo)
	deps.buildTasks.GerritClient = pubGerrit

	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "vu1"}}

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
}

func TestCreateVulnReportsNilMilestone(t *testing.T) {
	deps, _ := newMinorCoalesceTestDeps(t, false)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "vu1-noop"}}

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

func TestMergedCLCherryPickedOntoInternalBranch(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "cp1"}}

	bi, err := computeSecurityBranchInfo(taskCtx, deps.versionTasks, 26, mustGetNextMinors(t, deps))
	if err != nil {
		t.Fatal(err)
	}

	// Mark CL 1234 as already merged.
	merged1234, err := privGerrit.GetChange(taskCtx, "1234")
	if err != nil {
		t.Fatalf("GetChange(1234): %v", err)
	}
	merged1234.Status = gerrit.ChangeStatusMerged
	merged1234.Submittable = false

	var allCLs []*gerrit.ChangeInfo
	for _, num := range []string{"1234", "5678"} {
		ci, err := privGerrit.GetChange(deps.ctx, num)
		if err != nil {
			t.Fatalf("GetChange(%s): %v", num, err)
		}
		allCLs = append(allCLs, ci)
	}

	openCLs := []*gerrit.ChangeInfo{}
	for _, ci := range allCLs {
		if ci.Status != gerrit.ChangeStatusMerged {
			openCLs = append(openCLs, ci)
		}
	}
	_, err = deps.buildTasks.createSecurityCheckpoint(taskCtx, bi, openCLs)
	if err != nil {
		t.Fatalf("createSecurityCheckpoint: %v", err)
	}

	// Create internal release branches from ALL cls (the full milestone).
	releaseBranches, err := deps.buildTasks.createInternalReleaseBranches(taskCtx, bi, allCLs)
	if err != nil {
		t.Fatalf("createInternalReleaseBranches: %v", err)
	}

	cps, err := deps.buildTasks.createSecurityCherryPicks(taskCtx, releaseBranches, allCLs)
	if err != nil {
		t.Fatalf("createSecurityCherryPicks: %v", err)
	}

	wantCount := len(allCLs) * len(releaseBranches)
	if got := len(cps); got != wantCount {
		t.Errorf("cherry-picks: got %d, want %d", got, wantCount)
	}
}

func TestConvertInternalChangelists(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, true)
	pubGerrit := deps.gerrit.FakeGerrit
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "pc1"}}

	privGerrit.AddChange("go", "1234", nil, "crypto/tls: fix something\n\nFixes CVE-1985-0703\nFixes golang/go#1\n\nChange-Id: I0000000000000000000000000000000000000001")
	privGerrit.AddChange("go", "5678", nil, "cmd/compile: fix something else\n\nFixes CVE-1970-0001\nFixes #2\n\nChange-Id: I0000000000000000000000000000000000000002")
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

}

func TestConvertInternalChangelistsEmptyMilestone(t *testing.T) {
	deps, privGerrit := newMinorCoalesceTestDeps(t, false)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "pc3"}}

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
}

func TestConvertInternalChangelistsMissingChangeID(t *testing.T) {
	deps, _ := newMinorCoalesceTestDeps(t, true)
	taskCtx := &workflow.TaskContext{Context: deps.ctx, Logger: &testLogger{t: t, task: "pc2"}}

	rm, err := deps.buildTasks.fetchSecurityMilestone(taskCtx, "99915010")
	if err != nil {
		t.Fatal(err)
	}
	_, err = deps.buildTasks.convertInternalChangelists(taskCtx, rm, nil)
	if err == nil || !strings.Contains(err.Error(), "no Change-Id footer") {
		t.Fatalf("convertInternalChangelists error = %v, want Change-Id footer error", err)
	}
}
