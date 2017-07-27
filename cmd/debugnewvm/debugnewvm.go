// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The debugnewvm command creates and destroys a VM-based GCE buildlet
// with lots of logging for debugging. Nothing depends on this.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
)

var (
	hostType     = flag.String("host", "", "host type to create")
	vmImage      = flag.String("override-image", "", "if non-empty, an alternate GCE VM image to use, overriding the one defined for the given host")
	serial       = flag.Bool("serial", true, "watch serial")
	pauseAfterUp = flag.Bool("pause-after-up", false, "pause for a few seconds (enough to SIGSTOP) after WorkDir returns")

	runBuild = flag.String("run-build", "", "optional builder name to run all.bash for")
	buildRev = flag.String("rev", "master", "if --run-build is specified, the git hash or branch name to build")
)

var (
	computeSvc *compute.Service
	env        *buildenv.Environment
)

func main() {
	buildenv.RegisterFlags()
	flag.Parse()

	var bconf dashboard.BuildConfig
	if *runBuild != "" {
		var ok bool
		bconf, ok = dashboard.Builders[*runBuild]
		if !ok {
			log.Fatalf("unknown builder %q", *runBuild)
		}
		if *hostType == "" {
			*hostType = bconf.HostType
		}
	}

	if *hostType == "" {
		log.Fatalf("missing --host (or --run-build)")
	}

	hconf, ok := dashboard.Hosts[*hostType]
	if !ok {
		log.Fatalf("unknown host type %q", *hostType)
	}
	if !hconf.IsGCE() {
		log.Fatalf("host type %q is not a GCE host type", *hostType)
	}
	if *vmImage != "" {
		hconf.VMImage = *vmImage
	}

	ts, err := google.DefaultTokenSource(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	env = buildenv.FromFlags()
	computeSvc, _ = compute.New(oauth2.NewClient(context.TODO(), ts))

	name := fmt.Sprintf("debug-temp-%d", time.Now().Unix())
	log.Printf("Creating %s (with VM image %q)", name, hconf.VMImage)
	bc, err := buildlet.StartNewVM(ts, name, *hostType, buildlet.VMOpts{
		Zone:                env.Zone,
		ProjectID:           env.ProjectName,
		DeleteIn:            15 * time.Minute,
		OnInstanceRequested: func() { log.Printf("instance requested") },
		OnInstanceCreated: func() {
			log.Printf("instance created")
			if *serial {
				go watchSerial(name)
			}
		},
		OnGotInstanceInfo: func() { log.Printf("got instance info") },
		OnBeginBuildletProbe: func(buildletURL string) {
			log.Printf("About to hit %s to see if buildlet is up yet...", buildletURL)
		},
		OnEndBuildletProbe: func(res *http.Response, err error) {
			if err != nil {
				log.Printf("client buildlet probe error: %v", err)
				return
			}
			log.Printf("buildlet probe: %s", res.Status)
		},
	})
	if err != nil {
		log.Fatalf("StartNewVM: %v", err)
	}
	dir, err := bc.WorkDir()
	log.Printf("WorkDir: %v, %v", dir, err)

	var buildFailed bool
	if *runBuild != "" {
		// Push GOROOT_BOOTSTRAP, if needed.
		if u := bconf.GoBootstrapURL(env); u != "" {
			log.Printf("Pushing 'go1.4' Go bootstrap dir ...")
			const bootstrapDir = "go1.4" // might be newer; name is the default
			if err := bc.PutTarFromURL(u, bootstrapDir); err != nil {
				bc.Close()
				log.Fatalf("Putting Go bootstrap: %v", err)
			}
		}

		// Push Go code
		log.Printf("Pushing 'go' dir...")
		goTarGz := "https://go.googlesource.com/go/+archive/" + *buildRev + ".tar.gz"
		if err := bc.PutTarFromURL(goTarGz, "go"); err != nil {
			bc.Close()
			log.Fatalf("Putting go code: %v", err)
		}

		// Push a synthetic VERSION file to prevent git usage:
		if err := bc.PutTar(buildgo.VersionTgz(*buildRev), "go"); err != nil {
			bc.Close()
			log.Fatalf("Putting VERSION file: %v", err)
		}

		allScript := bconf.AllScript()
		log.Printf("Running %s ...", allScript)
		remoteErr, err := bc.Exec(path.Join("go", allScript), buildlet.ExecOpts{
			Output:   os.Stdout,
			ExtraEnv: bconf.Env(),
			Debug:    true,
			Args:     bconf.AllScriptArgs(),
		})
		if err != nil {
			log.Fatalf("error trying to run %s: %v", allScript, err)
		}
		if remoteErr != nil {
			log.Printf("remote failure running %s: %v", allScript, remoteErr)
			buildFailed = true
		}
	}

	if *pauseAfterUp {
		log.Printf("Shutting down in 5 seconds...")
		time.Sleep(5 * time.Second)
	}
	if err := bc.Close(); err != nil {
		log.Fatalf("Close: %v", err)
	}
	log.Printf("done.")
	time.Sleep(2 * time.Second) // wait for serial logging to catch up

	if buildFailed {
		os.Exit(1)
	}
}

// watchSerial streams the named VM's serial port to log.Printf. It's roughly:
//   gcloud compute connect-to-serial-port --zone=xxx $NAME
// but in Go and works. For some reason, gcloud doesn't work as a
// child process and has weird errors.
func watchSerial(name string) {
	start := int64(0)
	indent := strings.Repeat(" ", len("2017/07/25 06:37:14 SERIAL: "))
	for {
		sout, err := computeSvc.Instances.GetSerialPortOutput(env.ProjectName, env.Zone, name).Start(start).Do()
		if err != nil {
			log.Printf("serial output error: %v", err)
			return
		}
		moved := sout.Next != start
		start = sout.Next
		contents := strings.Replace(strings.TrimSpace(sout.Contents), "\r\n", "\r\n"+indent, -1)
		if contents != "" {
			log.Printf("SERIAL: %s", contents)
		}
		if !moved {
			time.Sleep(1 * time.Second)
		}
	}
}
