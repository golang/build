// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	goversion "go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BootstrapVersion returns the Go bootstrap version required
// for the named version of Go. If the version needs no bootstrap
// (that is, if it's before Go 1.5), BootstrapVersion returns an empty version.
func BootstrapVersion(version string) (string, error) {
	// go1 returns the version string for Go 1.N ("go1.N").
	go1 := func(N int) string { return fmt.Sprintf("go1.%d", N) }

	if goversion.Compare(version, go1(5)) < 0 {
		return "", nil
	}
	if goversion.Compare(version, go1(20)) < 0 {
		return go1(4), nil
	}
	if goversion.Compare(version, go1(22)) < 0 {
		return go1(17), nil
	}
	if goversion.Compare(version, go1(1000)) > 0 {
		return "", fmt.Errorf("invalid version %q", version)
	}
	for i := 24; ; i += 2 {
		if goversion.Compare(version, go1(i)) < 0 {
			// 1.24 will switch to 1.22; before that we used 1.20
			// 1.26 will switch to 1.24; before that we used 1.22
			// ...
			return go1(i - 4), nil
		}
	}
}

// BootstrapDir returns the name of a directory containing the GOROOT
// for a fully built bootstrap toolchain with the given version.
func (r *Report) BootstrapDir(version string) (dir string, err error) {
	for _, b := range r.Bootstraps {
		if b.Version == version {
			return b.Dir, b.Err
		}
	}

	dir = filepath.Join(r.Work, "bootstrap-"+version)
	b := &Bootstrap{
		Version: version,
		Dir:     dir,
		Err:     fmt.Errorf("bootstrap %s cycle", version),
	}
	b.Log.Name = "bootstrap " + version
	r.Bootstraps = append(r.Bootstraps, b)

	defer func() {
		if err != nil {
			b.Log.Printf("%v", err)
			err = fmt.Errorf("bootstrap %s: %v", version, err)
		}
		b.Err = err
	}()

	if r.Full {
		return b.Dir, r.BootstrapBuild(b, version)
	}
	return b.Dir, r.BootstrapPrebuilt(b, version)
}

// BootstrapPrebuilt downloads a prebuilt toolchain.
func (r *Report) BootstrapPrebuilt(b *Bootstrap, version string) error {
	for _, dl := range r.dl {
		if strings.HasPrefix(dl.Version, version+".") {
			b.Log.Printf("using %s binary distribution for %s", dl.Version, version)
			version = dl.Version
			break
		}
	}

	url := "https://go.dev/dl/" + version + "." + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	unpack := UnpackTarGz
	if runtime.GOOS == "windows" {
		url = strings.TrimSuffix(url, ".tar.gz") + ".zip"
		unpack = UnpackZip
	}

	arch, err := Get(&b.Log, url)
	if err != nil {
		return err
	}
	if err := unpack(b.Dir, arch); err != nil {
		return err
	}
	b.Dir = filepath.Join(b.Dir, "go")
	return nil
}

// BootstrapBuild builds the named bootstrap toolchain and returns
// the directory containing the GOROOT for the build.
func (r *Report) BootstrapBuild(b *Bootstrap, version string) error {
	tgz, err := GerritTarGz(&b.Log, "go", "refs/heads/release-branch."+version)
	if err != nil {
		return err
	}
	if err := UnpackTarGz(b.Dir, tgz); err != nil {
		return err
	}
	return r.Build(&b.Log, b.Dir, version, nil, nil)
}

// Build runs a Go make.bash/make.bat/make.rc in the named goroot
// which contains the named version of Go,
// with the additional environment and command-line arguments.
// The returned error is not logged.
// If an error happens during the build, the full output is logged to log,
// but the returned error simply says "make.bash in <goroot> failed".
func (r *Report) Build(log *Log, goroot, version string, env, args []string) error {
	bver, err := BootstrapVersion(version)
	if err != nil {
		return err
	}
	var bdir string
	if bver != "" {
		bdir, err = r.BootstrapDir(bver)
		if err != nil {
			return err
		}
	}

	make := "./make.bash"
	switch runtime.GOOS {
	case "windows":
		make = `.\make.bat`
	case "plan9":
		make = "./make.rc"
	}
	cmd := exec.Command(make, args...)
	cmd.Dir = filepath.Join(goroot, "src")
	cmd.Env = append(os.Environ(),
		"GOROOT="+goroot,
		"GOROOT_BOOTSTRAP="+bdir,
		"GOTOOLCHAIN=local", // keep bootstraps honest

		// Clear various settings that would leak into defaults
		// in the toolchain and change the generated binaries.
		// These are unlikely to be set to begin with, except
		// maybe $CC and $CXX, but if they are, the failures would
		// be mysterious.
		"CC=",
		"CC_FOR_TARGET=",
		"CGO_ENABLED=",
		"CXX=",
		"CXX_FOR_TARGET=",
		"GO386=",
		"GOAMD64=",
		"GOARM=",
		"GOBIN=",
		"GOEXPERIMENT=",
		"GOMIPS64=",
		"GOMIPS=",
		"GOPATH=",
		"GOPPC64=",
		"GOROOT_FINAL=",
		"GO_EXTLINK_ENABLED=",
		"GO_GCFLAGS=",
		"GO_LDFLAGS=",
		"GO_LDSO=",
		"PKG_CONFIG=",

		// If make.bash is run and GOROOT/bin isn't already in PATH,
		// make.bash prints what it hopes to be a useful suggestion:
		// "*** You need to add GOROOT/bin to your PATH."
		// Abide the suggestion and include GOROOT/bin in PATH here,
		// so that it's not printed unnecessarily. The hope is that
		// this way it'll stand out more when it's printed in other
		// appropriate contexts.
		"PATH="+filepath.Join(goroot, "bin")+string(filepath.ListSeparator)+os.Getenv("PATH"),
		"PWD="+cmd.Dir, // set PWD to handle symlinks in work dir
	)
	cmd.Env = append(cmd.Env, env...)
	log.Printf("running %s env=%v args=%v\nGOROOT=%s\nGOROOT_BOOTSTRAP=%s\n",
		make, env, args, goroot, bdir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("%s: %s\n%s", make, err, out)
		return fmt.Errorf("%s in %s failed", make, goroot)
	}
	log.Printf("%s completed:\n%s", make, out)
	return nil
}
