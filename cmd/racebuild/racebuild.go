// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// racebuild builds the race runtime (syso files) on all supported OSes using gomote.
// Usage:
//
//	$ racebuild -rev <llvm_git_revision> -goroot <path_to_go_repo>
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/build/internal/envutil"
	"golang.org/x/sync/errgroup"
)

var (
	flagGoroot     = flag.String("goroot", "", "path to Go repository to update (required)")
	flagRev        = flag.String("rev", "", "llvm-project git revision from https://github.com/llvm/llvm-project (required)")
	flagCherryPick = flag.String("cherrypick", "", "go.googlesource.com CL reference to cherry-pick on top of Go repo (takes form 'refs/changes/NNN/<CL number>/<patchset number>') (optional)")
	flagCheckout   = flag.String("checkout", "", "go.googlesource.com CL reference to check out on top of Go repo (takes form 'refs/changes/NNN/<CL number>/<patchset number>') (optional)")
	flagCopyOnFail = flag.Bool("copyonfail", false, "Attempt to copy newly built race syso into Go repo even if script fails.")
	flagGoRev      = flag.String("gorev", "HEAD", "Go repository revision to use; HEAD is relative to --goroot")
	flagPlatforms  = flag.String("platforms", "all", `comma-separated platforms (such as "darwin/arm64" or "linux/amd64v1") to rebuild, or "all"`)
)

// goRev is the resolved commit ID of flagGoRev.
var goRev string

