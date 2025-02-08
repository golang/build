// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package buildgo provides tools for pushing and building the Go
// distribution on buildlets.
package buildgo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/spanlog"
)

// BuilderRev is a build configuration type and a revision.
type BuilderRev struct {
	Name string // e.g. "linux-amd64-race"
	Rev  string // lowercase hex core repo git hash

	// optional sub-repository details (both must be present)
	SubName string // e.g. "net"
	SubRev  string // lowercase hex sub-repo git hash
}

func (br BuilderRev) IsSubrepo() bool {
	return br.SubName != ""
}

func (br BuilderRev) SubRevOrGoRev() string {
	if br.SubRev != "" {
		return br.SubRev
	}
	return br.Rev
}

func (br BuilderRev) RepoOrGo() string {
	if br.SubName == "" {
		return "go"
	}
	return br.SubName
}

// SnapshotObjectName is the cloud storage object name of the
// built Go tree for this builder and Go rev (not the sub-repo).
// The entries inside this tarball do not begin with "go/".
func (br *BuilderRev) SnapshotObjectName() string {
	return fmt.Sprintf("%v/%v/%v.tar.gz", "go", br.Name, br.Rev)
}

// SnapshotURL is the absolute URL of the snapshot object (see above).
func (br *BuilderRev) SnapshotURL(buildEnv *buildenv.Environment) string {
	return buildEnv.SnapshotURL(br.Name, br.Rev)
}

var TestHookSnapshotExists func(*BuilderRev) bool

// SnapshotExists reports whether the snapshot exists in storage.
// It returns potentially false negatives on network errors.
// Callers must not depend on this as more than an optimization.
func (br *BuilderRev) SnapshotExists(ctx context.Context, buildEnv *buildenv.Environment) bool {
	if f := TestHookSnapshotExists; f != nil {
		return f(br)
	}
	req, err := http.NewRequest("HEAD", br.SnapshotURL(buildEnv), nil)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		log.Printf("SnapshotExists check: %v", err)
		return false
	}
	return res.StatusCode == http.StatusOK
}

// A GoBuilder knows how to build a revision of Go with the given configuration.
type GoBuilder struct {
	spanlog.Logger
	BuilderRev
	Conf *dashboard.BuildConfig
	// Goroot is a Unix-style path relative to the work directory of the
	// builder (e.g. "go").
	Goroot string
	// GorootBootstrap is an optional absolute Unix-style path to the
	// bootstrap toolchain, overriding the default.
	GorootBootstrap string
	// GoDevDLBootstrap is whether the bootstrap comes from go.dev/dl/,
	// meaning its content is inside a directory named "go" as opposed
	// to the root of the archive. GorootBootstrap must be empty if so.
	GoDevDLBootstrap bool
	// Force controls whether to use the -force flag when building Go.
	// See go.dev/issue/56679.
	Force bool
}

