// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command release builds a Go release.
package main

import (
	"bytes"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/auth"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/oauth2"
	"google.golang.org/api/compute/v1"
)

var (
	rev      = flag.String("rev", "", "Go revision to build")
	toolsRev = flag.String("tools", "", "Tools revision to build")
	project  = flag.String("project", "symbolic-datum-552", "Google Cloud Project")
	zone     = flag.String("zone", "us-central1-a", "Compute Engine zone")
	target   = flag.String("target", "", "If specified, build specific target platform")
)

const timeout = time.Hour

type Build struct {
	OS, Arch string

	Race   bool // Build race detector.
	Static bool // Statically-link binaries.

	Builder string // Key for dashboard.Builders.
}

func (b *Build) String() string {
	return fmt.Sprintf("%v-%v", b.OS, b.Arch)
}

var builds = []*Build{
	{
		OS:      "linux",
		Arch:    "386",
		Builder: "linux-amd64",
	},
	{
		OS:      "linux",
		Arch:    "amd64",
		Race:    true,
		Static:  true,
		Builder: "linux-amd64",
	},
	{
		OS:      "freebsd",
		Arch:    "386",
		Builder: "freebsd-386-gce101",
	},
	{
		OS:      "freebsd",
		Arch:    "amd64",
		Race:    true,
		Builder: "freebsd-amd64-gce101",
	},
}

const toolsRepo = "golang.org/x/tools"

var toolPaths = []string{
	"golang.org/x/tools/cmd/cover",
	"golang.org/x/tools/cmd/godoc",
	"golang.org/x/tools/cmd/vet",
}

var preBuildCleanFiles = []string{
	".gitattributes",
	".gitignore",
	".hgignore",
	".hgtags",
	"misc/dashboard",
}

var postBuildCleanFiles = []string{
	"VERSION.cache",
}

func main() {
	flag.Parse()

	if *rev == "" {
		log.Fatal("must specify -rev flag")
	}
	if *toolsRev == "" {
		log.Fatal("must specify -tools flag")
	}

	var wg sync.WaitGroup
	for _, b := range builds {
		b := b
		if *target != "" && b.String() != *target {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.make() // error logged by make function
		}()
	}
	// TODO(adg): show progress of running builders
	wg.Wait()
}

