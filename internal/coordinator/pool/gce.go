// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

// Code interacting with Google Compute Engine (GCE) and
// a GCE implementation of the BuildletPool interface.

package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/datastore"
	"cloud.google.com/go/errorreporting"
	"cloud.google.com/go/storage"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/buildstats"
	"golang.org/x/build/internal/coordinator/pool/queue"
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

// Separate rate limit for deletions, which are more important than other
// actions, especially at server startup.
var deletionTicker = time.NewTicker(time.Second / 10)

func gceAPIGate() {
	<-apiCallTicker.C
}

func deletionAPIGate() {
	<-deletionTicker.C
}

// Initialized by InitGCE:
//
// TODO(golang.org/issue/38337): These should be moved into a struct as
// part of the effort to reduce package level variables.
var (
	buildEnv *buildenv.Environment

	// dsClient is a datastore client for the build project (symbolic-datum-552), where build progress is stored.
	dsClient *datastore.Client
	// goDSClient is a datastore client for golang-org, where build status is stored.
	goDSClient *datastore.Client
	// oAuthHTTPClient is the OAuth2 HTTP client used to make API calls to Google Cloud APIs.
	oAuthHTTPClient *http.Client
	computeService  *compute.Service
	gcpCreds        *google.Credentials
	errTryDeps      error // non-nil if try bots are disabled
	gerritClient    *gerrit.Client
	storageClient   *storage.Client
	inStaging       bool                   // are we running in the staging project? (named -dev)
	errorsClient    *errorreporting.Client // Stackdriver errors client
	gkeNodeHostname string

	// values created due to separating the buildlet pools into a separate package
	gceMode          string
	basePinErr       *atomic.Value
	isRemoteBuildlet IsRemoteBuildletFunc
)

// InitGCE initializes the GCE buildlet pool.
func InitGCE(sc *secret.Client, basePin *atomic.Value, fn IsRemoteBuildletFunc, buildEnvName, mode string) error {
	gceMode = mode
	basePinErr = basePin
	isRemoteBuildlet = fn

	ctx := context.Background()
	var err error

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
		gkeNodeHostname, err = metadata.Get("instance/hostname")
		if err != nil {
			return fmt.Errorf("failed to get current instance hostname: %v", err)
		}

		if len(buildEnv.VMZones) == 0 || buildEnv.VMRegion == "" {
			projectZone, err := metadata.Get("instance/zone")
			if err != nil || projectZone == "" {
				return fmt.Errorf("failed to get current GCE zone: %v", err)
			}
			// Convert the zone from "projects/1234/zones/us-central1-a" to "us-central1-a".
			projectZone = path.Base(projectZone)
			if len(buildEnv.VMZones) == 0 {
				buildEnv.VMZones = []string{projectZone}
			}
			if buildEnv.VMRegion == "" {
				buildEnv.VMRegion = strings.Join(strings.Split(projectZone, "-")[:2], "-")
			}
		}

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

	if mode != "dev" && metadata.OnGCE() && (buildEnv == buildenv.Production || buildEnv == buildenv.Staging) {
		go syncBuildStatsLoop(buildEnv)
		go gcePool.pollQuotaLoop()
		go createBasepinDisks(ctx)
	}

	return nil
}

// StorageClient retrieves the GCE storage client.
func StorageClient(ctx context.Context) (*storage.Client, error) {
	sc, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %w", err)
	}
	return sc, nil
}

// TODO(golang.org/issue/38337): These should be moved into a struct as
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

// InStaging returns a boolean denoting if the environment is staging.
func (c *GCEConfiguration) InStaging() bool {
	return inStaging
}

// GerritClient retrieves a gerrit client.
func (c *GCEConfiguration) GerritClient() *gerrit.Client {
	return gerritClient
}

// GKENodeHostname retrieves the GKE node hostname.
func (c *GCEConfiguration) GKENodeHostname() string {
	return gkeNodeHostname
}

// DSClient retrieves the datastore client.
func (c *GCEConfiguration) DSClient() *datastore.Client {
	return dsClient
}

// GoDSClient retrieves the datastore client for golang.org project.
func (c *GCEConfiguration) GoDSClient() *datastore.Client {
	return goDSClient
}

// TryDepsErr retrieves any Trybot dependency error.
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

var gcePool = &GCEBuildlet{
	c2cpuQueue:  queue.NewQuota(),
	cpuQueue:    queue.NewQuota(),
	instQueue:   queue.NewQuota(),
	n2cpuQueue:  queue.NewQuota(),
	n2dcpuQueue: queue.NewQuota(),
	t2acpuQueue: queue.NewQuota(),
}

var _ Buildlet = (*GCEBuildlet)(nil)

