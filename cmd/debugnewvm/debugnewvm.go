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
	"strings"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
)

var (
	hostType     = flag.String("host", "host-windows-amd64-2012", "host type to create")
	serial       = flag.Bool("serial", true, "watch serial")
	pauseAfterUp = flag.Bool("pause-after-up", false, "pause for a few seconds (enough to SIGSTOP) after WorkDir returns")
)

var (
	computeSvc *compute.Service
	env        *buildenv.Environment
)

func main() {
	buildenv.RegisterFlags()
	flag.Parse()
	ts, err := google.DefaultTokenSource(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	env = buildenv.FromFlags()
	computeSvc, _ = compute.New(oauth2.NewClient(context.TODO(), ts))

	name := fmt.Sprintf("buildlet-debug-%d", time.Now().Unix())
	log.Printf("Creating %s", name)
	c, err := buildlet.StartNewVM(ts, name, *hostType, buildlet.VMOpts{
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
	dir, err := c.WorkDir()
	log.Printf("WorkDir: %v, %v", dir, err)
	if *pauseAfterUp {
		log.Printf("Shutting down in 5 seconds...")
		time.Sleep(5 * time.Second)
	}
	if err := c.Close(); err != nil {
		log.Fatalf("Close: %v", err)
	}
	log.Printf("done.")
	time.Sleep(5 * time.Second) // wait for serial logging to catch up
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
