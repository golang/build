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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
)

var (
	image      = flag.String("image", "golang/builder", "docker image to run; required.")
	numInst    = flag.Int("n", 1, "number of containers to keep running at once")
	basename   = flag.String("basename", "builder", "prefix before the builder number to use for the container names and host names")
	memory     = flag.String("memory", "3g", "memory limit flag for docker run")
	keyFile    = flag.String("key", "/etc/gobuild.key", "go build key file")
	builderEnv = flag.String("env", "", "optional GO_BUILDER_ENV environment variable value to set in the guests")
	cpu        = flag.Int("cpu", 0, "if non-zero, how many CPUs to assign from the host and pass to docker run --cpuset-cpus")
	pull       = flag.Bool("pull", false, "whether to pull the the --image before each container starting")
)

var (
	buildKey     []byte
	scalewayMeta = new(scalewayMetadata)
	isReverse    = true
	isSingleRun  = false
	// ec2UD contains a copy of the EC2 vm user data retrieved from the metadata.
	ec2UD *buildlet.EC2UserData
	// ec2MetaClient is an EC2 metadata client.
	ec2MetaClient *ec2metadata.EC2Metadata
)

func main() {
	flag.Parse()

	if onScaleway() {
		*memory = ""
		*image = "eu.gcr.io/symbolic-datum-552/scaleway-builder"
		*pull = true
		*numInst = 1
		*basename = "scaleway"
		initScalewayMeta()
	} else if onEC2() {
		initEC2Meta()
		*memory = ""
		*image = ec2UD.BuildletImageURL
		*pull = true
		*numInst = 1
		isReverse = false
		isSingleRun = true
	}

	if isReverse {
		buildKey = getBuildKey()
	}

	if *image == "" {
		log.Fatalf("docker --image is required")
	}

	log.Printf("Started. Will keep %d copies of %s running.", *numInst, *image)
	for {
		if err := checkFix(); err != nil {
			log.Print(err)
		}
		if isSingleRun {
			log.Printf("Configured to run a single instance. Exiting")
			os.Exit(0)
		}
		time.Sleep(time.Second) // TODO: docker wait on the running containers?
	}
}

func ec2MdClient() *ec2metadata.EC2Metadata {
	if ec2MetaClient != nil {
		return ec2MetaClient
	}
	ses, err := session.NewSession()
	if err != nil {
		return nil
	}
	ec2MetaClient = ec2metadata.New(ses)
	return ec2MetaClient
}

func onEC2() bool {
	if ec2MdClient() == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return ec2MdClient().AvailableWithContext(ctx)
}

func onScaleway() bool {
	if *builderEnv == "host-linux-arm-scaleway" {
		return true
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm" {
		if _, err := os.Stat("/usr/local/bin/oc-metadata"); err == nil {
			return true
		}
	}
	return false
}