// TODO: use buildlet package instead of calling out to gomote.
var platforms = []*Platform{
	{
		OS:      "openbsd",
		Arch:    "amd64",
		SubArch: "v1",
		Skip:    true, // openbsd support is removed from TSAN, see issue #52090
		Type:    "openbsd-amd64-72",
		Script: `#!/usr/bin/env bash
set -e
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && CC=clang ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_openbsd_amd64.syso go/src/runtime/race/internal/amd64v1/race_openbsd.syso
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_openbsd_amd64.syso outdir/race_openbsd.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
	{
		OS:      "freebsd",
		Arch:    "amd64",
		SubArch: "v1",
		Type:    "freebsd-amd64-race",
		Script: `#!/usr/bin/env bash
set -e
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && CC=clang ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_freebsd_amd64.syso go/src/runtime/race/internal/amd64v1/race_freebsd.syso
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_freebsd_amd64.syso outdir/race_freebsd.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
	{
		OS:      "darwin",
		Arch:    "amd64",
		SubArch: "v1",
		Type:    "darwin-amd64-12_0",
		Script: `#!/usr/bin/env bash
set -e
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && CC=clang ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_darwin_amd64.syso go/src/runtime/race/internal/amd64v1/race_darwin.syso
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_darwin_amd64.syso outdir/race_darwin.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
	{
		OS:      "darwin",
		Arch:    "amd64",
		SubArch: "v3",
		Skip:    true,
		Type:    "darwin-amd64-12_0",
		Script: `#!/usr/bin/env bash
set -e
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && CC=clang GOAMD64=v3 ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_darwin_amd64.syso go/src/runtime/race/internal/amd64v3/race_darwin.syso
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_darwin_amd64.syso outdir/race_darwin.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && GOAMD64=v3 ./race.bash)
			`,
	},
	{
		OS:   "darwin",
		Arch: "arm64",
		Type: "darwin-arm64-12",
		Script: `#!/usr/bin/env bash
set -e
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && CC=clang ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_darwin_arm64.syso go/src/runtime/race
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_darwin_arm64.syso outdir/race_darwin_arm64.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
	{
		OS:      "linux",
		Arch:    "amd64",
		SubArch: "v1",
		Type:    "linux-amd64-race",
		Script: `#!/usr/bin/env bash
set -e
apt-get update --allow-releaseinfo-change
apt-get install -y git g++ unzip
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_amd64.syso go/src/runtime/race/internal/amd64v1/race_linux.syso
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_amd64.syso outdir/race_linux.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
	{
		OS:      "linux",
		Arch:    "amd64",
		SubArch: "v3",
		Type:    "linux-amd64-race",
		Script: `#!/usr/bin/env bash
set -e
apt-get update
apt-get install -y git g++ unzip
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && GOAMD64=v3 ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_amd64.syso go/src/runtime/race/internal/amd64v3/race_linux.syso
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_amd64.syso outdir/race_linux.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && GOAMD64=v3 ./race.bash)
			`,
	},
	{
		OS:   "linux",
		Arch: "ppc64le",
		Type: "linux-ppc64le-buildlet",
		Script: `#!/usr/bin/env bash
set -e
apt-get update --allow-releaseinfo-change
apt-get install -y git g++ unzip
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
workdir=$(pwd)
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_ppc64le.syso $workdir/go/src/runtime/race
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_ppc64le.syso outdir/race_linux_ppc64le.syso
# TODO(#23731): Uncomment to test the syso file before accepting it.
# free some disk space
# rm -r llvm.zip llvm-project-${REV}
# (cd go/src && ./race.bash)
			`,
	},
	{
		OS:   "linux",
		Arch: "arm64",
		Type: "linux-arm64-race",
		Script: `#!/usr/bin/env bash
set -e
apt-get update --allow-releaseinfo-change
apt-get install -y git g++ unzip
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_arm64.syso go/src/runtime/race
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_arm64.syso outdir/race_linux_arm64.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
	{
		OS:      "netbsd",
		Arch:    "amd64",
		SubArch: "v1",
		Type:    "netbsd-amd64-9_3",
		Script: `#!/usr/bin/env bash
set -e
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && CC=clang ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_netbsd_amd64.syso go/src/runtime/race/internal/amd64v1/race_netbsd.syso
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_netbsd_amd64.syso outdir/race_netbsd.syso
# TODO(#24322): Uncomment to test the syso file before accepting it.
# free some disk space
# rm -r llvm.zip llvm-project-${REV}
# (cd go/src && ./race.bash)
			`,
	},
	{
		OS:      "windows",
		Arch:    "amd64",
		SubArch: "v1",
		Type:    "windows-amd64-race",
		Script: `
@"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -InputFormat None -ExecutionPolicy Bypass -Command "[System.net.ServicePointManager]::SecurityProtocol = 3072; iex ((New-Object System.Net.WebClient).DownloadString('https://chocolatey.org/install.ps1'))" && SET "PATH=%PATH%;%ALLUSERSPROFILE%\chocolatey\bin"
choco install git -y
if %errorlevel% neq 0 exit /b %errorlevel%
call refreshenv
echo adding back in compiler path
set PATH=C:\go\bin;%PATH%;C:\godep\gcc64\bin
rem make sure we have a working copy of gcc
gcc --version
if %errorlevel% neq 0 exit /b %errorlevel%
git clone https://go.googlesource.com/go
if %errorlevel% neq 0 exit /b %errorlevel%
cd go
git checkout %GOREV%
if %errorlevel% neq 0 exit /b %errorlevel%
if "%$GOGITOP%"=="" goto nogogitop
git fetch https://go.googlesource.com/go %GOSRCREF%
if %errorlevel% neq 0 exit /b %errorlevel%
git %GOGITOP% FETCH_HEAD
if %errorlevel% neq 0 exit /b %errorlevel%
:nogogitop
cd ..
git clone https://github.com/llvm/llvm-project
if %errorlevel% neq 0 exit /b %errorlevel%
cd llvm-project
git checkout %REV%
if %errorlevel% neq 0 exit /b %errorlevel%
cd ..
cd llvm-project/compiler-rt/lib/tsan/go
call build.bat
if %errorlevel% neq 0 exit /b %errorlevel%
cd ../../../../..
xcopy llvm-project\compiler-rt\lib\tsan\go\race_windows_amd64.syso go\src\runtime\race\internal\amd64v1\race_windows.syso /Y
if %errorlevel% neq 0 exit /b %errorlevel%
rem work around gomote gettar issue #64195
mkdir outdir
if %errorlevel% neq 0 exit /b %errorlevel%
copy llvm-project\compiler-rt\lib\tsan\go\race_windows_amd64.syso outdir\race_windows.syso
if %errorlevel% neq 0 exit /b %errorlevel%
cd go/src
call race.bat
if %errorlevel% neq 0 exit /b %errorlevel%
			`,
	},
	{
		OS:   "linux",
		Arch: "s390x",
		Type: "linux-s390x-ibm",
		Script: `#!/usr/bin/env bash
set -e
cat /etc/os-release
yum install -y gcc-c++ git golang-bin unzip
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_s390x.syso go/src/runtime/race
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_s390x.syso outdir/race_linux_s390x.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
	{
		OS:   "linux",
		Arch: "loong64",
		Type: "gotip-linux-loong64",
		Script: `#!/usr/bin/env bash
set -e
cat /etc/os-release
# Because LUCI builder is not run as root, these dependencies have been manually installed
# yum install -y gcc-c++ git golang-bin unzip
git clone https://go.googlesource.com/go
pushd go
git checkout $GOREV
if [ "$GOGITOP" != "" ]; then
  git fetch https://go.googlesource.com/go "$GOSRCREF"
  git $GOGITOP FETCH_HEAD
fi
popd
curl -L -o llvm.zip https://github.com/llvm/llvm-project/archive/${REV}.zip
unzip -q llvm.zip llvm-project-${REV}/compiler-rt/*
(cd llvm-project-${REV}/compiler-rt/lib/tsan/go && ./buildgo.sh)
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_loong64.syso go/src/runtime/race
# work around gomote gettar issue #64195
mkdir outdir
cp llvm-project-${REV}/compiler-rt/lib/tsan/go/race_linux_loong64.syso outdir/race_linux_loong64.syso
# free some disk space
rm -r llvm.zip llvm-project-${REV}
(cd go/src && ./race.bash)
			`,
	},
}

func init() {
	// Ensure that there are no duplicate platform entries.
	seen := make(map[string]bool)
	for _, p := range platforms {
		if seen[p.Name()] {
			log.Fatalf("Duplicate platforms entry for %s.", p.Name())
		}
		seen[p.Name()] = true
	}
}

var platformEnabled = make(map[string]bool)

func parsePlatformsFlag() {
	if *flagPlatforms == "all" {
		for _, p := range platforms {
			if !p.Skip {
				platformEnabled[p.Name()] = true
			}
		}
		return
	}

	var invalid []string
	for _, name := range strings.Split(*flagPlatforms, ",") {
		for _, p := range platforms {
			if name == p.Name() {
				platformEnabled[name] = true
				break
			}
		}
		if !platformEnabled[name] {
			invalid = append(invalid, name)
		}
	}

	if len(invalid) > 0 {
		var msg bytes.Buffer
		fmt.Fprintf(&msg, "Unrecognized platforms: %q. Supported platforms are:\n", invalid)
		for _, p := range platforms {
			fmt.Fprintf(&msg, "\t%s\n", p.Name())
		}
		log.Fatal(&msg)
	}
}

func main() {
	flag.Parse()
	if *flagRev == "" || *flagGoroot == "" || *flagGoRev == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}
	if *flagCherryPick != "" && *flagCheckout != "" {
		log.Fatalf("select at most one of -cherrypick and -checkout")
	}
	parsePlatformsFlag()

	cmd := exec.Command("git", "rev-parse", *flagGoRev)
	envutil.SetDir(cmd, *flagGoroot)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("%s failed: %v", strings.Join(cmd.Args, " "), err)
	}
	goRev = string(bytes.TrimSpace(out))
	log.Printf("using Go revision: %s", goRev)

	// Start build on all platforms in parallel.
	// On interrupt, destroy any in-flight builders before exiting.
	ctx, cancel := context.WithCancel(context.Background())
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt)
	go func() {
		<-shutdown
		cancel()
	}()

	g, ctx := errgroup.WithContext(ctx)
	for _, p := range platforms {
		if !platformEnabled[p.Name()] {
			continue
		}

		p := p
		g.Go(func() error {
			if err := p.Build(ctx); err != nil {
				return fmt.Errorf("%v failed: %v", p.Name(), err)
			}
			return p.UpdateReadme()
		})
	}

	if err := g.Wait(); err != nil {
		log.Fatal(err)
	}
}

type Platform struct {
	OS      string
	Arch    string
	SubArch string
	Type    string // gomote instance type
	Inst    string // actual gomote instance name
	Script  string
	Skip    bool // disabled by default
}

func (p *Platform) Name() string {
	if p.SubArch != "" {
		return fmt.Sprintf("%v/%v%v", p.OS, p.Arch, p.SubArch)
	}
	return fmt.Sprintf("%v/%v", p.OS, p.Arch)
}

// Basename returns the name of the output file relative to src/runtime/race.
func (p *Platform) Basename() string {
	if p.SubArch != "" {
		return fmt.Sprintf("internal/%s%s/race_%s.syso", p.Arch, p.SubArch, p.OS)
	}
	return fmt.Sprintf("race_%v_%s.syso", p.OS, p.Arch)
}

func setupForGoRepoGitOp() (string, string) {
	if *flagCherryPick != "" {
		return "cherry-pick -n", *flagCherryPick
	} else if *flagCheckout != "" {
		return "checkout", *flagCheckout
	}
	return "", ""
}

func (p *Platform) Build(ctx context.Context) error {
	// Create gomote instance (or reuse an existing instance for debugging).
	var lastErr error
	for p.Inst == "" {
		inst, err := p.Gomote(ctx, "create", "-status=false", p.Type)
		if err != nil {
			select {
			case <-ctx.Done():
				if lastErr != nil {
					return lastErr
				}
				return err
			default:
				// Creation sometimes fails with transient errors like:
				// "buildlet didn't come up at http://10.240.0.13 in 3m0s".
				log.Printf("%v: instance creation failed, retrying", p.Name())
				lastErr = err
				continue
			}
		}
		p.Inst = strings.Trim(string(inst), " \t\n")
		defer p.Gomote(context.Background(), "destroy", p.Inst)
	}
	log.Printf("%s: using instance %v", p.Name(), p.Inst)

	// putbootstrap
	if _, err := p.Gomote(ctx, "putbootstrap", p.Inst); err != nil {
		return err
	}

	// Execute the script.
	script, err := os.CreateTemp("", "racebuild")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %v", err)
	}
	defer func() {
		script.Close()
		os.Remove(script.Name())
	}()
	if _, err := script.Write([]byte(p.Script)); err != nil {
		return fmt.Errorf("failed to write temp file: %v", err)
	}
	script.Close()
	targetName := "script.bash"
	if p.OS == "windows" {
		targetName = "script.bat"
	}
	if _, err := p.Gomote(ctx, "put", "-mode=0700", p.Inst, script.Name(), targetName); err != nil {
		return err
	}
	var scriptRunErr error
	gogitop, gosrcref := setupForGoRepoGitOp()
	if _, err := p.Gomote(ctx, "run", "-e=REV="+*flagRev, "-e=GOREV="+goRev,
		"-e=GOGITOP="+gogitop, "-e=GOSRCREF="+gosrcref,
		p.Inst, targetName); err != nil {
		if !*flagCopyOnFail {
			return err
		}
		log.Printf("%v: gomote script run failed, continuing...\n", p.Name())
		scriptRunErr = err
	}

	// The script is supposed to leave updated runtime at that path. Copy it out.
	syso := p.Basename()
	_, err = p.Gomote(ctx, "gettar", "-dir=outdir", p.Inst)
	if err != nil {
		return err
	}

	// Untar the runtime and write it to goroot.
	if err := p.WriteSyso(filepath.Join(*flagGoroot, "src", "runtime", "race", syso), p.Inst+".tar.gz"); err != nil {
		return fmt.Errorf("%v", err)
	}
	if scriptRunErr != nil {
		return err
	}

	log.Printf("%v: build completed", p.Name())
	return nil
}

func (p *Platform) WriteSyso(sysof string, targz string) error {
	// Ungzip.
	targzf, err := os.Open(targz)
	if err != nil {
		return fmt.Errorf("failed to open targz file %s: %v", targz, err)
	}
	defer targzf.Close()
	gzipr, err := gzip.NewReader(targzf)
	if err != nil {
		return fmt.Errorf("failed to read gzip archive: %v", err)
	}
	defer gzipr.Close()
	tr := tar.NewReader(gzipr)
	if _, err := tr.Next(); err != nil {
		return fmt.Errorf("failed to read tar archive: %v", err)
	}

	// Copy the file.
	syso, err := os.Create(sysof)
	if err != nil {
		return fmt.Errorf("failed to open race runtime: %v", err)
	}
	defer syso.Close()
	if _, err := io.Copy(syso, tr); err != nil {
		return fmt.Errorf("failed to write race runtime: %v", err)
	}
	return nil
}

var readmeMu sync.Mutex

func (p *Platform) UpdateReadme() error {
	readmeMu.Lock()
	defer readmeMu.Unlock()

	readmeFile := filepath.Join(*flagGoroot, "src", "runtime", "race", "README")
	readme, err := os.ReadFile(readmeFile)
	if err != nil {
		log.Fatalf("bad -goroot? %v", err)
	}

	syso := p.Basename()
	const (
		readmeTmpl = "%s built with LLVM %s and Go %s."
		commitRE   = "[0-9a-f]+"
	)

	// TODO(bcmills): Extract the C++ toolchain version from the .syso file and
	// record it in the README.
	updatedLine := fmt.Sprintf(readmeTmpl, syso, *flagRev, goRev)

	lineRE, err := regexp.Compile("(?m)^" + fmt.Sprintf(readmeTmpl, regexp.QuoteMeta(syso), commitRE, commitRE) + "$")
	if err != nil {
		return err
	}
	if lineRE.Match(readme) {
		readme = lineRE.ReplaceAll(readme, []byte(updatedLine))
	} else {
		readme = append(append(readme, []byte(updatedLine)...), '\n')
	}

	return os.WriteFile(readmeFile, readme, 0640)
}

func (p *Platform) Gomote(ctx context.Context, args ...string) ([]byte, error) {
	log.Printf("%v: gomote %v", p.Name(), args)

	cmd := exec.CommandContext(ctx, "gomote", args...)
	outBuf := new(bytes.Buffer)
	errBuf := outBuf

	cmd.Stdout = outBuf
	cmd.Stderr = errBuf
	run := cmd.Run
	if len(platformEnabled) == 1 {
		// If building only one platform, stream gomote output to os.Stderr.
		r, w := io.Pipe()
		errTee := io.TeeReader(r, cmd.Stderr)
		if cmd.Stdout == cmd.Stderr {
			cmd.Stdout = w
		}
		cmd.Stderr = w

		run = func() (err error) {
			go func() {
				err = cmd.Run()
				w.Close()
			}()
			io.Copy(os.Stderr, errTee)
			return
		}
	}

	if err := run(); err != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		log.Printf("%v: gomote %v failed:\n%s", p.Name(), args, errBuf)
		return nil, err
	}

	if errBuf.Len() == 0 {
		log.Printf("%v: gomote %v succeeded: <no output>", p.Name(), args)
	} else {
		log.Printf("%v: gomote %v succeeded:\n%s", p.Name(), args, errBuf)
	}
	return outBuf.Bytes(), nil
}
