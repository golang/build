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
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"google.golang.org/cloud/compute/metadata"
)

// This lets us be lazy and put the stage0 start-up in rc.local where
// it might race with the network coming up, rather than write proper
// upstart+systemd+init scripts:
var networkWait = flag.Duration("network-wait", 0, "if non-zero, the time to wait for the network to come up.")

const attr = "buildlet-binary-url"

var (
	onScaleway   bool
	scalewayMeta scalewayMetadata
)

func main() {
	flag.Parse()

	if runtime.GOOS == "linux" && runtime.GOARCH == "arm" {
		if _, err := os.Stat("/usr/local/bin/oc-metadata"); err == nil {
			initScaleway()
		}
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
	cmd := exec.Command(target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if onScaleway {
		cmd.Args = append(cmd.Args, scalewayBuildletArgs()...)
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "s390x" {
		cmd.Args = append(cmd.Args, s390xBuildletArgs()...)
	}
	if err := cmd.Run(); err != nil {
		sleepFatalf("Error running buildlet: %v", err)
	}
}

func s390xBuildletArgs() []string {
	return []string{
		"--halt=false",
		"--workdir=/data/golang/workdir",
		"--reverse=linux-s390x-ibm",
		"--coordinator=farmer.golang.org:443",
	}
}

func scalewayBuildletArgs() []string {
	var modes []string // e.g. "linux-arm", "linux-arm-arm5"
	// tags are of form "buildkey_linux-arm_HEXHEXHEX"
	for _, tag := range scalewayMeta.Tags {
		if strings.HasPrefix(tag, "buildkey_") {
			parts := strings.Split(tag, "_")
			if len(parts) != 3 {
				log.Fatalf("invalid server tag %q", tag)
			}
			mode, buildkey := parts[1], parts[2]
			modes = append(modes, mode)
			file := "/root/.gobuildkey-" + mode
			if fi, err := os.Stat(file); err != nil || (err == nil && fi.Size() == 0) {
				if err := ioutil.WriteFile(file, []byte(buildkey), 0600); err != nil {
					log.Fatal(err)
				}
			}
		}
	}
	server := "farmer.golang.org:443"
	if scalewayMeta.IsStaging() {
		server = "104.154.113.235:443" // fixed IP, but no hostname.
	}
	return []string{
		"--workdir=/workdir",
		"--hostname=" + scalewayMeta.Hostname,
		"--halt=false",
		"--reverse=" + strings.Join(modes, ","),
		"--coordinator=" + server,
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
	// The buildlet download URL is located in an env var
	// when the buildlet is not running on GCE, or is running
	// on Kubernetes.
	if !metadata.OnGCE() || os.Getenv("IN_KUBERNETES") == "1" {
		if v := os.Getenv("META_BUILDLET_BINARY_URL"); v != "" {
			return v
		}
		if onScaleway {
			if scalewayMeta.IsStaging() {
				return "https://storage.googleapis.com/dev-go-builder-data/buildlet.linux-arm"
			} else {
				return "https://storage.googleapis.com/go-builder-data/buildlet.linux-arm"
			}
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
	if strings.HasPrefix(url, "https://storage.googleapis.com") {
		url += fmt.Sprintf("?%d", time.Now().Unix())
	}
	log.Printf("Downloading %s to %s ...\n", url, file)

	var res *http.Response
	var err error
	deadline := time.Now().Add(*networkWait)
	for {
		res, err = http.Get(url)
		if err != nil {
			if time.Now().Before(deadline) {
				time.Sleep(1 * time.Second)
				continue
			}
			return fmt.Errorf("Error fetching %v: %v", url, err)
		}
		break
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("HTTP status code of %s was %v", url, res.Status)
	}
	tmp := file + ".tmp"
	os.Remove(tmp)
	os.Remove(file)
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, res.Body)
	res.Body.Close()
	if err != nil {
		return fmt.Errorf("Error reading %v: %v", url, err)
	}
	f.Close()
	err = os.Rename(tmp, file)
	if err != nil {
		return err
	}
	log.Printf("Downloaded %s (%d bytes)", file, n)
	return nil
}

func initScaleway() {
	log.Printf("On scaleway.")
	onScaleway = true
	initScalewaySwap()
	initScalewayWorkdir()
	initScalewayMeta()
	initScalewayDNS()
	initScalewayGo14()
	log.Printf("Scaleway init complete; metadata is %+v", scalewayMeta)
}

type scalewayMetadata struct {
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	Tags     []string `json:"tags"`
}

// IsStaging reports whether this instance has a "staging" tag.
func (m *scalewayMetadata) IsStaging() bool {
	for _, t := range m.Tags {
		if t == "staging" {
			return true
		}
	}
	return false
}

func initScalewayMeta() {
	const metaURL = "http://169.254.42.42/conf?format=json"
	res, err := http.Get(metaURL)
	if err != nil {
		log.Fatalf("failed to get scaleway metadata: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Fatalf("failed to get scaleway metadata from %s: %v", metaURL, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&scalewayMeta); err != nil {
		log.Fatalf("invalid JSON from scaleway metadata URL %s: %v", metaURL, err)
	}
}

func initScalewayDNS() {
	setFileContents("/etc/resolv.conf", []byte("nameserver 8.8.8.8\n"))
}

func setFileContents(file string, contents []byte) {
	old, err := ioutil.ReadFile(file)
	if err == nil && bytes.Equal(old, contents) {
		return
	}
	if err := ioutil.WriteFile(file, contents, 0644); err != nil {
		log.Fatal(err)
	}
}

func initScalewaySwap() {
	const swapFile = "/swapfile"
	slurp, _ := ioutil.ReadFile("/proc/swaps")
	if strings.Contains(string(slurp), swapFile) {
		log.Printf("scaleway swapfile already active.")
		return
	}
	os.Remove(swapFile) // if it already exists, else ignore error
	log.Printf("Running fallocate on swapfile")
	if out, err := exec.Command("fallocate", "--length", "16GiB", swapFile).CombinedOutput(); err != nil {
		log.Fatalf("Failed to fallocate /swapfile: %v, %s", err, out)
	}
	log.Printf("Running mkswap")
	if out, err := exec.Command("mkswap", swapFile).CombinedOutput(); err != nil {
		log.Fatalf("Failed to mkswap /swapfile: %v, %s", err, out)
	}
	os.Chmod(swapFile, 0600)
	log.Printf("Running swapon")
	if out, err := exec.Command("swapon", swapFile).CombinedOutput(); err != nil {
		log.Fatalf("Failed to swapon /swapfile: %v, %s", err, out)
	}
}

func initScalewayWorkdir() {
	const dir = "/workdir"
	slurp, _ := ioutil.ReadFile("/proc/mounts")
	if strings.Contains(string(slurp), dir) {
		log.Printf("scaleway workdir already mounted")
		return
	}
	if err := os.MkdirAll("/workdir", 0755); err != nil {
		log.Fatal(err)
	}
	if out, err := exec.Command("mount",
		"-t", "tmpfs",
		"-o", "size=8589934592",
		"tmpfs", "/workdir").CombinedOutput(); err != nil {
		log.Fatalf("Failed to mount /buildtmp: %v, %s", err, out)
	}
}

func initScalewayGo14() {
	if fi, err := os.Stat("/usr/local/go"); err == nil && fi.IsDir() {
		log.Printf("go directory already exists.")
		return
	}
	os.RemoveAll("/usr/local/go") // in case it existed somehow, or as regular file
	if err := os.RemoveAll("/usr/local/go.tmp"); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll("/usr/local/go.tmp", 0755); err != nil {
		log.Fatal(err)
	}
	log.Printf("Downloading go1.4-linux-arm.tar.gz")
	if out, err := exec.Command("curl",
		"-o", "/usr/local/go.tmp/go.tar.gz",
		"--silent",
		"https://storage.googleapis.com/go-builder-data/go1.4-linux-arm.tar.gz",
	).CombinedOutput(); err != nil {
		log.Fatalf("Failed to download go1.4-linux-arm.tar.gz: %v, %s", err, out)
	}
	log.Printf("Extracting go1.4-linux-arm.tar.gz")
	if out, err := exec.Command("tar",
		"-C", "/usr/local/go.tmp",
		"-zx",
		"-f", "/usr/local/go.tmp/go.tar.gz",
	).CombinedOutput(); err != nil {
		log.Fatalf("Failed to untar go1.4-linux-arm.tar.gz: %v, %s", err, out)
	}
	if err := os.Rename("/usr/local/go.tmp", "/usr/local/go"); err != nil {
		log.Fatal(err)
	}
}
