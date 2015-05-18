// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Code interacting with Google Compute Engine (GCE) and
// a GCE implementation of the BuildletPool interface.

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/cloud"
	"google.golang.org/cloud/compute/metadata"
	"google.golang.org/cloud/storage"
)

func init() {
	buildlet.GCEGate = gceAPIGate
}

// apiCallTicker ticks regularly, preventing us from accidentally making
// GCE API calls too quickly. Our quota is 20 QPS, but we temporarily
// limit ourselves to less than that.
var apiCallTicker = time.NewTicker(time.Second / 5)

func gceAPIGate() {
	<-apiCallTicker.C
}

// Initialized by initGCE:
var (
	projectID      string
	projectZone    string
	computeService *compute.Service
	externalIP     string
	tokenSource    oauth2.TokenSource
	serviceCtx     context.Context
	errTryDeps     error // non-nil if try bots are disabled
	gerritClient   *gerrit.Client
	devCluster     bool // are we running in the dev cluster?
)

func initGCE() error {
	if !metadata.OnGCE() {
		return errors.New("not running on GCE; VM support disabled")
	}
	var err error
	projectID, err = metadata.ProjectID()
	if err != nil {
		return fmt.Errorf("failed to get current GCE ProjectID: %v", err)
	}
	tokenSource = google.ComputeTokenSource("default")
	httpClient := oauth2.NewClient(oauth2.NoContext, tokenSource)
	serviceCtx = cloud.NewContext(projectID, httpClient)

	projectZone, err = metadata.Get("instance/zone")
	if err != nil || projectZone == "" {
		return fmt.Errorf("failed to get current GCE zone: %v", err)
	}
	// Convert the zone from "projects/1234/zones/us-central1-a" to "us-central1-a".
	projectZone = path.Base(projectZone)
	if !hasComputeScope() {
		return errors.New("The coordinator is not running with access to read and write Compute resources. VM support disabled.")

	}
	externalIP, err = metadata.ExternalIP()
	if err != nil {
		return fmt.Errorf("ExternalIP: %v", err)
	}
	computeService, _ = compute.New(httpClient)
	errTryDeps = checkTryBuildDeps()
	if errTryDeps != nil {
		log.Printf("TryBot builders disabled due to error: %v", errTryDeps)
	} else {
		log.Printf("TryBot builders enabled.")
	}

	devCluster = projectID == "go-dashboard-dev"
	if devCluster {
		log.Printf("Running in dev cluster")
		gcePool.vmCap = make(chan bool, 1)
	}

	return nil
}

func checkTryBuildDeps() error {
	if !hasStorageScope() {
		return errors.New("coordinator's GCE instance lacks the storage service scope")
	}
	wr := storage.NewWriter(serviceCtx, *buildLogBucket, "hello.txt")
	fmt.Fprintf(wr, "Hello, world! Coordinator start-up at %v", time.Now())
	if err := wr.Close(); err != nil {
		return fmt.Errorf("test write of a GCS object failed: %v", err)
	}
	gobotPass, err := metadata.ProjectAttributeValue("gobot-password")
	if err != nil {
		return fmt.Errorf("failed to get project metadata 'gobot-password': %v", err)
	}
	gerritClient = gerrit.NewClient("https://go-review.googlesource.com",
		gerrit.BasicAuth("git-gobot.golang.org", strings.TrimSpace(string(gobotPass))))

	return nil
}

// We artifically limit ourselves to 60 VMs right now, assuming that
// each takes 2 CPU, and we have a current quota of 200 CPUs. That
// gives us headroom, but also doesn't account for SSD or memory
// quota.
// TODO(bradfitz): better quota system.
const maxVMs = 60

var gcePool = &gceBuildletPool{
	vmCap: make(chan bool, maxVMs),
}
var _ BuildletPool = (*gceBuildletPool)(nil)

type gceBuildletPool struct {
	// vmCap is a semaphore used to limit the number of VMs in
	// use.
	vmCap chan bool

	mu       sync.Mutex
	instUsed map[string]bool // GCE VM instance name -> true
}

func (p *gceBuildletPool) SetEnabled(enabled bool) {
	if enabled {
		p.vmCap = make(chan bool, maxVMs)
	} else {
		p.vmCap = make(chan bool)
	}
}

func (p *gceBuildletPool) GetBuildlet(typ, rev string, el eventTimeLogger) (*buildlet.Client, error) {
	el.logEventTime("awaiting_gce_quota")
	p.awaitVMCountQuota()

	// name is the project-wide unique name of the GCE instance. It can't be longer
	// than 61 bytes.
	instName := "buildlet-" + typ + "-" + rev[:8] + "-rn" + randHex(6)
	p.setInstanceUsed(instName, true)

	var needDelete bool

	el.logEventTime("instance_name=" + instName)
	el.logEventTime("creating_instance")
	log.Printf("Creating GCE VM %q for %s at %s", instName, typ, rev)
	bc, err := buildlet.StartNewVM(tokenSource, instName, typ, buildlet.VMOpts{
		ProjectID:   projectID,
		Zone:        projectZone,
		Description: fmt.Sprintf("Go Builder for %s at %s", typ, rev),
		DeleteIn:    vmDeleteTimeout,
		OnInstanceRequested: func() {
			needDelete = true
			el.logEventTime("instance_create_requested")
			log.Printf("GCE VM %q now booting", instName)
		},
		OnInstanceCreated: func() {
			el.logEventTime("instance_created")
			needDelete = true // redundant with OnInstanceRequested one, but fine.
		},
		OnGotInstanceInfo: func() {
			el.logEventTime("waiting_for_buildlet")
		},
	})
	if err != nil {
		log.Printf("Failed to create VM for %s, %s: %v", typ, rev, err)
		if needDelete {
			deleteVM(projectZone, instName)
		}
		p.setInstanceUsed(instName, false)
		p.putVMCountQuota()
		return nil, err
	}
	bc.SetDescription("GCE VM: " + instName)
	bc.SetCloseFunc(func() error {
		deleteVM(projectZone, instName)
		p.setInstanceUsed(instName, false)
		p.putVMCountQuota()
		return nil
	})
	return bc, nil
}