// GCEBuildlet manages a pool of GCE buildlets.
type GCEBuildlet struct {
	mu sync.Mutex // guards all following

	disabled bool

	// CPU quota usage & limits. pollQuota updates quotas periodically.
	// The values recorded here reflect the updates as well as our own
	// bookkeeping of instances as they are created and destroyed.
	c2cpuQueue  *queue.Quota
	cpuQueue    *queue.Quota
	instQueue   *queue.Quota
	n2cpuQueue  *queue.Quota
	n2dcpuQueue *queue.Quota
	t2acpuQueue *queue.Quota
	inst        map[string]time.Time // GCE VM instance name -> creationTime
}

func (p *GCEBuildlet) pollQuotaLoop() {
	for {
		p.pollQuota()
		time.Sleep(time.Minute)
	}
}

// pollQuota updates cpu usage and limits from the compute API.
func (p *GCEBuildlet) pollQuota() {
	gceAPIGate()
	reg, err := computeService.Regions.Get(buildEnv.ProjectName, buildEnv.VMRegion).Do()
	if err != nil {
		log.Printf("Failed to get quota for %s/%s: %v", buildEnv.ProjectName, buildEnv.VMRegion, err)
		return
	}

	if err := p.updateUntrackedQuota(); err != nil {
		log.Printf("Failed to update quota used by other instances: %q", err)
	}
	for _, quota := range reg.Quotas {
		switch quota.Metric {
		case "CPUS":
			p.cpuQueue.UpdateLimit(int(quota.Limit))
		case "C2_CPUS":
			p.c2cpuQueue.UpdateLimit(int(quota.Limit))
		case "N2_CPUS":
			p.n2cpuQueue.UpdateLimit(int(quota.Limit))
		case "N2D_CPUS":
			p.n2dcpuQueue.UpdateLimit(int(quota.Limit))
		case "T2A_CPUS":
			p.t2acpuQueue.UpdateLimit(int(quota.Limit))
		case "INSTANCES":
			p.instQueue.UpdateLimit(int(quota.Limit))
		}
	}
}

func (p *GCEBuildlet) QuotaStats() map[string]*queue.QuotaStats {
	return map[string]*queue.QuotaStats{
		"gce-cpu":       p.cpuQueue.ToExported(),
		"gce-c2-cpu":    p.c2cpuQueue.ToExported(),
		"gce-n2-cpu":    p.n2cpuQueue.ToExported(),
		"gce-n2d-cpu":   p.n2dcpuQueue.ToExported(),
		"gce-t2a-cpu":   p.t2acpuQueue.ToExported(),
		"gce-instances": p.instQueue.ToExported(),
	}
}

