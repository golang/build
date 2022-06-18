// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The stage0 command looks up the buildlet's URL from its environment
// (GCE metadata service, EC2, etc), downloads it, and runs
// it. If not on GCE, such as when in a Linux Docker container being
// developed and tested locally, the stage0 instead looks for the
// META_BUILDLET_BINARY_URL environment to have a URL to the buildlet
// binary.
//
// The stage0 binary is typically baked into the VM or container
// images or manually copied to dedicated once and is typically never
// auto-updated. Changes to this binary should be rare, as it's
// difficult and slow to roll out. Any per-host-type logic to do at
// start-up should be done in x/build/cmd/buildlet instead, which is
// re-downloaded once per build, and rolls out easily.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/build/internal/httpdl"
	"golang.org/x/build/internal/untar"
)

// This lets us be lazy and put the stage0 start-up in rc.local where
// it might race with the network coming up, rather than write proper
// upstart+systemd+init scripts:
var networkWait = flag.Duration("network-wait", 0, "if zero, a default is used if needed")

const osArch = runtime.GOOS + "/" + runtime.GOARCH

const attr = "buildlet-binary-url"

// untar helper, for the Windows image prep script.
var (
	untarFile    = flag.String("untar-file", "", "if non-empty, tar.gz to untar to --untar-dest-dir")
	untarDestDir = flag.String("untar-dest-dir", "", "destination directory to untar --untar-file to")
)

// configureSerialLogOutput and closeSerialLogOutput are set non-nil
// on some platforms to configure log output to go to the serial
// console and to close the serial port, respectively.
var (
	configureSerialLogOutput func()
	closeSerialLogOutput     func()
)

var timeStart = time.Now()

