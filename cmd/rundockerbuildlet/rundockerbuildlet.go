// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The rundockerbuildlet command loops forever and creates and cleans
// up Docker containers running reverse buildlets. It keeps a fixed
// number of them running at a time. See x/build/env/linux-arm64/packet/README
// for one example user.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	image    = flag.String("image", "", "docker image to run; required.")
	numInst  = flag.Int("n", 1, "number of containers to keep running at once")
	basename = flag.String("basename", "builder", "prefix before the builder number to use for the container names and host names")
	memory   = flag.String("memory", "3g", "memory limit flag for docker run")
	keyFile  = flag.String("key", "/etc/gobuild.key", "go build key file")
)

var buildKey []byte

func main() {
	flag.Parse()

	key, err := ioutil.ReadFile(*keyFile)
	if err != nil {
		log.Fatalf("error reading build key from --key=%s: %v", buildKey, err)
	}
	buildKey = bytes.TrimSpace(key)

	if *image == "" {
		log.Fatalf("docker --image is required")
	}

	log.Printf("Started. Will keep %d copies of %s running.", *numInst, *image)
	for {
		if err := checkFix(); err != nil {
			log.Print(err)
		}
		time.Sleep(time.Second) // TODO: docker wait on the running containers?
	}
}

func checkFix() error {
	running := map[string]bool{}

	out, err := exec.Command("docker", "ps", "-a", "--format", "{{.ID}} {{.Names}} {{.Status}}").Output()
	if err != nil {
		return fmt.Errorf("error running docker ps: %v", err)
	}
	// Out is like:
	// b1dc9ec2e646 packet14 Up 23 minutes
	// eeb458938447 packet11 Exited (0) About a minute ago
	// ...
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		f := strings.SplitN(line, " ", 3)
		if len(f) < 3 {
			continue
		}
		container, name, status := f[0], f[1], f[2]
		if !strings.HasPrefix(name, *basename) {
			continue
		}
		if strings.HasPrefix(status, "Exited") {
			if out, err := exec.Command("docker", "rm", container).CombinedOutput(); err != nil {
				log.Printf("error running docker rm %s: %v, %s", container, err, out)
				continue
			}
			log.Printf("Removed container %s (%s)", container, name)
		}
		running[name] = strings.HasPrefix(status, "Up")
	}

	for num := 1; num <= *numInst; num++ {
		name := fmt.Sprintf("%s%02d", *basename, num)
		if running[name] {
			continue
		}
		log.Printf("Creating %s ...", name)
		keyFile := fmt.Sprintf("/tmp/buildkey%02d/gobuildkey", num)
		if err := os.MkdirAll(filepath.Dir(keyFile), 0700); err != nil {
			return err
		}
		if err := ioutil.WriteFile(keyFile, buildKey, 0600); err != nil {
			return err
		}
		out, err := exec.Command("docker", "run",
			"-d",
			"--memory="+*memory,
			"--name="+name,
			"-v", filepath.Dir(keyFile)+":/buildkey/",
			"-e", "HOSTNAME="+name,
			"--tmpfs=/workdir:rw,exec",
			*image).CombinedOutput()
		if err != nil {
			log.Printf("Error creating %s: %v, %s", name, err, out)
			continue
		}
		log.Printf("Created %v", name)
	}
	return nil
}