func (p *gceBuildletPool) WriteHTMLStatus(w io.Writer) {
	fmt.Fprintf(w, "<b>GCE pool</b> capacity: %d/%d", len(p.vmCap), cap(p.vmCap))
}

func (p *gceBuildletPool) String() string {
	return fmt.Sprintf("GCE pool capacity: %d/%d", len(p.vmCap), cap(p.vmCap))
}

func (p *gceBuildletPool) awaitVMCountQuota() { p.vmCap <- true }
func (p *gceBuildletPool) putVMCountQuota()   { <-p.vmCap }

func (p *gceBuildletPool) setInstanceUsed(instName string, used bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.instUsed == nil {
		p.instUsed = make(map[string]bool)
	}
	if used {
		p.instUsed[instName] = true
	} else {
		delete(p.instUsed, instName)
	}
}

func (p *gceBuildletPool) instanceUsed(instName string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instUsed[instName]
}

// cleanUpOldVMs loops forever and periodically enumerates virtual
// machines and deletes those which have expired.
//
// A VM is considered expired if it has a "delete-at" metadata
// attribute having a unix timestamp before the current time.
//
// This is the safety mechanism to delete VMs which stray from the
// normal deleting process. VMs are created to run a single build and
// should be shut down by a controlling process. Due to various types
// of failures, they might get stranded. To prevent them from getting
// stranded and wasting resources forever, we instead set the
// "delete-at" metadata attribute on them when created to some time
// that's well beyond their expected lifetime.
func (p *gceBuildletPool) cleanUpOldVMs() {
	if *mode == "dev" {
		return
	}
	if computeService == nil {
		return
	}
	for {
		for _, zone := range strings.Split(*cleanZones, ",") {
			zone = strings.TrimSpace(zone)
			if err := p.cleanZoneVMs(zone); err != nil {
				log.Printf("Error cleaning VMs in zone %q: %v", zone, err)
			}
		}
		time.Sleep(time.Minute)
	}
}

// cleanZoneVMs is part of cleanUpOldVMs, operating on a single zone.
func (p *gceBuildletPool) cleanZoneVMs(zone string) error {
	// Fetch the first 500 (default) running instances and clean
	// thoes. We expect that we'll be running many fewer than
	// that. Even if we have more, eventually the first 500 will
	// either end or be cleaned, and then the next call will get a
	// partially-different 500.
	// TODO(bradfitz): revist this code if we ever start running
	// thousands of VMs.
	gceAPIGate()
	list, err := computeService.Instances.List(projectID, zone).Do()
	if err != nil {
		return fmt.Errorf("listing instances: %v", err)
	}
	for _, inst := range list.Items {
		if inst.Metadata == nil {
			// Defensive. Not seen in practice.
			continue
		}
		sawDeleteAt := false
		for _, it := range inst.Metadata.Items {
			if it.Key == "delete-at" {
				sawDeleteAt = true
				unixDeadline, err := strconv.ParseInt(it.Value, 10, 64)
				if err != nil {
					log.Printf("invalid delete-at value %q seen; ignoring", it.Value)
				}
				if err == nil && time.Now().Unix() > unixDeadline {
					log.Printf("Deleting expired VM %q in zone %q ...", inst.Name, zone)
					deleteVM(zone, inst.Name)
				}
			}
		}
		// Delete buildlets (things we made) from previous
		// generations.  Thenaming restriction (buildlet-*)
		// prevents us from deleting buildlet VMs used by
		// Gophers for interactive development & debugging
		// (non-builder users); those are named "mote-*".
		if sawDeleteAt && strings.HasPrefix(inst.Name, "buildlet-") && !p.instanceUsed(inst.Name) {
			log.Printf("Deleting VM %q in zone %q from an earlier coordinator generation ...", inst.Name, zone)
			deleteVM(zone, inst.Name)
		}
	}
	return nil
}

// deleteVM starts a delete of an instance in a given zone.
//
// It either returns an operation name (if delete is pending) or the
// empty string if the instance didn't exist.
func deleteVM(zone, instName string) (operation string, err error) {
	gceAPIGate()
	op, err := computeService.Instances.Delete(projectID, zone, instName).Do()
	apiErr, ok := err.(*googleapi.Error)
	if ok {
		if apiErr.Code == 404 {
			return "", nil
		}
	}
	if err != nil {
		log.Printf("Failed to delete instance %q in zone %q: %v", instName, zone, err)
		return "", err
	}
	log.Printf("Sent request to delete instance %q in zone %q. Operation ID, Name: %v, %v", instName, zone, op.Id, op.Name)
	return op.Name, nil
}

func hasScope(want string) bool {
	if !metadata.OnGCE() {
		return false
	}
	scopes, err := metadata.Scopes("default")
	if err != nil {
		log.Printf("failed to query metadata default scopes: %v", err)
		return false
	}
	for _, v := range scopes {
		if v == want {
			return true
		}
	}
	return false
}

func hasComputeScope() bool {
	return hasScope(compute.ComputeScope)
}

func hasStorageScope() bool {
	return hasScope(storage.ScopeReadWrite) || hasScope(storage.ScopeFullControl)
}

func randHex(n int) string {
	buf := make([]byte, n/2)
	_, err := rand.Read(buf)
	if err != nil {
		panic("Failed to get randomness: " + err.Error())
	}
	return fmt.Sprintf("%x", buf)
}