func main() {
	if configureSerialLogOutput != nil {
		configureSerialLogOutput()
	}
	log.SetPrefix("stage0: ")
	flag.Parse()

	if *untarFile != "" {
		log.Printf("running in untar mode, untarring %q to %q", *untarFile, *untarDestDir)
		untarMode()
		log.Printf("done untarring; exiting")
		return
	}
	log.Printf("bootstrap binary running")

	var isMacStadiumVM bool
	switch osArch {
	case "linux/arm":
		switch env := os.Getenv("GO_BUILDER_ENV"); env {
		case "host-linux-arm-aws":
			// No setup currently.
		default:
			panic(fmt.Sprintf("unknown/unspecified $GO_BUILDER_ENV value %q", env))
		}
	case "linux/arm64":
		switch env := os.Getenv("GO_BUILDER_ENV"); env {
		case "host-linux-arm64-aws":
			// No special setup.
		default:
			panic(fmt.Sprintf("unknown/unspecified $GO_BUILDER_ENV value %q", env))
		}
	case "darwin/amd64":
		// The MacStadium builders' baked-in stage0.sh
		// bootstrap file doesn't set GO_BUILDER_ENV
		// unfortunately, so use the filename it runs its
		// downloaded bootstrap URL to determine whether we're
		// in that environment.
		isMacStadiumVM = len(os.Args) > 0 && strings.HasSuffix(os.Args[0], "run-builder")
		log.Printf("isMacStadiumVM = %v", isMacStadiumVM)
		os.Setenv("GO_BUILDER_ENV", "macstadium_vm")
	}

	if !awaitNetwork() {
		sleepFatalf("network didn't become reachable")
	}
	timeNetwork := time.Now()
	netDelay := prettyDuration(timeNetwork.Sub(timeStart))
	log.Printf("network up after %v", netDelay)

Download:
	// Note: we name it ".exe" for Windows, but the name also
	// works fine on Linux, etc.
	target := filepath.FromSlash("./buildlet.exe")
	if err := download(target, buildletURL()); err != nil {
		sleepFatalf("Downloading %s: %v", buildletURL(), err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0755); err != nil {
			log.Fatal(err)
		}
	}
	downloadDelay := prettyDuration(time.Since(timeNetwork))
	log.Printf("downloaded buildlet in %v", downloadDelay)

	env := os.Environ()
	if isUnix() && os.Getuid() == 0 {
		if os.Getenv("USER") == "" {
			env = append(env, "USER=root")
		}
		if os.Getenv("HOME") == "" {
			env = append(env, "HOME=/root")
		}
	}
	env = append(env, fmt.Sprintf("GO_STAGE0_NET_DELAY=%v", netDelay))
	env = append(env, fmt.Sprintf("GO_STAGE0_DL_DELAY=%v", downloadDelay))

	cmd := exec.Command(target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	// buildEnv is set by some builders. It's increasingly set by new ones.
	// It predates the buildtype-vs-hosttype split, so the values aren't
	// always host types, but they're often host types. They should probably
	// be host types in the future, or we can introduce GO_BUILD_HOST_TYPE
	// to be explicit and kill off GO_BUILDER_ENV.
	buildEnv := os.Getenv("GO_BUILDER_ENV")

	switch buildEnv {
	case "host-linux-arm-aws":
		cmd.Args = append(cmd.Args, os.ExpandEnv("--workdir=${WORKDIR}"))
	case "host-linux-arm64-aws":
		cmd.Args = append(cmd.Args, os.ExpandEnv("--workdir=${WORKDIR}"))
	case "host-linux-mipsle-mengzhuo":
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
		cmd.Args = append(cmd.Args, os.ExpandEnv("--workdir=${WORKDIR}"))
	case "host-linux-loong64-3a5000":
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
		cmd.Args = append(cmd.Args, os.ExpandEnv("--workdir=${WORKDIR}"))
	case "host-linux-mips64le-rtrk":
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
		cmd.Args = append(cmd.Args, os.ExpandEnv("--workdir=${WORKDIR}"))
		cmd.Args = append(cmd.Args, os.ExpandEnv("--hostname=${GO_BUILDER_ENV}"))
	case "host-linux-mips64-rtrk":
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
		cmd.Args = append(cmd.Args, os.ExpandEnv("--workdir=${WORKDIR}"))
		cmd.Args = append(cmd.Args, os.ExpandEnv("--hostname=${GO_BUILDER_ENV}"))
	case "host-linux-ppc64le-power9-osu":
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
	case "host-linux-ppc64le-osu": // power8
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
	case "host-linux-ppc64-osu":
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
	case "host-linux-amd64-wsl":
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(buildEnv)...)
	}
	switch osArch {
	case "linux/s390x":
		cmd.Args = append(cmd.Args, "--workdir=/data/golang/workdir")
		cmd.Args = append(cmd.Args, reverseHostTypeArgs("host-linux-s390x")...)
	case "linux/arm64":
		switch buildEnv {
		case "host-linux-arm64-aws":
			// no special configuration
		default:
			panic(fmt.Sprintf("unknown/unspecified $GO_BUILDER_ENV value %q", env))
		}
	case "solaris/amd64", "illumos/amd64":
		hostType := buildEnv
		cmd.Args = append(cmd.Args, reverseHostTypeArgs(hostType)...)
	case "windows/arm64":
		switch buildEnv {
		case "host-windows-arm64-mini", "host-windows11-arm64-mini":
			cmd.Args = append(cmd.Args,
				"--halt=true",
				"--reverse-type="+buildEnv,
				"--coordinator=farmer.golang.org:443",
			)
		}
	}
	// Release the serial port (if we opened it) so the buildlet
	// process can open & write to it. At least on Windows, only
	// one process can have it open.
	if closeSerialLogOutput != nil {
		closeSerialLogOutput()
	}
	err := cmd.Run()
	if isMacStadiumVM {
		if err != nil {
			log.Printf("error running buildlet: %v", err)
			log.Printf("restarting in 2 seconds.")
			time.Sleep(2 * time.Second) // in case we're spinning, slow it down
		} else {
			log.Printf("buildlet process exited; restarting.")
		}
		// Some of the MacStadium VM environments reuse their
		// environment. Re-download the buildlet (if it
		// changed-- httpdl does conditional downloading) and
		// then re-run. At least on Sierra we never get this
		// far because the buildlet will halt the machine
		// before we get here. (and then cmd/makemac will
		// recreate the VM)
		// But if we get here, restart the process.
		goto Download
	}
	if err != nil {
		if configureSerialLogOutput != nil {
			configureSerialLogOutput()
		}
		sleepFatalf("Error running buildlet: %v", err)
	}
}

// reverseHostTypeArgs returns the default arguments for the buildlet
// for the provided host type. (one of the keys of the
// x/build/dashboard.Hosts map)
func reverseHostTypeArgs(hostType string) []string {
	return []string{
		"--halt=false",
		"--reverse-type=" + hostType,
		"--coordinator=farmer.golang.org:443",
	}
}