func (b *Build) make() (err error) {
	bc, ok := dashboard.Builders[b.Builder]
	if !ok {
		return fmt.Errorf("unknown builder: %v", bc)
	}

	// Start VM
	log.Printf("%v: Starting VM.", b)
	keypair, err := buildlet.NewKeyPair()
	if err != nil {
		return err
	}
	instance := fmt.Sprintf("release-%v-%v-rn%v", os.Getenv("USER"), bc.Name, randHex(6))
	client, err := buildlet.StartNewVM(projTokenSource(), instance, bc.Name, buildlet.VMOpts{
		Zone:        *zone,
		ProjectID:   *project,
		TLS:         keypair,
		DeleteIn:    timeout,
		Description: fmt.Sprintf("release buildlet for %s", os.Getenv("USER")),
		OnInstanceRequested: func() {
			log.Printf("%v: Sent create request. Waiting for operation.", b)
		},
		OnInstanceCreated: func() {
			log.Printf("%v: Instance created.", b)
		},
	})
	if err != nil {
		return err
	}
	log.Printf("%v: Instance %v up.", b, instance)

	defer func() {
		if err != nil {
			log.Printf("%v: %v", b, err)
		}
		log.Printf("%v: Destroying VM.", b)
		err := client.DestroyVM(projTokenSource(), *project, *zone, instance)
		if err != nil {
			log.Printf("%v: Destroying VM: %v", b, err)
		}
	}()

	work, err := client.WorkDir()
	if err != nil {
		return err
	}

	const (
		goDir    = "go"
		goPath   = "gopath"
		toolsDir = goPath + "/src/" + toolsRepo
	)

	// Push source to VM
	log.Printf("%v: Pushing source to VM.", b)
	tar := "https://go.googlesource.com/go/+archive/" + *rev + ".tar.gz"
	if err := client.PutTarFromURL(tar, goDir); err != nil {
		return err
	}
	tar = "https://go.googlesource.com/tools/+archive/" + *toolsRev + ".tar.gz"
	if err := client.PutTarFromURL(tar, toolsDir); err != nil {
		return err
	}

	log.Printf("%v: Cleaning goroot (pre-build).", b)
	if err := client.RemoveAll(addPrefix(goDir, preBuildCleanFiles)...); err != nil {
		return err
	}

	// Set up build environment.
	sep := "/"
	if b.OS == "windows" {
		sep = "\\"
	}
	env := []string{
		"GOOS=" + b.OS,
		"GOARCH=" + b.Arch,
		"GOHOSTOS=" + b.OS,
		"GOHOSTARCH=" + b.Arch,
		"GOROOT_FINAL=" + bc.GorootFinal(),
		"GOROOT=" + work + sep + goDir,
		"GOPATH=" + work + sep + goPath,
	}
	if b.Static {
		env = append(env, "GO_DISTFLAGS=-s")
	}

	// Execute build
	log.Printf("%v: Building.", b)
	out := new(bytes.Buffer)
	mk := filepath.Join(goDir, bc.MakeScript())
	remoteErr, err := client.Exec(mk, buildlet.ExecOpts{
		Output:   out,
		ExtraEnv: env,
	})
	if err != nil {
		return err
	}
	if remoteErr != nil {
		// TODO(adg): write log to file instead?
		return fmt.Errorf("Build failed: %v\nOutput:\n%v", remoteErr, out)
	}

	goCmd := path.Join(goDir, "bin/go")
	if b.OS == "windows" {
		goCmd += ".exe"
	}
	runGo := func(args ...string) error {
		out := new(bytes.Buffer)
		remoteErr, err := client.Exec(goCmd, buildlet.ExecOpts{
			Output:   out,
			Args:     args,
			ExtraEnv: env,
		})
		if err != nil {
			return err
		}
		if remoteErr != nil {
			return fmt.Errorf("go %v: %v\n%s", strings.Join(args, " "), remoteErr, out)
		}
		return nil
	}

	if b.Race {
		log.Printf("%v: Building race detector.", b)

		// Because on release branches, go install -a std is a NOP,
		// we have to resort to delete pkg/$GOOS_$GOARCH, install -race,
		// and then reinstall std so that we're not left with a slower,
		// race-enabled cmd/go, etc.
		if err := client.RemoveAll(path.Join(goDir, "pkg", b.OS+"_"+b.Arch)); err != nil {
			return err
		}
		if err := runGo("tool", "dist", "install", "runtime"); err != nil {
			return err
		}
		if err := runGo("install", "-race", "std"); err != nil {
			return err
		}
		if err := runGo("install", "std"); err != nil {
			return err
		}
		// Re-building go command leaves old versions of go.exe as go.exe~ on windows.
		// See (*builder).copyFile in $GOROOT/src/cmd/go/build.go for details.
		// Remove it manually.
		if b.OS == "windows" {
			if err := client.RemoveAll(goCmd + "~"); err != nil {
				return err
			}
		}
	}

	for _, p := range toolPaths {
		log.Printf("%v: Building %v.", b, p)
		if err := runGo("install", p); err != nil {
			return err
		}
	}

	// TODO(adg): check out blog, add to misc
	// TODO(adg): check out tour, add to misc

	log.Printf("%v: Cleaning goroot (post-build).", b)
	// Need to delete everything except the final "go" directory,
	// as we make the tarball relative to workdir.
	cleanFiles := append(addPrefix(goDir, postBuildCleanFiles), goPath)
	if err := client.RemoveAll(cleanFiles...); err != nil {
		return err
	}

	// TODO(adg): build msi files on windows and pkg files on osx

	// Download tarball
	log.Printf("%v: Downloading tarball", b)
	tgz, err := client.GetTar(".")
	if err != nil {
		return err
	}
	// TODO(adg): deduce actual version
	version := "VERSION"
	filename := "go." + version + "." + b.String() + ".tar.gz"
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, tgz); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	log.Printf("%v: Wrote %q.", b, filename)

	return nil
}

func projTokenSource() oauth2.TokenSource {
	ts, err := auth.ProjectTokenSource(*project, compute.ComputeScope)
	if err != nil {
		log.Fatalf("Failed to get OAuth2 token source for project %s: %v", *project, err)
	}
	return ts
}

func randHex(n int) string {
	buf := make([]byte, n/2)
	_, err := rand.Read(buf)
	if err != nil {
		panic("Failed to get randomness: " + err.Error())
	}
	return fmt.Sprintf("%x", buf)
}

func addPrefix(prefix string, in []string) []string {
	var out []string
	for _, s := range in {
		out = append(out, path.Join(prefix, s))
	}
	return out
}
