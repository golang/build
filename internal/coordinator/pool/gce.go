// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

// Code interacting with Google Compute Engine (GCE) and
// a GCE implementation of the BuildletPool interface.

package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/datastore"
	"cloud.google.com/go/errorreporting"
	monapi "cloud.google.com/go/monitoring/apiv3"
	"cloud.google.com/go/storage"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/buildstats"
	"golang.org/x/build/internal/lru"
	"golang.org/x/build/internal/secret"
	"golang.org/x/build/internal/spanlog"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

func init() {
	buildlet.GCEGate = gceAPIGate
}

// apiCallTicker ticks regularly, preventing us from accidentally making
// GCE API calls too quickly. Our quota is 20 QPS, but we temporarily
// limit ourselves to less than that.
var apiCallTicker = time.NewTicker(time.Second / 10)

func gceAPIGate() {
	<-apiCallTicker.C
}

// IsGCERemoteBuildletFunc should return true if the buildlet instance name is
// is a GCE remote buildlet.
type IsGCERemoteBuildletFunc func(instanceName string) bool

// Initialized by initGCE:
// TODO(http://golang.org/issue/38337): These should be moved into a struct as
// part of the effort to reduce package level variables.
var (
	buildEnv *buildenv.Environment

	// dsClient is a datastore client for the build project (symbolic-datum-552), where build progress is stored.
	dsClient *datastore.Client
	// goDSClient is a datastore client for golang-org, where build status is stored.
	goDSClient     *datastore.Client
	computeService *compute.Service
	gcpCreds       *google.Credentials
	errTryDeps     error // non-nil if try bots are disabled
	gerritClient   *gerrit.Client
	storageClient  *storage.Client
	metricsClient  *monapi.MetricClient
	inStaging      bool                   // are we running in the staging project? (named -dev)
	errorsClient   *errorreporting.Client // Stackdriver errors client
	gkeNodeIP      string

	// values created due to seperating the buildlet pools into a seperate package
	gceMode             string
	deleteTimeout       time.Duration
	testFiles           map[string]string
	basePinErr          *atomic.Value
	isGCERemoteBuildlet IsGCERemoteBuildletFunc

	initGCECalled bool
)

// oAuthHTTPClient is the OAuth2 HTTP client used to make API calls to Google Cloud APIs.
// It is initialized by initGCE.
var oAuthHTTPClient *http.Client

