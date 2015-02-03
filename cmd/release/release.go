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
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/build/auth"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/oauth2"
	"google.golang.org/api/compute/v1"
)

var (
	revision = flag.String("rev", "", "Go revision to build")
	project  = flag.String("project", "symbolic-datum-552", "Google Cloud Project")
	zone     = flag.String("zone", "us-central1-a", "Compute Engine zone")
)

var builders = []string{
	"darwin-386",
	"darwin-amd64",
	"freebsd-386",
	"freebsd-amd64",
	"linux-386",
	"linux-amd64",
	"windows-386",
	"windows-amd64",
}

const timeout = time.Hour

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

	if *revision == "" {
		log.Fatal("must specify -rev flag")
	}

	var wg sync.WaitGroup
	for _, name := range builders {
		b, ok := dashboard.Builders[name]
		if !ok {
			log.Printf("unknown builder %q", name)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := makeRelease(b); err != nil {
				log.Printf("makeRelease(%q): %v", b.Name, err)
			}
		}()
	}
	// TODO(adg): show progress of running builders
	wg.Wait()
}

func makeRelease(bc dashboard.BuildConfig) (err error) {
	// Start VM
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
			log.Printf("%v: Sent create request. Waiting for operation.", instance)
		},
		OnInstanceCreated: func() {
			log.Printf("%v: Instance created.", instance)
		},
	})
	if err != nil {
		return err
	}
	log.Printf("%v: Instance up.", instance)

	defer func() {
		log.Printf("%v: Destroying VM.", instance)
		err := client.DestroyVM(projTokenSource(), *project, *zone, instance)
		if err != nil {
			log.Printf("%v: Destroying VM: %v", instance, err)
		}
	}()

	// Push source to VM
	const dir = "go"
	tar := "https://go.googlesource.com/go/+archive/" + *revision + ".tar.gz"
	if err := client.PutTarFromURL(tar, dir); err != nil {
		return err
	}
	log.Printf("%v: Pushed source to VM.", instance)

	if err := client.RemoveAll(preBuildCleanFiles...); err != nil {
		return err
	}
	log.Printf("%v: Cleaned repo (pre-build).", instance)

	// Execute build
	out := new(bytes.Buffer)
	mk := filepath.Join(dir, bc.MakeScript())
	remoteErr, err := client.Exec(mk, buildlet.ExecOpts{
		Output:   out,
		ExtraEnv: []string{"GOROOT_FINAL=" + bc.GorootFinal()},
	})
	if err != nil {
		return err
	}
	if remoteErr != nil {
		// TODO(adg): write log to file instead?
		return fmt.Errorf("Build failed: %v\nOutput:\n%v", remoteErr, out)
	}
	log.Printf("%v: Build complete.", instance)

	// TODO: build race-enabled tool chain

	// TODO: check out tools
	// TODO: build godoc, vet, cover

	// TODO: check out blog, add to misc
	// TODO: check out tour, add to misc

	if err := client.RemoveAll(postBuildCleanFiles...); err != nil {
		return err
	}
	log.Printf("%v: Cleaned repo (post-build).", instance)

	// Download tarball
	tgz, err := client.GetTar(dir)
	if err != nil {
		return err
	}
	filename := "go-version-" + bc.Name + ".tar.gz"
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
	log.Printf("%v: Wrote %q.", instance, filename)

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
