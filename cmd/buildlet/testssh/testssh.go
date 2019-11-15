// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The testssh binary exists to verify that a buildlet container's
// ssh works, without running the whole coordinator binary in the
// staging environment.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
)

var (
	container  = flag.String("container", "", "if non-empty, the ID of a running docker container")
	startImage = flag.String("start-image", "", "if non-empty, the Docker image to start a buildlet of locally, and use its container ID for the -container value")
	user       = flag.String("user", "root", "SSH user")
)

func main() {
	flag.Parse()
	ipPort := getIPPort()
	defer cleanContainer()

	bc := buildlet.NewClient(ipPort, buildlet.NoKeyPair)
	for {
		c, err := net.Dial("tcp", ipPort)
		if err == nil {
			c.Close()
			break
		}
		log.Printf("waiting for %v to come up...", ipPort)
		time.Sleep(time.Second)
	}

	pubKey, privPath := genKey()

	log.Printf("hitting buildlet's /connect-ssh ...")
	buildletConn, err := bc.ConnectSSH(*user, pubKey)
	if err != nil {
		var out []byte
		if *container != "" {
			var err error
			out, err = exec.Command("docker", "logs", *container).CombinedOutput()
			if err != nil {
				log.Printf("failed to fetch docker logs: %v", err)
			}
		}
		cleanContainer()
		log.Printf("image logs: %s", out)
		log.Fatalf("ConnectSSH: %v (logs above)", err)
	}
	defer buildletConn.Close()
	log.Printf("ConnectSSH succeeded; testing connection...")

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go io.Copy(buildletConn, c)
		go io.Copy(c, buildletConn)
	}()
	ip, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		log.Fatal(err)
	}

	cmd := exec.Command("ssh",
		"-v",
		"-i", privPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-p", port,
		*user+"@"+ip,
		"echo", "SSH works")
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		cleanContainer()
		log.Fatalf("ssh client: %v, %s", err, stderr)
	}
	fmt.Print(stdout.String())
}

func cleanContainer() {
	if *startImage == "" {
		return
	}
	out, err := exec.Command("docker", "rm", "-f", *container).CombinedOutput()
	if err != nil {
		log.Printf("docker rm: %v, %s", err, out)
	}
}

func genKey() (pubKey, privateKeyPath string) {
	cache, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	cache = filepath.Join(cache, "testssh")
	os.MkdirAll(cache, 0755)
	privateKeyPath = filepath.Join(cache, "testkey")
	pubKeyPath := filepath.Join(cache, "testkey.pub")
	if _, err := os.Stat(pubKeyPath); err != nil {
		out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", privateKeyPath, "-N", "").CombinedOutput()
		if err != nil {
			log.Fatalf("ssh-keygen: %v, %s", err, out)
		}
	}
	slurp, err := ioutil.ReadFile(pubKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	return strings.TrimSpace(string(slurp)), privateKeyPath
}

func getIPPort() string {
	if *startImage != "" {
		buildlet := "buildlet.linux-amd64"
		if strings.Contains(*startImage, "linux-x86-alpine") {
			buildlet = "buildlet.linux-amd64-static"
		}
		log.Printf("creating container with image %s ...", *startImage)
		out, err := exec.Command("docker", "run", "-d",
			"--stop-timeout=300",
			"-e", "META_BUILDLET_BINARY_URL=https://storage.googleapis.com/"+buildenv.Production.BuildletBucket+"/"+buildlet,
			*startImage).CombinedOutput()
		if err != nil {
			log.Fatalf("docker run: %v, %s", err, out)
		}
		*container = strings.TrimSpace(string(out))
		log.Printf("created container %s ...", *container)
	}
	if *container != "" {
		out, err := exec.Command("bash", "-c", "docker inspect "+*container+" | jq -r '.[0].NetworkSettings.IPAddress'").CombinedOutput()
		if err != nil {
			log.Fatalf("%v: %s", err, out)
		}
		return strings.TrimSpace(string(out)) + ":80"
	}
	log.Fatalf("no address specified")
	return ""
}