// InitGCE initializes the GCE buildlet pool.
func InitGCE(sc *secret.Client, vmDeleteTimeout time.Duration, tFiles map[string]string, basePin *atomic.Value, fn IsGCERemoteBuildletFunc, buildEnvName, mode string) error {
	initGCECalled = true
	deleteTimeout = vmDeleteTimeout
	testFiles = tFiles
	basePinErr = basePin
	isGCERemoteBuildlet = fn
	var err error
	ctx := context.Background()

	// If the coordinator is running on a GCE instance and a
	// buildEnv was not specified with the env flag, set the
	// buildEnvName to the project ID
	if buildEnvName == "" {
		if mode == "dev" {
			buildEnvName = "dev"
		} else if metadata.OnGCE() {
			buildEnvName, err = metadata.ProjectID()
			if err != nil {
				log.Fatalf("metadata.ProjectID: %v", err)
			}
		}
	}

	buildEnv = buildenv.ByProjectID(buildEnvName)
	inStaging = buildEnv == buildenv.Staging

	// If running on GCE, override the zone and static IP, and check service account permissions.
	if metadata.OnGCE() {
		projectZone, err := metadata.Get("instance/zone")
		if err != nil || projectZone == "" {
			return fmt.Errorf("failed to get current GCE zone: %v", err)
		}

		gkeNodeIP, err = metadata.Get("instance/network-interfaces/0/ip")
		if err != nil {
			return fmt.Errorf("failed to get current instance IP: %v", err)
		}

		// Convert the zone from "projects/1234/zones/us-central1-a" to "us-central1-a".
		projectZone = path.Base(projectZone)
		buildEnv.ControlZone = projectZone

		if buildEnv.StaticIP == "" {
			buildEnv.StaticIP, err = metadata.ExternalIP()
			if err != nil {
				return fmt.Errorf("ExternalIP: %v", err)
			}
		}

		if !hasComputeScope() {
			return errors.New("coordinator is not running with access to read and write Compute resources. VM support disabled")
		}
	}

	cfgDump, _ := json.MarshalIndent(buildEnv, "", "  ")
	log.Printf("Loaded configuration %q for project %q:\n%s", buildEnvName, buildEnv.ProjectName, cfgDump)

	if mode != "dev" {
		storageClient, err = storage.NewClient(ctx)
		if err != nil {
			log.Fatalf("storage.NewClient: %v", err)
		}

		metricsClient, err = monapi.NewMetricClient(ctx)
		if err != nil {
			log.Fatalf("monapi.NewMetricClient: %v", err)
		}
	}

	dsClient, err = datastore.NewClient(ctx, buildEnv.ProjectName)
	if err != nil {
		if mode == "dev" {
			log.Printf("Error creating datastore client for %q: %v", buildEnv.ProjectName, err)
		} else {
			log.Fatalf("Error creating datastore client for %q: %v", buildEnv.ProjectName, err)
		}
	}
	goDSClient, err = datastore.NewClient(ctx, buildEnv.GoProjectName)
	if err != nil {
		if mode == "dev" {
			log.Printf("Error creating datastore client for %q: %v", buildEnv.GoProjectName, err)
		} else {
			log.Fatalf("Error creating datastore client for %q: %v", buildEnv.GoProjectName, err)
		}
	}

	// don't send dev errors to Stackdriver.
	if mode != "dev" {
		errorsClient, err = errorreporting.NewClient(ctx, buildEnv.ProjectName, errorreporting.Config{
			ServiceName: "coordinator",
		})
		if err != nil {
			// don't exit, we still want to run coordinator
			log.Printf("Error creating errors client: %v", err)
		}
	}

	gcpCreds, err = buildEnv.Credentials(ctx)
	if err != nil {
		if mode == "dev" {
			// don't try to do anything else with GCE, as it will likely fail
			return nil
		}
		log.Fatalf("failed to get a token source: %v", err)
	}
	oAuthHTTPClient = oauth2.NewClient(ctx, gcpCreds.TokenSource)
	computeService, _ = compute.New(oAuthHTTPClient)
	errTryDeps = checkTryBuildDeps(ctx, sc)
	if errTryDeps != nil {
		log.Printf("TryBot builders disabled due to error: %v", errTryDeps)
	} else {
		log.Printf("TryBot builders enabled.")
	}

	if mode != "dev" {
		go syncBuildStatsLoop(buildEnv)
	}

	gceMode = mode

	go gcePool.pollQuotaLoop()
	go createBasepinDisks(context.Background())
	return nil
}

// TODO(http://golang.org/issue/38337): These should be moved into a struct as
// part of the effort to reduce package level variables.

// GCEConfiguration manages and contains all of the GCE configuration.
type GCEConfiguration struct{}

// NewGCEConfiguration creates a new GCEConfiguration.
func NewGCEConfiguration() *GCEConfiguration { return &GCEConfiguration{} }

// StorageClient retrieves the GCE storage client.
func (c *GCEConfiguration) StorageClient() *storage.Client {
	return storageClient
}

// BuildEnv retrieves the GCE build env.
func (c *GCEConfiguration) BuildEnv() *buildenv.Environment {
	return buildEnv
}

// SetBuildEnv sets the GCE build env. This is primarily reserved for
// testing purposes.
func (c *GCEConfiguration) SetBuildEnv(b *buildenv.Environment) {
	buildEnv = b
}

// BuildletPool retrieves the GCE buildlet pool.
func (c *GCEConfiguration) BuildletPool() *GCEBuildlet {
	return gcePool
}

// InStaging returns a boolean denoting if the enviornment is stageing.
func (c *GCEConfiguration) InStaging() bool {
	return inStaging
}

// GerritClient retrieves a gerrit client.
func (c *GCEConfiguration) GerritClient() *gerrit.Client {
	return gerritClient
}

// GKENodeIP retrieves the GKE node IP.
func (c *GCEConfiguration) GKENodeIP() string {
	return gkeNodeIP
}

// DSClient retrieves the datastore client.
func (c *GCEConfiguration) DSClient() *datastore.Client {
	return dsClient
}

// GoDSClient retrieves the datastore client for golang.org project.
func (c *GCEConfiguration) GoDSClient() *datastore.Client {
	return goDSClient
}

// TryDepsErr retrives any Trybot dependency error.
func (c *GCEConfiguration) TryDepsErr() error {
	return errTryDeps
}

// ErrorsClient retrieves the stackdriver errors client.
func (c *GCEConfiguration) ErrorsClient() *errorreporting.Client {
	return errorsClient
}