// awaitNetwork reports whether the network came up within 30 seconds,
// determined somewhat arbitrarily via a DNS lookup for google.com.
func awaitNetwork() bool {
	timeout := 30 * time.Second
	if runtime.GOOS == "windows" {
		timeout = 5 * time.Minute // empirically slower sometimes?
	}
	if *networkWait != 0 {
		timeout = *networkWait
	}
	deadline := time.Now().Add(timeout)
	var lastSpam time.Time
	log.Printf("waiting for network.")
	for time.Now().Before(deadline) {
		t0 := time.Now()
		if isNetworkUp() {
			return true
		}
		failAfter := time.Since(t0)
		if now := time.Now(); now.After(lastSpam.Add(5 * time.Second)) {
			log.Printf("network still down for %v; probe failure took %v",
				prettyDuration(time.Since(timeStart)),
				prettyDuration(failAfter))
			lastSpam = now
		}
		time.Sleep(1 * time.Second)
	}
	log.Printf("gave up waiting for network")
	return false
}

// isNetworkUp reports whether the network is up by hitting an
// known-up HTTP server. It might block for a few seconds before
// returning an answer.
func isNetworkUp() bool {
	const probeURL = "http://farmer.golang.org/netcheck" // 404 is fine.
	c := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			Proxy:             http.ProxyFromEnvironment,
		},
	}
	res, err := c.Get(probeURL)
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}

func buildletURL() string {
	if v := os.Getenv("META_BUILDLET_BINARY_URL"); v != "" {
		return v
	}

	if metadata.OnGCE() {
		v, err := metadata.InstanceAttributeValue(attr)
		if err == nil {
			return v
		}
		sleepFatalf("on GCE, but no META_BUILDLET_BINARY_URL env or instance attribute %q: %v", attr, err)
	}

	// Fallback:
	return fmt.Sprintf("https://storage.googleapis.com/go-builder-data/buildlet.%s-%s", runtime.GOOS, runtime.GOARCH)
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
	log.Printf("downloading %s to %s ...\n", url, file)
	const maxTry = 3
	var lastErr error
	for try := 1; try <= maxTry; try++ {
		if try > 1 {
			// network should be up by now per awaitNetwork, so just retry
			// shortly a few time on errors.
			time.Sleep(2)
		}
		err := httpdl.Download(file, url)
		if err == nil {
			fi, err := os.Stat(file)
			if err != nil {
				return err
			}
			log.Printf("downloaded %s (%d bytes)", file, fi.Size())
			return nil
		}
		lastErr = err
		log.Printf("try %d/%d download failure: %v", try, maxTry, err)
	}
	return lastErr
}

func aptGetInstall(pkgs ...string) {
	t0 := time.Now()
	args := append([]string{"--yes", "install"}, pkgs...)
	cmd := exec.Command("apt-get", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("error running apt-get install: %s", out)
	}
	log.Printf("stage0: apt-get installed %q in %v", pkgs, time.Since(t0).Round(time.Second/10))
}

func initBootstrapDir(destDir, tgzCache string) {
	t0 := time.Now()
	if err := os.MkdirAll(destDir, 0755); err != nil {
		log.Fatal(err)
	}
	latestURL := fmt.Sprintf("https://storage.googleapis.com/go-builder-data/gobootstrap-%s-%s.tar.gz",
		runtime.GOOS, runtime.GOARCH)
	if err := httpdl.Download(tgzCache, latestURL); err != nil {
		log.Fatalf("dowloading %s to %s: %v", latestURL, tgzCache, err)
	}
	log.Printf("synced %s to %s in %v", latestURL, tgzCache, time.Since(t0).Round(time.Second/10))

	t1 := time.Now()
	// TODO(bradfitz): rewrite this to use Go instead of shelling
	// out to tar? if this ever gets used on platforms besides
	// Unix. For Windows and Plan 9 we bake in the bootstrap
	// tarball into the image anyway. So this works for now.
	// Solaris might require tweaking to use gtar instead or
	// something.
	tar := exec.Command("tar", "zxf", tgzCache)
	tar.Dir = destDir
	out, err := tar.CombinedOutput()
	if err != nil {
		log.Fatalf("error untarring %s to %s: %s", tgzCache, destDir, out)
	}
	log.Printf("untarred %s to %s in %v", tgzCache, destDir, time.Since(t1).Round(time.Second/10))
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

func prettyDuration(d time.Duration) time.Duration {
	const round = time.Second / 10
	return d / round * round
}
