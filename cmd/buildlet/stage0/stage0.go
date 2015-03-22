// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The stage0 command looks up the buildlet's URL from the GCE
// metadata service, downloads it, and runs it. If not on GCE, such as
// when in a Linux Docker container being developed and tested
// locally, the stage0 instead looks for the META_BUILDLET_BINARY_URL
// environment to have a URL to the buildlet binary.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"google.golang.org/cloud/compute/metadata"
)

// This lets us be lazy and put the stage0 start-up in rc.local where
// it might race with the network coming up, rather than write proper
// upstart+systemd+init scripts:
var networkWait = flag.Duration("network-wait", 0, "if non-zero, the time to wait for the network to come up.")

const attr = "buildlet-binary-url"

func main() {
	flag.Parse()

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
	if err := cmd.Run(); err != nil {
		sleepFatalf("Error running buildlet: %v", err)
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
	if !metadata.OnGCE() {
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