// OAuthHTTPClient retrieves an OAuth2 HTTP client used to make API calls to GCP.
func (c *GCEConfiguration) OAuthHTTPClient() *http.Client {
	return oAuthHTTPClient
}

// GCPCredentials retrieves the GCP credentials.
func (c *GCEConfiguration) GCPCredentials() *google.Credentials {
	return gcpCreds
}

// MetricsClient retrieves a metrics client.
func (c *GCEConfiguration) MetricsClient() *monapi.MetricClient {
	return metricsClient
}

func checkTryBuildDeps(ctx context.Context, sc *secret.Client) error {
	if !hasStorageScope() {
		return errors.New("coordinator's GCE instance lacks the storage service scope")
	}
	if gceMode == "dev" {
		return errors.New("running in dev mode")
	}
	wr := storageClient.Bucket(buildEnv.LogBucket).Object("hello.txt").NewWriter(context.Background())
	fmt.Fprintf(wr, "Hello, world! Coordinator start-up at %v", time.Now())
	if err := wr.Close(); err != nil {
		return fmt.Errorf("test write of a GCS object to bucket %q failed: %v", buildEnv.LogBucket, err)
	}
	if inStaging {
		// Don't expect to write to Gerrit in staging mode.
		gerritClient = gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth)
	} else {
		ctxSec, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		gobotPass, err := sc.Retrieve(ctxSec, secret.NameGobotPassword)
		if err != nil {
			return fmt.Errorf("failed to get project metadata 'gobot-password': %v", err)
		}
		gerritClient = gerrit.NewClient("https://go-review.googlesource.com",
			gerrit.BasicAuth("git-gobot.golang.org", strings.TrimSpace(string(gobotPass))))
	}

	return nil
}

var gcePool = &GCEBuildlet{}

var _ Buildlet = (*GCEBuildlet)(nil)

// maxInstances is a temporary hack because we can't get buildlets to boot
// without IPs, and we only have 200 IP addresses.
// TODO(bradfitz): remove this once fixed.
const maxInstances = 190

// GCEBuildlet manages a pool of GCE buildlets.
type GCEBuildlet struct {
	mu sync.Mutex // guards all following

	disabled bool

	// CPU quota usage & limits.
	cpuLeft   int // dead-reckoning CPUs remain
	instLeft  int // dead-reckoning instances remain
	instUsage int
	cpuUsage  int
	addrUsage int
	inst      map[string]time.Time // GCE VM instance name -> creationTime
}

func (p *GCEBuildlet) pollQuotaLoop() {
	if computeService == nil {
		log.Printf("pollQuotaLoop: no GCE access; not checking quota.")
		return
	}
	if buildEnv.ProjectName == "" {
		log.Printf("pollQuotaLoop: no GCE project name configured; not checking quota.")
		return
	}
	for {
		p.pollQuota()
		time.Sleep(5 * time.Second)
	}
}

