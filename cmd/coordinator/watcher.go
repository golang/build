// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Code related to managing the 'watcher' child process in
// a Docker container.

package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"google.golang.org/cloud/compute/metadata"
)

var (
	watchers = map[string]watchConfig{} // populated at startup, keyed by repo, e.g. "https://go.googlesource.com/go"
)

type watchConfig struct {
	repo       string        // "https://go.googlesource.com/go"
	dash       string        // "https://build.golang.org/" (must end in /)
	interval   time.Duration // Polling interval
	mirrorBase string        // "https://github.com/golang/" or empty to disable mirroring
	netHost    bool          // run docker container in the host's network namespace
	httpAddr   string
}

type imageInfo struct {
	url string // of tar file

	mu      sync.Mutex
	lastMod string
}

var images = map[string]*imageInfo{
	"go-commit-watcher": {url: "https://storage.googleapis.com/go-builder-data/docker-commit-watcher.tar.gz"},
}

const gitArchiveAddr = "127.0.0.1:21536" // 21536 == keys above WATCH

func startWatchers() {
	mirrorBase := "https://github.com/golang/"
	if inStaging {
		mirrorBase = "" // don't mirror from dev cluster
	}
	addWatcher(watchConfig{
		repo:       "https://go.googlesource.com/go",
		dash:       dashBase(),
		mirrorBase: mirrorBase,
		netHost:    true,
		httpAddr:   gitArchiveAddr,
	})
	addWatcher(watchConfig{repo: "https://go.googlesource.com/gofrontend", dash: dashBase() + "gccgo/"})

	go cleanUpOldContainers()

	stopWatchers() // clean up before we start new ones
	for _, watcher := range watchers {
		if err := startWatching(watchers[watcher.repo]); err != nil {
			log.Printf("Error starting watcher for %s: %v", watcher.repo, err)
		}
	}
}

// Stop any previous go-commit-watcher Docker tasks, so they don't
// pile up upon restarts of the coordinator.
func stopWatchers() {
	out, err := exec.Command("docker", "ps").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "go-commit-watcher:") {
			continue
		}
		f := strings.Fields(line)
		exec.Command("docker", "rm", "-f", "-v", f[0]).Run()
	}
}

// returns the part after "docker run"
func (conf watchConfig) dockerRunArgs() (args []string) {
	log.Printf("Running watcher with master key %q", masterKey())
	if key := masterKey(); len(key) > 0 {
		tmpKey := "/tmp/watcher.buildkey"
		if _, err := os.Stat(tmpKey); err != nil {
			if err := ioutil.WriteFile(tmpKey, key, 0600); err != nil {
				log.Fatal(err)
			}
		}
		// Images may look for .gobuildkey in / or /root, so provide both.
		// TODO(adg): fix images that look in the wrong place.
		args = append(args, "-v", tmpKey+":/.gobuildkey")
		args = append(args, "-v", tmpKey+":/root/.gobuildkey")
	}
	if conf.netHost {
		args = append(args, "--net=host")
	}
	args = append(args,
		"go-commit-watcher",
		"/usr/local/bin/watcher",
		"-repo="+conf.repo,
		"-dash="+conf.dash,
		"-poll="+conf.interval.String(),
		"-http="+conf.httpAddr,
	)
	if conf.mirrorBase != "" {
		dst, err := url.Parse(conf.mirrorBase)
		if err != nil {
			log.Fatalf("Bad mirror destination URL: %q", conf.mirrorBase)
		}
		dst.User = url.UserPassword(mirrorCred())
		args = append(args, "-mirror="+dst.String())
	}
	return
}

func addWatcher(c watchConfig) {
	if c.repo == "" {
		c.repo = "https://go.googlesource.com/go"
	}
	if c.dash == "" {
		c.dash = "https://build.golang.org/"
	}
	if c.interval == 0 {
		c.interval = 10 * time.Second
	}
	watchers[c.repo] = c
}

func condUpdateImage(img string) error {
	ii := images[img]
	if ii == nil {
		return fmt.Errorf("image %q doesn't exist", img)
	}
	ii.mu.Lock()
	defer ii.mu.Unlock()
	u := ii.url
	if inStaging {
		u = strings.Replace(u, "go-builder-data", "dev-go-builder-data", 1)
	}
	res, err := http.Head(u)
	if err != nil {
		return fmt.Errorf("Error checking %s: %v", u, err)
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("Error checking %s: %v", u, res.Status)
	}
	if res.Header.Get("Last-Modified") == ii.lastMod {
		return nil
	}

	res, err = http.Get(u)
	if err != nil || res.StatusCode != 200 {
		return fmt.Errorf("Get after Head failed for %s: %v, %v", u, err, res)
	}
	defer res.Body.Close()

	log.Printf("Running: docker load of %s\n", u)
	cmd := exec.Command("docker", "load")
	cmd.Stdin = res.Body

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if cmd.Run(); err != nil {
		log.Printf("Failed to pull latest %s from %s and pipe into docker load: %v, %s", img, u, err, out.Bytes())
		return err
	}
	ii.lastMod = res.Header.Get("Last-Modified")
	return nil
}

func startWatching(conf watchConfig) (err error) {
	defer func() {
		if err != nil {
			restartWatcherSoon(conf)
		}
	}()
	log.Printf("Starting watcher for %v", conf.repo)
	if err := condUpdateImage("go-commit-watcher"); err != nil {
		log.Printf("Failed to setup container for commit watcher: %v", err)
		return err
	}

	cmd := exec.Command("docker", append([]string{"run", "-d"}, conf.dockerRunArgs()...)...)
	all, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Docker run for commit watcher = err:%v, output: %s", err, all)
		return err
	}
	container := strings.TrimSpace(string(all))
	// Start a goroutine to wait for the watcher to die.
	go func() {
		exec.Command("docker", "wait", container).Run()
		out, _ := exec.Command("docker", "logs", container).CombinedOutput()
		exec.Command("docker", "rm", "-v", container).Run()
		const maxLogBytes = 1 << 10
		if len(out) > maxLogBytes {
			out = out[len(out)-maxLogBytes:]
		}
		log.Printf("Watcher %v crashed. Restarting soon. Logs: %s", conf.repo, out)
		restartWatcherSoon(conf)
	}()
	return nil
}

func restartWatcherSoon(conf watchConfig) {
	time.AfterFunc(30*time.Second, func() {
		startWatching(conf)
	})
}

// This is only for the watcher container, since all builds run in VMs
// now.
func cleanUpOldContainers() {
	for {
		for _, cid := range oldContainers() {
			log.Printf("Cleaning old container %v", cid)
			exec.Command("docker", "rm", "-v", cid).Run()
		}
		time.Sleep(60 * time.Second)
	}
}

func oldContainers() []string {
	out, _ := exec.Command("docker", "ps", "-a", "--filter=status=exited", "--no-trunc", "-q").Output()
	return strings.Fields(string(out))
}

func mirrorCred() (username, password string) {
	mirrorCredOnce.Do(loadMirrorCred)
	return mirrorCredCache.username, mirrorCredCache.password
}

var (
	mirrorCredOnce  sync.Once
	mirrorCredCache struct {
		username, password string
	}
)

func loadMirrorCred() {
	cred, err := metadata.ProjectAttributeValue("mirror-credentials")
	if err != nil {
		log.Printf("No mirror credentials available: %v", err)
		return
	}
	p := strings.SplitN(strings.TrimSpace(cred), ":", 2)
	if len(p) != 2 {
		log.Fatalf("Bad mirror credentials: %q", cred)
	}
	mirrorCredCache.username, mirrorCredCache.password = p[0], p[1]
}