// RunMake builds the toolchain.
// w is the Writer to send build output to.
// remoteErr and err are as described at the top of this file.
func (gb GoBuilder) RunMake(ctx context.Context, bc buildlet.Client, w io.Writer) (remoteErr, err error) {
	// Build the source code.
	makeSpan := gb.CreateSpan("make", gb.Conf.MakeScript())
	env := append(gb.Conf.Env(), "GOBIN=")
	if gb.GorootBootstrap != "" {
		if gb.GoDevDLBootstrap {
			return nil, fmt.Errorf("cannot set a non-empty GorootBootstrap together with GoDevDLBootstrap")
		}
		env = append(env, "GOROOT_BOOTSTRAP="+gb.GorootBootstrap)
	}
	makeArgs := gb.Conf.MakeScriptArgs()
	if gb.Force {
		makeArgs = append(makeArgs, "-force")
	}
	var makePath []string
	if gb.GoDevDLBootstrap {
		// When using go.dev/dl/ tarballs for bootstrap, their GOROOT ends up being
		// $WORKDIR/go1.4/go, which differs from the genbootstrap tarballs where it's
		// $WORKDIR/go1.4 (without the 'go' subdirectory) instead. So, it's necessary
		// to arrange for it to be found.
		//
		// GOROOT_BOOTSTRAP defaults to $WORKDIR/go1.4 via cmd/buildlet, and since
		// $WORKDIR expansion is supported in $PATH but not in other environment variables,
		// setting it to a correct value here would need an extra bc.WorkDir remote call.
		// So it is simpler to clear out GOROOT_BOOTSTRAP and let the bootstrap
		// toolchain be found by adding its go/bin directory to the front of $PATH.
		env = append(env, "GOROOT_BOOTSTRAP=")
		makePath = []string{"$WORKDIR/go1.4/go/bin", "$PATH"}
	}
	remoteErr, err = bc.Exec(ctx, path.Join(gb.Goroot, gb.Conf.MakeScript()), buildlet.ExecOpts{
		Output:   w,
		ExtraEnv: env,
		Debug:    true,
		Args:     makeArgs,
		Path:     makePath,
	})
	if err != nil {
		makeSpan.Done(err)
		return nil, err
	}
	if remoteErr != nil {
		makeSpan.Done(remoteErr)
		return fmt.Errorf("make script failed: %v", remoteErr), nil
	}
	makeSpan.Done(nil)

	// Need to run "go install -race std" before the snapshot + tests.
	if pkgs := gb.Conf.GoInstallRacePackages(); len(pkgs) > 0 {
		sp := gb.CreateSpan("install_race_std")
		remoteErr, err = bc.Exec(ctx, path.Join(gb.Goroot, "bin/go"), buildlet.ExecOpts{
			Output:   w,
			ExtraEnv: append(gb.Conf.Env(), "GOBIN="),
			Debug:    true,
			Args:     append([]string{"install", "-race"}, pkgs...),
		})
		if err != nil {
			sp.Done(err)
			return nil, err
		}
		if remoteErr != nil {
			sp.Done(err)
			return fmt.Errorf("go install -race std failed: %v", remoteErr), nil
		}
		sp.Done(nil)
	}

	if gb.Name == "linux-amd64-racecompile" {
		return gb.runConcurrentGoBuildStdCmd(ctx, bc, w)
	}

	return nil, nil
}

// runConcurrentGoBuildStdCmd is a step specific only to the
// "linux-amd64-racecompile" builder to exercise the Go 1.9's new
// concurrent compilation. It re-builds the standard library and tools
// with -gcflags=-c=8 using a race-enabled cmd/compile and cmd/link
// (built by caller, RunMake, per builder config).
// The idea is that this might find data races in cmd/compile and cmd/link.
func (gb GoBuilder) runConcurrentGoBuildStdCmd(ctx context.Context, bc buildlet.Client, w io.Writer) (remoteErr, err error) {
	span := gb.CreateSpan("go_build_c128_std_cmd")
	remoteErr, err = bc.Exec(ctx, path.Join(gb.Goroot, "bin/go"), buildlet.ExecOpts{
		Output:   w,
		ExtraEnv: append(gb.Conf.Env(), "GOBIN="),
		Debug:    true,
		Args:     []string{"build", "-a", "-gcflags=-c=8", "std", "cmd"},
	})
	if err != nil {
		span.Done(err)
		return nil, err
	}
	if remoteErr != nil {
		span.Done(remoteErr)
		return fmt.Errorf("go build failed: %v", remoteErr), nil
	}
	span.Done(nil)
	return nil, nil
}

// VersionTgz returns an io.Reader of a *.tar.gz file containing only
// a VERSION file containing the contents of the provided rev string.
func VersionTgz(rev string) io.Reader {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	// Writing to a bytes.Buffer should never fail, so check
	// errors with an explosion:
	check := func(err error) {
		if err != nil {
			panic("previously assumed to never fail: " + err.Error())
		}
	}

	contents := "devel " + rev
	check(tw.WriteHeader(&tar.Header{
		Name: "VERSION",
		Mode: 0644,
		Size: int64(len(contents)),
	}))
	_, err := io.WriteString(tw, contents)
	check(err)
	check(tw.Close())
	check(zw.Close())
	return bytes.NewReader(buf.Bytes())
}