func (p *GCEBuildlet) pollQuota() {
	gceAPIGate()
	reg, err := computeService.Regions.Get(buildEnv.ProjectName, buildEnv.Region()).Do()
	if err != nil {
		log.Printf("Failed to get quota for %s/%s: %v", buildEnv.ProjectName, buildEnv.Region(), err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, quota := range reg.Quotas {
		switch quota.Metric {
		case "CPUS":
			p.cpuLeft = int(quota.Limit) - int(quota.Usage)
			p.cpuUsage = int(quota.Usage)
		case "INSTANCES":
			p.instLeft = int(quota.Limit) - int(quota.Usage)
			p.instUsage = int(quota.Usage)
		case "IN_USE_ADDRESSES":
			p.addrUsage = int(quota.Usage)
		}
	}
}

// SetEnabled marks the buildlet pool as enabled.
func (p *GCEBuildlet) SetEnabled(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabled = !enabled
}

// GetBuildlet retrieves a buildlet client for an available buildlet.
func (p *GCEBuildlet) GetBuildlet(ctx context.Context, hostType string, lg Logger) (bc *buildlet.Client, err error) {
	hconf, ok := dashboard.Hosts[hostType]
	if !ok {
		return nil, fmt.Errorf("gcepool: unknown host type %q", hostType)
	}
	qsp := lg.CreateSpan("awaiting_gce_quota")
	err = p.awaitVMCountQuota(ctx, hconf.GCENumCPU())
	qsp.Done(err)
	if err != nil {
		return nil, err
	}

	deleteIn, ok := ctx.Value(BuildletTimeoutOpt{}).(time.Duration)
	if !ok {
		deleteIn = deleteTimeout
	}

	instName := "buildlet-" + strings.TrimPrefix(hostType, "host-") + "-rn" + randHex(7)
	instName = strings.Replace(instName, "_", "-", -1) // Issue 22905; can't use underscores in GCE VMs
	p.setInstanceUsed(instName, true)

	gceBuildletSpan := lg.CreateSpan("create_gce_buildlet", instName)
	defer func() { gceBuildletSpan.Done(err) }()

	var (
		needDelete   bool
		createSpan   = lg.CreateSpan("create_gce_instance", instName)
		waitBuildlet spanlog.Span // made after create is done
		curSpan      = createSpan // either instSpan or waitBuildlet
	)

	zone := buildEnv.RandomVMZone()

	log.Printf("Creating GCE VM %q for %s at %s", instName, hostType, zone)
	bc, err = buildlet.StartNewVM(gcpCreds, buildEnv, instName, hostType, buildlet.VMOpts{
		DeleteIn: deleteIn,
		OnInstanceRequested: func() {
			log.Printf("GCE VM %q now booting", instName)
		},
		OnInstanceCreated: func() {
			needDelete = true

			createSpan.Done(nil)
			waitBuildlet = lg.CreateSpan("wait_buildlet_start", instName)
			curSpan = waitBuildlet
		},
		OnGotInstanceInfo: func(*compute.Instance) {
			lg.LogEventTime("got_instance_info", "waiting_for_buildlet...")
		},
		Zone: zone,
	})
	if err != nil {
		curSpan.Done(err)
		log.Printf("Failed to create VM for %s at %s: %v", hostType, zone, err)
		if needDelete {
			deleteVM(zone, instName)
			p.putVMCountQuota(hconf.GCENumCPU())
		}
		p.setInstanceUsed(instName, false)
		return nil, err
	}
	waitBuildlet.Done(nil)
	bc.SetDescription("GCE VM: " + instName)
	bc.SetGCEInstanceName(instName)
	bc.SetOnHeartbeatFailure(func() {
		p.putBuildlet(bc, hostType, zone, instName)
	})
	return bc, nil
}

func (p *GCEBuildlet) putBuildlet(bc *buildlet.Client, hostType, zone, instName string) error {
	// TODO(bradfitz): add the buildlet to a freelist (of max N
	// items) for up to 10 minutes since when it got started if
	// it's never seen a command execution failure, and we can
	// wipe all its disk content? (perhaps wipe its disk content
	// when it's retrieved, like the reverse buildlet pool) But
	// this will require re-introducing a distinction in the
	// buildlet client library between Close, Destroy/Halt, and
	// tracking execution errors.  That was all half-baked before
	// and thus removed. Now Close always destroys everything.
	deleteVM(zone, instName)
	p.setInstanceUsed(instName, false)

	hconf, ok := dashboard.Hosts[hostType]
	if !ok {
		panic("failed to lookup conf") // should've worked if we did it before
	}
	p.putVMCountQuota(hconf.GCENumCPU())
	return nil
}

// WriteHTMLStatus writes the status of the buildlet pool to an io.Writer.
func (p *GCEBuildlet) WriteHTMLStatus(w io.Writer) {
	fmt.Fprintf(w, "<b>GCE pool</b> capacity: %s", p.capacityString())
	const show = 6 // must be even
	active := p.instancesActive()
	if len(active) > 0 {
		fmt.Fprintf(w, "<ul>")
		for i, inst := range active {
			if i < show/2 || i >= len(active)-(show/2) {
				fmt.Fprintf(w, "<li>%v, %s</li>\n", inst.Name, friendlyDuration(time.Since(inst.Creation)))
			} else if i == show/2 {
				fmt.Fprintf(w, "<li>... %d of %d total omitted ...</li>\n", len(active)-show, len(active))
			}
		}
		fmt.Fprintf(w, "</ul>")
	}
}

func (p *GCEBuildlet) String() string {
	return fmt.Sprintf("GCE pool capacity: %s", p.capacityString())
}

func (p *GCEBuildlet) capacityString() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("%d/%d instances; %d/%d CPUs",
		len(p.inst), p.instUsage+p.instLeft,
		p.cpuUsage, p.cpuUsage+p.cpuLeft)
}

// awaitVMCountQuota waits for numCPU CPUs of quota to become available,
// or returns ctx.Err.
func (p *GCEBuildlet) awaitVMCountQuota(ctx context.Context, numCPU int) error {
	// Poll every 2 seconds, which could be better, but works and
	// is simple.
	for {
		if p.tryAllocateQuota(numCPU) {
			return nil
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// haveQuotaLocked reports whether the current GCE quota permits
// starting numCPU more CPUs.
//
// precondition: p.mu must be held.
func (p *GCEBuildlet) haveQuotaLocked(numCPU int) bool {
	return p.cpuLeft >= numCPU && p.instLeft >= 1 && len(p.inst) < maxInstances && p.addrUsage < maxInstances
}

func (p *GCEBuildlet) tryAllocateQuota(numCPU int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disabled {
		return false
	}
	if p.haveQuotaLocked(numCPU) {
		p.cpuUsage += numCPU
		p.cpuLeft -= numCPU
		p.instLeft--
		p.addrUsage++
		return true
	}
	return false
}

// putVMCountQuota adjusts the dead-reckoning of our quota usage by
// one instance and cpu CPUs.
func (p *GCEBuildlet) putVMCountQuota(cpu int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cpuUsage -= cpu
	p.cpuLeft += cpu
	p.instLeft++
}

func (p *GCEBuildlet) setInstanceUsed(instName string, used bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inst == nil {
		p.inst = make(map[string]time.Time)
	}
	if used {
		p.inst[instName] = time.Now()
	} else {
		delete(p.inst, instName)
	}
}

func (p *GCEBuildlet) instanceUsed(instName string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.inst[instName]
	return ok
}

func (p *GCEBuildlet) instancesActive() (ret []ResourceTime) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, create := range p.inst {
		ret = append(ret, ResourceTime{
			Name:     name,
			Creation: create,
		})
	}
	sort.Sort(ByCreationTime(ret))
	return ret
}

// ResourceTime is a GCE instance or Kube pod name and its creation time.
type ResourceTime struct {
	Name     string
	Creation time.Time
}

// ByCreationTime provides the functionality to sort resource times by
// the time of creation.
type ByCreationTime []ResourceTime

func (s ByCreationTime) Len() int           { return len(s) }
func (s ByCreationTime) Less(i, j int) bool { return s[i].Creation.Before(s[j].Creation) }
func (s ByCreationTime) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// CleanUpOldVMs loops forever and periodically enumerates virtual
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
func (p *GCEBuildlet) CleanUpOldVMs() {
	if gceMode == "dev" {
		return
	}
	if computeService == nil {
		return
	}

	// TODO(bradfitz): remove this list and just query it from the compute API?
	// http://godoc.org/google.golang.org/api/compute/v1#RegionsService.Get
	// and Region.Zones: http://godoc.org/google.golang.org/api/compute/v1#Region

	for {
		for _, zone := range buildEnv.VMZones {
			if err := p.cleanZoneVMs(zone); err != nil {
				log.Printf("Error cleaning VMs in zone %q: %v", zone, err)
			}
		}
		time.Sleep(time.Minute)
	}
}

// cleanZoneVMs is part of cleanUpOldVMs, operating on a single zone.
func (p *GCEBuildlet) cleanZoneVMs(zone string) error {
	// Fetch the first 500 (default) running instances and clean
	// those. We expect that we'll be running many fewer than
	// that. Even if we have more, eventually the first 500 will
	// either end or be cleaned, and then the next call will get a
	// partially-different 500.
	// TODO(bradfitz): revisit this code if we ever start running
	// thousands of VMs.
	gceAPIGate()
	list, err := computeService.Instances.List(buildEnv.ProjectName, zone).Do()
	if err != nil {
		return fmt.Errorf("listing instances: %v", err)
	}
	for _, inst := range list.Items {
		if inst.Metadata == nil {
			// Defensive. Not seen in practice.
			continue
		}
		if isGCERemoteBuildlet(inst.Name) {
			// Remote buildlets have their own expiration mechanism that respects active SSH sessions.
			log.Printf("cleanZoneVMs: skipping remote buildlet %q", inst.Name)
			continue
		}
		var sawDeleteAt bool
		var deleteReason string
		for _, it := range inst.Metadata.Items {
			if it.Key == "delete-at" {
				if it.Value == nil {
					log.Printf("missing delete-at value; ignoring")
					continue
				}
				unixDeadline, err := strconv.ParseInt(*it.Value, 10, 64)
				if err != nil {
					log.Printf("invalid delete-at value %q seen; ignoring", *it.Value)
					continue
				}
				sawDeleteAt = true
				if time.Now().Unix() > unixDeadline {
					deleteReason = "delete-at expiration"
				}
			}
		}
		isBuildlet := strings.HasPrefix(inst.Name, "buildlet-")

		if isBuildlet && !sawDeleteAt && !p.instanceUsed(inst.Name) {
			createdAt, _ := time.Parse(time.RFC3339Nano, inst.CreationTimestamp)
			if createdAt.Before(time.Now().Add(-3 * time.Hour)) {
				deleteReason = fmt.Sprintf("no delete-at, created at %s", inst.CreationTimestamp)
			}
		}

		// Delete buildlets (things we made) from previous
		// generations. Only deleting things starting with "buildlet-"
		// is a historical restriction, but still fine for paranoia.
		if deleteReason == "" && sawDeleteAt && isBuildlet && !p.instanceUsed(inst.Name) {
			if _, ok := deletedVMCache.Get(inst.Name); !ok {
				deleteReason = "from earlier coordinator generation"
			}
		}

		if deleteReason != "" {
			log.Printf("deleting VM %q in zone %q; %s ...", inst.Name, zone, deleteReason)
			deleteVM(zone, inst.Name)
		}

	}
	return nil
}

var deletedVMCache = lru.New(100) // keyed by instName

type token struct{}

// deleteVM starts a delete of an instance in a given zone.
//
// It either returns an operation name (if delete is pending) or the
// empty string if the instance didn't exist.
func deleteVM(zone, instName string) (operation string, err error) {
	deletedVMCache.Add(instName, token{})
	gceAPIGate()
	op, err := computeService.Instances.Delete(buildEnv.ProjectName, zone, instName).Do()
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

// HasScope returns true if the GCE metadata contains the default scopes.
func HasScope(want string) bool {
	// If not on GCE, assume full access
	if !metadata.OnGCE() {
		return true
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
	return HasScope(compute.ComputeScope) || HasScope(compute.CloudPlatformScope)
}

func hasStorageScope() bool {
	return HasScope(storage.ScopeReadWrite) || HasScope(storage.ScopeFullControl) || HasScope(compute.CloudPlatformScope)
}

// ReadGCSFile reads the named file from the GCS bucket.
func ReadGCSFile(name string) ([]byte, error) {
	if gceMode == "dev" {
		b, ok := testFiles[name]
		if !ok {
			return nil, &os.PathError{
				Op:   "open",
				Path: name,
				Err:  os.ErrNotExist,
			}
		}
		return []byte(b), nil
	}

	r, err := storageClient.Bucket(buildEnv.BuildletBucket).Object(name).NewReader(context.Background())
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ioutil.ReadAll(r)
}

// syncBuildStatsLoop runs forever in its own goroutine and syncs the
// coordinator's datastore Build & Span entities to BigQuery
// periodically.
func syncBuildStatsLoop(env *buildenv.Environment) {
	ticker := time.NewTicker(5 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if err := buildstats.SyncBuilds(ctx, env); err != nil {
			log.Printf("buildstats: SyncBuilds: %v", err)
		}
		if err := buildstats.SyncSpans(ctx, env); err != nil {
			log.Printf("buildstats: SyncSpans: %v", err)
		}
		cancel()
		<-ticker.C
	}
}

// createBasepinDisks creates zone-local copies of VM disk images, to
// speed up VM creations in the future.
//
// Other than a list call, this a no-op unless new VM images were
// added or updated recently.
func createBasepinDisks(ctx context.Context) {
	if !metadata.OnGCE() || (buildEnv != buildenv.Production && buildEnv != buildenv.Staging) {
		return
	}
	for {
		t0 := time.Now()
		bgc, err := buildgo.NewClient(ctx, buildEnv)
		if err != nil {
			log.Printf("basepin: NewClient: %v", err)
			return
		}
		log.Printf("basepin: creating basepin disks...")
		err = bgc.MakeBasepinDisks(ctx)
		d := time.Since(t0).Round(time.Second / 10)
		if err != nil {
			basePinErr.Store(err.Error())
			log.Printf("basepin: error creating basepin disks, after %v: %v", d, err)
			time.Sleep(5 * time.Minute)
			continue
		}
		basePinErr.Store("")
		log.Printf("basepin: created basepin disks after %v", d)
		return
	}
}