func (p *GCEBuildlet) updateUntrackedQuota() error {
	untrackedQuotas := make(map[*queue.Quota]int)
	for _, zone := range buildEnv.VMZones {
		gceAPIGate()
		err := computeService.Instances.List(buildEnv.ProjectName, zone).Pages(context.Background(), func(list *compute.InstanceList) error {
			for _, inst := range list.Items {
				if isBuildlet(inst.Name) {
					continue
				}
				untrackedQuotas[p.queueForMachineType(inst.MachineType)] += GCENumCPU(inst.MachineType)
			}
			if list.NextPageToken != "" {
				// Don't use all our quota flipping through pages.
				gceAPIGate()
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for quota, num := range untrackedQuotas {
		quota.UpdateUntracked(num)
	}
	return nil
}

// SetEnabled marks the buildlet pool as enabled.
func (p *GCEBuildlet) SetEnabled(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabled = !enabled
}

// GetBuildlet retrieves a buildlet client for an available buildlet.
func (p *GCEBuildlet) GetBuildlet(ctx context.Context, hostType string, lg Logger, si *queue.SchedItem) (bc buildlet.Client, err error) {
	if p.disabled {
		return nil, errors.New("pool disabled by configuration")
	}
	hconf, ok := dashboard.Hosts[hostType]
	if !ok {
		return nil, fmt.Errorf("gcepool: unknown host type %q", hostType)
	}
	qsp := lg.CreateSpan("awaiting_gce_quota")
	instItem := p.instQueue.Enqueue(1, si)
	if err := instItem.Await(ctx); err != nil {
		return nil, err
	}
	cpuItem := p.queueForMachineType(hconf.MachineType()).Enqueue(GCENumCPU(hconf.MachineType()), si)
	err = cpuItem.Await(ctx)
	qsp.Done(err)
	if err != nil {
		// return unused quota
		instItem.ReturnQuota()
		return nil, err
	}

	instName := instanceName(hostType, 7)
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
	cleanup := func() {
		if needDelete {
			deleteVM(zone, instName)
		}
		instItem.ReturnQuota()
		cpuItem.ReturnQuota()
		p.setInstanceUsed(instName, false)
	}

	log.Printf("Creating GCE VM %q for %s at %s", instName, hostType, zone)
	attempts := 1
	for {
		bc, err = buildlet.StartNewVM(gcpCreds, buildEnv, instName, hostType, buildlet.VMOpts{
			DeleteIn: determineDeleteTimeout(hconf),
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
			Zone:       zone,
			DiskSizeGB: hconf.RootDriveSizeGB,
		})
		if errors.Is(err, buildlet.ErrQuotaExceeded) && ctx.Err() == nil {
			log.Printf("Failed to create VM because quota exceeded. Retrying after 10 second (attempt: %d).", attempts)
			attempts++
			time.Sleep(10 * time.Second)
			continue
		} else if err != nil {
			curSpan.Done(err)
			log.Printf("Failed to create VM for %s at %s: %v", hostType, zone, err)
			cleanup()
			return nil, err
		}
		break
	}
	waitBuildlet.Done(nil)
	bc.SetDescription("GCE VM: " + instName)
	bc.SetInstanceName(instName)
	bc.SetOnHeartbeatFailure(cleanup)
	return bc, nil
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
	cpuUsage := p.cpuQueue.Quotas()
	c2Usage := p.c2cpuQueue.Quotas()
	instUsage := p.instQueue.Quotas()
	n2Usage := p.n2cpuQueue.Quotas()
	n2dUsage := p.n2dcpuQueue.Quotas()
	t2aUsage := p.t2acpuQueue.Quotas()
	return fmt.Sprintf("%d/%d instances; %d/%d CPUs, %d/%d C2_CPUS, %d/%d N2_CPUS, %d/%d N2D_CPUS %d/%d T2A_CPUS",
		instUsage.Used, instUsage.Limit,
		cpuUsage.Used, cpuUsage.Limit,
		c2Usage.Used, c2Usage.Limit,
		n2Usage.Used, n2Usage.Limit,
		n2dUsage.Used, n2dUsage.Limit,
		t2aUsage.Used, t2aUsage.Limit)
}

func (p *GCEBuildlet) queueForMachineType(mt string) *queue.Quota {
	if strings.HasPrefix(mt, "n2-") {
		return p.n2cpuQueue
	} else if strings.HasPrefix(mt, "n2d-") {
		return p.n2dcpuQueue
	} else if strings.HasPrefix(mt, "c2-") {
		return p.c2cpuQueue
	} else if strings.HasPrefix(mt, "t2a-") {
		return p.t2acpuQueue
	} else {
		// E2 and N1 instances are counted here. We do not use M1, M2,
		// or A2 quotas. See
		// https://cloud.google.com/compute/quotas#cpu_quota.
		return p.cpuQueue
	}
}

// returnQuota adjusts the dead-reckoning of our quota usage by
// one instance and cpu CPUs.
func (p *GCEBuildlet) returnQuota(hconf *dashboard.HostConfig) {
	machineType := hconf.MachineType()
	p.queueForMachineType(hconf.MachineType()).ReturnQuota(GCENumCPU(machineType))
	p.instQueue.ReturnQuota(1)
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
	// https://godoc.org/google.golang.org/api/compute/v1#RegionsService.Get
	// and Region.Zones: https://godoc.org/google.golang.org/api/compute/v1#Region

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
	deletionAPIGate()
	err := computeService.Instances.List(buildEnv.ProjectName, zone).Pages(context.Background(), func(list *compute.InstanceList) error {
		for _, inst := range list.Items {
			if inst.Metadata == nil {
				// Defensive. Not seen in practice.
				continue
			}
			if isRemoteBuildlet(inst.Name) {
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
			isBuildlet := isBuildlet(inst.Name)

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
		if list.NextPageToken != "" {
			// Don't use all our quota flipping through pages.
			deletionAPIGate()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("listing instances: %v", err)
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
	deletionAPIGate()
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
	return slices.Contains(scopes, want)
}

func hasComputeScope() bool {
	return HasScope(compute.ComputeScope) || HasScope(compute.CloudPlatformScope)
}

func hasStorageScope() bool {
	return HasScope(storage.ScopeReadWrite) || HasScope(storage.ScopeFullControl) || HasScope(compute.CloudPlatformScope)
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

// GCENumCPU returns the number of GCE CPUs used by the specified machine type.
func GCENumCPU(machineType string) int {
	if strings.HasSuffix(machineType, "e2-medium") || strings.HasSuffix(machineType, "e2-small") || strings.HasSuffix(machineType, "e2-micro") {
		return 2
	}
	n, _ := strconv.Atoi(machineType[strings.LastIndex(machineType, "-")+1:])
	return n
}
