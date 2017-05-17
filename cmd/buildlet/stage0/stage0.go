// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The stage0 command looks up the buildlet's URL from its environment
// (GCE metadata service, scaleway, etc), downloads it, and runs
// it. If not on GCE, such as when in a Linux Docker container being
// developed and tested locally, the stage0 instead looks for the
// META_BUILDLET_BINARY_URL environment to have a URL to the buildlet
// binary.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/build/internal/httpdl"
	"golang.org/x/build/internal/untar"
)

// This lets us be lazy and put the stage0 start-up in rc.local where
// it might race with the network coming up, rather than write proper
// upstart+systemd+init scripts:
var networkWait = flag.Duration("network-wait", 0, "if non-zero, the time to wait for the network to come up.")

const osArch = runtime.GOOS + "/" + runtime.GOARCH

const attr = "buildlet-binary-url"

// untar helper, for the Windows image prep script.
var (
	untarFile    = flag.String("untar-file", "", "if non-empty, tar.gz to untar to --untar-dest-dir")
	untarDestDir = flag.String("untar-dest-dir", "", "destination directory to untar --untar-file to")
)

func main() {
	flag.Parse()

	if *untarFile != "" {
		untarMode()
		return
	}

	switch osArch {
	case "linux/arm":
		switch env := os.Getenv("GO_BUILDER_ENV"); env {
		case "linux-arm-arm5spacemonkey", "host-linux-arm-scaleway":
			// No setup currently.
		default:
			panic(fmt.Sprintf("unknown/unspecified $GO_BUILDER_ENV value %q", env))
		}
	case "linux/arm64":
		switch env := os.Getenv("GO_BUILDER_ENV"); env {
		case "host-linux-arm64-packet", "host-linux-arm64-linaro":
			// No special setup.
		default:
			panic(fmt.Sprintf("unknown/unspecified $GO_BUILDER_ENV value %q", env))
		}
	case "linux/ppc64":
		initOregonStatePPC64()
	case "linux/ppc64le":
		initOregonStatePPC64le()
	}

	if !awaitNetwork() {
		sleepFatalf("network didn't become reachable")
	}

	// Note: we name it ".exe" for Windows, but the name also
	// works fine on Linux, etc.
	target := filepath.FromSlash("./buildlet.exe")
	if err := download(target, buildletURL()); err != nil {
		sleepFatalf("Downloading %s: %v", buildletURL, err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0755); err != nil {
			log.Fatal(err)
		}
	}

	env := os.Environ()
	if isUnix() && os.Getuid() == 0 {
		if os.Getenv("USER") == "" {
			env = append(env, "USER=root")
		}
		if os.Getenv("HOME") == "" {
			env = append(env, "HOME=/root")
		}
	}

	cmd := exec.Command(target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	switch buildenv := os.Getenv("GO_BUILDER_ENV"); buildenv {
	case "linux-arm-arm5spacemonkey":
		cmd.Args = append(cmd.Args, legacyReverseBuildletArgs(buildenv)...)
	case "host-linux-arm-scaleway":
		scalewayArgs := append(
			legacyReverseBuildletArgs(buildenv),
			"--hostname="+os.Getenv("HOSTNAME"),
		)
		cmd.Args = append(cmd.Args,
			scalewayArgs...,
		)
	}
	switch osArch {
	case "linux/s390x":
		cmd.Args = append(cmd.Args, "--workdir=/data/golang/workdir")
		cmd.Args = append(cmd.Args, legacyReverseBuildletArgs("linux-s390x-ibm")...)
	case "linux/arm64":
		switch v := os.Getenv("GO_BUILDER_ENV"); v {
		case "host-linux-arm64-packet", "host-linux-arm64-linaro":
			hostname := os.Getenv("HOSTNAME") // if empty, docker container name is used
			cmd.Args = append(cmd.Args,
				"--reverse-type="+v,
				"--workdir=/workdir",
				"--hostname="+hostname,
				"--halt=false",
				"--reboot=false",
				"--coordinator=farmer.golang.org:443",
			)
		default:
			panic(fmt.Sprintf("unknown/unspecified $GO_BUILDER_ENV value %q", env))
		}
	case "linux/ppc64":
		cmd.Args = append(cmd.Args, legacyReverseBuildletArgs("linux-ppc64-buildlet")...)
	case "linux/ppc64le":
		cmd.Args = append(cmd.Args, legacyReverseBuildletArgs("linux-ppc64le-buildlet")...)
	case "solaris/amd64":
		cmd.Args = append(cmd.Args, legacyReverseBuildletArgs("solaris-amd64-smartosbuildlet")...)
	}
	if err := cmd.Run(); err != nil {
		sleepFatalf("Error running buildlet: %v", err)
	}
}

// legacyReverseBuildletArgs passes builder as the deprecated --reverse flag.
// New code should use --reverse-type instead.
func legacyReverseBuildletArgs(builder string) []string {
	return []string{
		"--halt=false",
		"--reverse=" + builder,
		"--coordinator=farmer.golang.org:443",
	}
}

// awaitNetwork reports whether the network came up within 30 seconds,
// determined somewhat arbitrarily via a DNS lookup for google.com.
func awaitNetwork() bool {
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if addrs, _ := net.LookupIP("google.com"); len(addrs) > 0 {
			log.Printf("network is up.")
			return true
		}
		log.Printf("waiting for network...")
	}
	log.Printf("gave up waiting for network")
	return false
}

