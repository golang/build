// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The xb command wraps GCP deployment commands such as gcloud,
// kubectl, and docker push and verifies they're interacting with the
// intended prod-vs-staging environment.
//
// Usage:
//
//    xb {--prod,--staging} <CMD> [<ARGS>...]
//
// Examples:
//
//    xb --staging kubectl ...
//    xb --prod kubectl ...
//    xb google-email  # print the @google.com account from gcloud
//
package main // import "golang.org/x/build/cmd/xb"

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"golang.org/x/build/buildenv"
)

var (
	prod    = flag.Bool("prod", false, "use production")
	staging = flag.Bool("staging", false, "use staging")
)

func usage() {
	fmt.Fprintf(os.Stderr, `xb [--prod or --staging] <CMD> [<ARGS>...]
Example:
   xb --staging kubectl ...
   xb google-email
`)
	os.Exit(1)
}

func main() {
	flag.Parse()
	if flag.NArg() < 1 {
		usage()
	}

	cmd := flag.Arg(0)
	switch cmd {
	case "kubectl":
		env := getEnv()
		curCtx := kubeCurrentContext()
		wantCtx := fmt.Sprintf("gke_%s_%s_go", env.ProjectName, env.ControlZone)
		if curCtx != wantCtx {
			log.SetFlags(0)
			log.Fatalf("Wrong kubectl context; currently using %q; want %q\nRun:\n  gcloud container clusters get-credentials --project=%s --zone=%s go",
				curCtx, wantCtx,
				env.ProjectName, env.ControlZone,
			)
		}
		// gcloud container clusters get-credentials --zone=us-central1-f go
		// gcloud container clusters get-credentials --zone=us-central1-f buildlets
		runCmd()
	case "docker":
		runDocker()
	case "google-email":
		out, err := exec.Command("gcloud", "config", "configurations", "list").CombinedOutput()
		if err != nil {
			log.Fatalf("gcloud: %v, %s", err, out)
		}
		googRx := regexp.MustCompile(`\S+@google\.com\b`)
		e := googRx.FindString(string(out))
		if e == "" {
			log.Fatalf("didn't find @google.com address in gcloud config configurations list: %s", out)
		}
		fmt.Println(e)
	default:
		log.Fatalf("unknown command %q", cmd)
	}
}

func kubeCurrentContext() string {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		log.SetFlags(0)
		log.Fatalf("No kubectl in path.")
	}
	// Get current context, but ignore errors, as kubectl returns an error
	// if there's no context.
	out, err := exec.Command(kubectl, "config", "current-context").Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		if strings.Contains(stderr, "current-context is not set") {
			return ""
		}
		log.Printf("Failed to run 'kubectl config current-context': %v, %s", err, stderr)
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getEnv() *buildenv.Environment {
	if *prod == *staging {
		log.Fatalf("must specify exactly one of --prod or --staging")
	}
	if *prod {
		return buildenv.Production
	}
	return buildenv.Staging
}

func runDocker() {
	if flag.Arg(1) == "build" {
		file := "Dockerfile"
		for i, v := range flag.Args() {
			if v == "-f" {
				file = flag.Arg(i + 1)
			}
		}
		layers := fromLayers(file)
		for _, layer := range layers {
			if strings.HasPrefix(layer, "golang:") ||
				strings.HasPrefix(layer, "debian:") ||
				strings.HasPrefix(layer, "alpine:") ||
				strings.HasPrefix(layer, "fedora:") {
				continue
			}
			switch layer {
			case "golang/buildlet-stage0":
				log.Printf("building dependent layer %q", layer)
				buildStage0Container()
			default:
				log.Fatalf("unsupported layer %q; don't know how to validate or build", layer)
			}
		}
	}

	for i, v := range flag.Args() {
		// Replace any occurence of REPO with gcr.io/sybolic-datum-552 or
		// the staging equivalent. Note that getEnv() is only called if
		// REPO is already present, so the --prod and --staging flags
		// aren't required to run "xb docker ..." in general.
		if strings.Contains(v, "REPO") {
			flag.Args()[i] = strings.Replace(v, "REPO", "gcr.io/"+getEnv().ProjectName, -1)
		}
	}

	runCmd()
}

// fromLayers returns the layers named in the provided Dockerfile
// file's FROM statements.
func fromLayers(file string) (layers []string) {
	f, err := os.Open(file)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	bs := bufio.NewScanner(f)
	for bs.Scan() {
		line := strings.TrimSpace(bs.Text())
		if !strings.HasPrefix(line, "FROM") {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "FROM" {
			layers = append(layers, f[1])
		}
	}
	if err := bs.Err(); err != nil {
		log.Fatal(err)
	}
	return
}

func runCmd() {
	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		// TODO: return with exact exit status? when needed.
		log.Fatal(err)
	}
}

func buildStage0Container() {
	dir, err := exec.Command("go", "list", "-f", "{{.Dir}}", "golang.org/x/build/cmd/buildlet/stage0").Output()
	if err != nil {
		log.Fatalf("xb: error running go list to find golang.org/x/build/stage0: %v", err)
	}

	cmd := exec.Command("make", "docker")
	cmd.Dir = strings.TrimSpace(string(dir))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
}