func getBuildKey() []byte {
	key, err := ioutil.ReadFile(*keyFile)
	if err != nil {
		if onScaleway() {
			const prefix = "buildkey_host-linux-arm-scaleway_"
			for _, tag := range scalewayMeta.Tags {
				if strings.HasPrefix(tag, prefix) {
					return []byte(strings.TrimPrefix(tag, prefix))
				}
			}
		}
		log.Fatalf("error reading build key from --key=%s: %v", *keyFile, err)
	}
	return bytes.TrimSpace(key)
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
		prefix := *basename
		if scalewayMeta != nil {
			// scaleway containers are named after their instance.
			prefix = scalewayMeta.Hostname
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if strings.HasPrefix(status, "Exited") {
			removeContainer(container)
		}
		running[name] = strings.HasPrefix(status, "Up")
	}

	for num := 1; num <= *numInst; num++ {
		var name string
		if scalewayMeta != nil && scalewayMeta.Hostname != "" {
			// The -name passed to 'docker run' should match the
			// c1 instance hostname for debugability.
			// There should only be one running container per c1 instance.
			name = scalewayMeta.Hostname
		} else if onEC2() {
			name = ec2UD.BuildletName
		} else {
			name = fmt.Sprintf("%s%02d", *basename, num)
		}
		if running[name] {
			continue
		}

		// Just in case we have a container that exists but is not "running"
		// check if it exists and remove it before creating a new one.
		out, err = exec.Command("docker", "ps", "-a", "--filter", "name="+name, "--format", "{{.CreatedAt}}").Output()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			// The format for the output is the create time and date:
			// 2017-07-24 17:07:39 +0000 UTC
			// To avoid a race with a container that is "Created" but not yet running
			// check how long ago the container was created.
			// If it's longer than minute, remove it.
			created, err := time.Parse("2006-01-02 15:04:05 -0700 MST", strings.TrimSpace(string(out)))
			if err != nil {
				log.Printf("converting output %q for container %s to time failed: %v", out, name, err)
				continue
			}
			dur := time.Since(created)
			if dur.Minutes() > 0 {
				removeContainer(name)
			}

			log.Printf("Container %s is already being created, duration %s", name, dur.String())
			continue
		}

		if *pull {
			log.Printf("Pulling %s ...", *image)
			out, err := exec.Command("docker", "pull", *image).CombinedOutput()
			if err != nil {
				log.Printf("docker pull %s failed: %v, %s", *image, err, out)
			}
		}

		log.Printf("Creating %s ...", name)
		keyFile := fmt.Sprintf("/tmp/buildkey%02d/gobuildkey", num)
		if err := os.MkdirAll(filepath.Dir(keyFile), 0700); err != nil {
			return err
		}
		if err := ioutil.WriteFile(keyFile, buildKey, 0600); err != nil {
			return err
		}
		cmd := exec.Command("docker", "run",
			"-d",
			"--name="+name,
			"-e", "HOSTNAME="+name,
			"--security-opt=seccomp=unconfined", // Issue 35547
			"--tmpfs=/workdir:rw,exec")
		if *memory != "" {
			cmd.Args = append(cmd.Args, "--memory="+*memory)
		}
		if isReverse {
			cmd.Args = append(cmd.Args, "-v", filepath.Dir(keyFile)+":/buildkey/")
		} else {
			cmd.Args = append(cmd.Args, "-p", "443:443")
		}
		if *cpu > 0 {
			cmd.Args = append(cmd.Args, fmt.Sprintf("--cpuset-cpus=%d-%d", *cpu*(num-1), *cpu*num-1))
		}
		if *builderEnv != "" {
			cmd.Args = append(cmd.Args, "-e", "GO_BUILDER_ENV="+*builderEnv)
		}
		if u := buildletBinaryURL(); u != "" {
			cmd.Args = append(cmd.Args, "-e", "META_BUILDLET_BINARY_URL="+u)
		}
		cmd.Args = append(cmd.Args,
			"-e", "GO_BUILD_KEY_PATH=/buildkey/gobuildkey",
			"-e", "GO_BUILD_KEY_DELETE_AFTER_READ=true",
		)
		cmd.Args = append(cmd.Args, *image)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("Error creating %s: %v, %s", name, err, out)
			continue
		}
		log.Printf("Created %v", name)
	}
	return nil
}

type scalewayMetadata struct {
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	Tags     []string `json:"tags"`
}

func (m *scalewayMetadata) HasTag(t string) bool {
	if m == nil {
		return false
	}
	for _, v := range m.Tags {
		if v == t {
			return true
		}
	}
	return false
}

func initEC2Meta() {
	if !onEC2() {
		log.Fatal("attempt to initialize metadata on non-EC2 instance")
	}
	if ec2UD != nil {
		return
	}
	if ec2MdClient() == nil {
		log.Fatalf("unable to retrieve EC2 metadata client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ec2MetaJson, err := ec2MdClient().GetUserDataWithContext(ctx)
	if err != nil {
		log.Fatalf("unable to retrieve EC2 user data: %v", err)
	}
	ec2UD = &buildlet.EC2UserData{}
	err = json.Unmarshal([]byte(ec2MetaJson), ec2UD)
	if err != nil {
		log.Fatalf("unable to unmarshal user data json: %v", err)
	}
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
	if err := json.NewDecoder(res.Body).Decode(scalewayMeta); err != nil {
		log.Fatalf("invalid JSON from scaleway metadata URL %s: %v", metaURL, err)
	}
}

func removeContainer(container string) {
	if out, err := exec.Command("docker", "rm", "-f", container).CombinedOutput(); err != nil {
		log.Printf("error running docker rm -f %s: %v, %s", container, err, out)
		return
	}
	log.Printf("Removed container %s", container)
}

func buildletBinaryURL() string {
	if !onScaleway() {
		// Only used for Scaleway currently.
		return ""
	}
	env := buildenv.Production
	if scalewayMeta.HasTag("staging") {
		env = buildenv.Staging
	}
	return fmt.Sprintf("https://storage.googleapis.com/%s/buildlet.linux-arm", env.BuildletBucket)
}
