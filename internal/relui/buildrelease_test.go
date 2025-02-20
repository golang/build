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
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
)

func TestRelease(t *testing.T) {
	if testing.Short() {
		// These release tests are pretty thorough and
		// take upwards of 50 seconds as of 2025-02-21.
		//
		// Also, the major release kind involves doing
		// a 'go run git-generate@version' which needs
		// the internet if that version isn't in cache.
		//
		// Skip in short mode.
		t.Skip("skipping large test in short mode")
	}

	t.Run("minor", func(t *testing.T) {
		testRelease(t, "go1.22", 22, "go1.22.1", task.KindMinor)
	})
	t.Run("beta", func(t *testing.T) {
		testRelease(t, "go1.22", 23, "go1.23beta1", task.KindBeta)
	})
	t.Run("rc", func(t *testing.T) {
		testRelease(t, "go1.22", 23, "go1.23rc1", task.KindRC)
	})
	t.Run("major", func(t *testing.T) {
		testRelease(t, "go1.22", 23, "go1.23.0", task.KindMajor)
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
"generate")
  mkdir -p internal/stdlib
  cd internal/stdlib && echo "package stdlib" >> manifest.go
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
	versionTasks   *task.VersionTasks
	buildTasks     *BuildReleaseTasks
	milestoneTasks *task.MilestoneTasks
	publishedFiles map[string]task.WebsiteFile
}

func newReleaseTestDeps(t *testing.T, previousTag string, major int, wantVersion string) *releaseTestDeps {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Requires bash shell scripting support.")
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

	goRepo := task.NewFakeRepo(t, "go")
	base := goRepo.Commit(goFiles)
	goRepo.Tag(previousTag, base)
	goRepo.Branch(fmt.Sprintf("release-branch.go1.%d", major), base)
	dlRepo := task.NewFakeRepo(t, "dl")
	buildRepo := task.NewFakeRepo(t, "build")
	buildRepo.Commit(map[string]string{
		"go.mod": "module golang.org/x/build\n",
	})
	toolsRepo := task.NewFakeRepo(t, "tools")
	toolsRepo.Commit(map[string]string{
		"go.mod":                       "module golang.org/x/tools\n",
		"go.sum":                       "\n",
		"internal/imports/mkstdlib.go": "package imports\nconst C=1",
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
			Milestones:       map[int]string{0: "Go1.18", 1: "Go1.23", 2: "Go1.22.1"},
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
		ApproveAction: func(ctx *workflow.TaskContext) error {
			if strings.Contains(ctx.TaskName, "Release Coordinator Approval") {
				return nil
			}
			return fmt.Errorf("unexpected approval request for %q", ctx.TaskName)
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

	w, err := workflow.Start(wd, map[string]interface{}{
		"Targets to skip testing (or 'all') (optional)": []string{
			// allScript is intentionally hardcoded to fail on GOOS=js
			// and we confirm here that it's possible to skip that.
			"js-wasm-node18", // Builder used on 1.21 and newer.
			"js-wasm",        // Builder used on 1.20 and older.
		},
		"Ref from the private repository to build from (optional)": "",
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

	// Check for go.dev/issue/69095.
	for _, repo := range [...]string{"build", "tools"} {
		goMod, err := deps.gerrit.ReadFile(deps.ctx, repo, "HEAD", "go.mod")
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("module golang.org/x/%s\n", repo)
		if kind == task.KindMajor {
			prevGoVer := major - 1 // See https://go.googlesource.com/proposal/+/HEAD/design/69095-x-repo-continuous-go.md#why-1_n_1_0.
			want += fmt.Sprintf("\ngo 1.%d.0\n", prevGoVer)
		}
		if diff := cmp.Diff(want, string(goMod)); diff != "" {
			t.Errorf("x/ repo should have a maintained go directive and no toolchain directive: go.mod mismatch (-want +got):\n%s", diff)
		}
	}
}

func testSecurity(t *testing.T, mergeFixes bool) {
	deps := newReleaseTestDeps(t, "go1.17", 18, "go1.18rc1")

	// Set up the fake merge process. Once we stop to ask for approval, commit
	// the fix to the public server.
	privateRepo := task.NewFakeRepo(t, "go-private")
	privateRepo.Commit(goFiles)
	securityFix := map[string]string{"security.txt": "This file makes us secure"}
	privateRef := privateRepo.Commit(securityFix)
	privateGerrit := task.NewFakeGerrit(t, privateRepo)
	deps.buildBucket.GerritURL = privateGerrit.GerritURL()
	deps.buildBucket.Projects = []string{"go-private"}
	deps.buildTasks.PrivateGerritClient = privateGerrit
	deps.buildTasks.PrivateGerritProject = "go-private"

	defaultApprove := deps.buildTasks.ApproveAction
	deps.buildTasks.ApproveAction = func(tc *workflow.TaskContext) error {
		if mergeFixes {
			deps.goRepo.CommitOnBranch("release-branch.go1.18", securityFix)
		}
		return defaultApprove(tc)
	}

	// Run the release.
	wd := workflow.New(workflow.ACL{})
	v := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, 18, task.KindRC, workflow.Slice[string]())
	workflow.Output(wd, "Published Go version", v)

	w, err := workflow.Start(wd, map[string]interface{}{
		"Targets to skip testing (or 'all') (optional)":            []string{"js-wasm"},
		"Ref from the private repository to build from (optional)": privateRef,
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
		"go/security.txt": "This file makes us secure",
	})
}

func TestAdvisoryTestsFail(t *testing.T) {
	deps := newReleaseTestDeps(t, "go1.17", 18, "go1.18rc1")
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
	v := addSingleReleaseWorkflow(deps.buildTasks, deps.milestoneTasks, deps.versionTasks, wd, 18, task.KindRC, workflow.Slice[string]())
	workflow.Output(wd, "Published Go version", v)

	w, err := workflow.Start(wd, map[string]interface{}{
		"Targets to skip testing (or 'all') (optional)":            []string(nil),
		"Ref from the private repository to build from (optional)": "",
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

func (l *testLogger) Printf(format string, v ...interface{}) {
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
