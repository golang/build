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
//
// Currently kubectl is the only supported subcommand.

package main // import "golang.org/x/build/cmd/xb"

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/build/buildenv"
)

var (
	prod    = flag.Bool("prod", false, "use production")
	staging = flag.Bool("staging", false, "use staging")
)

func usage() {
	fmt.Fprintf(os.Stderr, `xb {prod,staging} <CMD> [<ARGS>...]
Example:
   xb staging kubectl ...
   xb prod gcloud ...
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
		curCtx := cmdStrOutput("kubectl", "config", "current-context")
		wantCtx := fmt.Sprintf("gke_%s_%s_go", env.ProjectName, env.Zone)
		if curCtx != wantCtx {
			log.SetFlags(0)
			log.Fatalf("Wrong kubectl context; currently using %q; want %q\nRun:\n  gcloud container clusters get-credentials --project=%s --zone=%s go",
				curCtx, wantCtx,
				env.ProjectName, env.Zone,
			)
		}
		// gcloud container clusters get-credentials --zone=us-central1-f go
		// gcloud container clusters get-credentials --zone=us-central1-f buildlets
		runCmd()
	default:
		log.Fatalf("unknown command %q", cmd)
	}
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

func cmdStrOutput(cmd string, args ...string) string {
	out, err := exec.Command(cmd, args...).Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		log.Fatalf("error running %s %v: %v, %s", cmd, args, err, stderr)
	}
	ret := strings.TrimSpace(string(out))
	if ret == "" {
		log.Fatalf("expected output from %s %v; got nothing", cmd, args)
	}
	return ret
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