func buildletURL() string {
	if os.Getenv("GO_BUILDER_ENV") == "linux-arm-arm5spacemonkey" {
		return "https://storage.googleapis.com/go-builder-data/buildlet.linux-arm-arm5"
	}
	switch osArch {
	case "linux/s390x":
		return "https://storage.googleapis.com/go-builder-data/buildlet.linux-s390x"
	case "linux/arm64":
		return "https://storage.googleapis.com/go-builder-data/buildlet.linux-arm64"
	case "linux/ppc64":
		return "https://storage.googleapis.com/go-builder-data/buildlet.linux-ppc64"
	case "linux/ppc64le":
		return "https://storage.googleapis.com/go-builder-data/buildlet.linux-ppc64le"
	case "solaris/amd64":
		return "https://storage.googleapis.com/go-builder-data/buildlet.solaris-amd64"
	}
	// The buildlet download URL is located in an env var
	// when the buildlet is not running on GCE, or is running
	// on Kubernetes.
	if !metadata.OnGCE() || os.Getenv("IN_KUBERNETES") == "1" {
		if v := os.Getenv("META_BUILDLET_BINARY_URL"); v != "" {
			return v
		}
		sleepFatalf("Not on GCE, and no META_BUILDLET_BINARY_URL specified.")
	}
	v, err := metadata.InstanceAttributeValue(attr)
	if err != nil {
		sleepFatalf("Failed to look up %q attribute value: %v", attr, err)
	}
	return v
}

func sleepFatalf(format string, args ...interface{}) {
	log.Printf(format, args...)
	if runtime.GOOS == "windows" {
		log.Printf("(sleeping for 1 minute before failing)")
		time.Sleep(time.Minute) // so user has time to see it in cmd.exe, maybe
	}
	os.Exit(1)
}

func download(file, url string) error {
	log.Printf("Downloading %s to %s ...\n", url, file)
	deadline := time.Now().Add(*networkWait)
	for {
		err := httpdl.Download(file, url)
		if err == nil {
			fi, _ := os.Stat(file)
			log.Printf("Downloaded %s (%d bytes)", file, fi.Size())
			return nil
		}
		// TODO(bradfitz): delete this whole download function
		// and move this functionality into a "WaitNetwork"
		// function somewhere?
		if time.Now().Before(deadline) {
			time.Sleep(1 * time.Second)
			continue
		}
		return err
	}
}

func aptGetInstall(pkgs ...string) {
	args := append([]string{"--yes", "install"}, pkgs...)
	cmd := exec.Command("apt-get", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("error running apt-get install: %s", out)
	}
}

func initBootstrapDir(destDir, tgzCache string) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		log.Fatal(err)
	}
	// TODO(bradfitz): rewrite this to use Go instead of curl+tar
	// if this ever gets used on platforms besides Unix. For
	// Windows and Plan 9 we bake in the bootstrap tarball into
	// the image anyway. So this works for now. Solaris might require
	// tweaking to use gtar instead or something.
	latestURL := fmt.Sprintf("https://storage.googleapis.com/go-builder-data/gobootstrap-%s-%s.tar.gz",
		runtime.GOOS, runtime.GOARCH)
	curl := exec.Command("/usr/bin/curl", "-R", "-o", tgzCache, "-z", tgzCache, latestURL)
	out, err := curl.CombinedOutput()
	if err != nil {
		log.Fatalf("curl error fetching %s to %s: %s", latestURL, out, err)
	}
	tar := exec.Command("tar", "zxf", tgzCache)
	tar.Dir = destDir
	out, err = tar.CombinedOutput()
	if err != nil {
		log.Fatalf("error untarring %s to %s: %s", tgzCache, destDir, out)
	}
}

func initOregonStatePPC64() {
	aptGetInstall("gcc", "strace", "libc6-dev", "gdb")
	initBootstrapDir("/usr/local/go-bootstrap", "/usr/local/go-bootstrap.tar.gz")
}

func initOregonStatePPC64le() {
	aptGetInstall("gcc", "strace", "libc6-dev", "gdb")
	initBootstrapDir("/usr/local/go-bootstrap", "/usr/local/go-bootstrap.tar.gz")
}

func isUnix() bool {
	switch runtime.GOOS {
	case "plan9", "windows":
		return false
	}
	return true
}

func untarMode() {
	if *untarDestDir == "" {
		log.Fatal("--untar-dest-dir must not be empty")
	}
	if fi, err := os.Stat(*untarDestDir); err != nil || !fi.IsDir() {
		if err != nil {
			log.Fatalf("--untar-dest-dir %q: %v", *untarDestDir, err)
		}
		log.Fatalf("--untar-dest-dir %q not a directory.", *untarDestDir)
	}
	f, err := os.Open(*untarFile)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := untar.Untar(f, *untarDestDir); err != nil {
		log.Fatalf("Untarring %q to %q: %v", *untarFile, *untarDestDir, err)
	}
}
