// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package pool

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"sync"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal"
	"golang.org/x/build/internal/cloud"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/spanlog"
)

var _ Buildlet = (*EC2Buildlet)(nil)

// ec2Buildlet is the package level buildlet pool.
//
// TODO(golang.org/issues/38337) remove once a package level variable is no longer
// required by the main package.
var ec2Buildlet *EC2Buildlet

// EC2BuildetPool retrieves the package level EC2Buildlet pool set by the constructor.
//
// TODO(golang.org/issues/38337) remove once a package level variable is no longer
// required by the main package.
func EC2BuildetPool() *EC2Buildlet {
	return ec2Buildlet
}

func init() {
	// initializes a basic package level ec2Buildlet pool to enable basic testing in other
	// packages.
	//
	// TODO(golang.org/issues/38337) remove once a package level variable is no longer
	// required by the main package.
	ec2Buildlet = &EC2Buildlet{
		ledger: newLedger(),
	}
}

// awsClient represents the aws client used to interact with AWS. This is a partial
// implementation of pool.AWSClient.
type awsClient interface {
	DestroyInstances(ctx context.Context, instIDs ...string) error
	Quota(ctx context.Context, service, code string) (int64, error)
	InstanceTypesARM(ctx context.Context) ([]*cloud.InstanceType, error)
	RunningInstances(ctx context.Context) ([]*cloud.Instance, error)
}

// EC2Opt is optional configuration for the buildlet.
type EC2Opt func(*EC2Buildlet)

// EC2Buildlet manages a pool of AWS EC2 buildlets.
type EC2Buildlet struct {
	// awsClient is the client used to interact with AWS services.
	awsClient awsClient
	// buildEnv contains the build environment settings.
	buildEnv *buildenv.Environment
	// buildletClient is the client used to create a buildlet.
	buildletClient ec2BuildletClient
	// hosts provides the host configuration for all hosts. It is passed in to facilitate
	// testing.
	hosts map[string]*dashboard.HostConfig
	// isRemoteBuildlet informs the caller is a VM instance is being used as a remote
	// buildlet.
	//
	// TODO(golang.org/issues/38337) remove once we find a way to pass in remote buildlet
	// information at the get buidlet request.
	isRemoteBuildlet IsRemoteBuildletFunc
	// ledger tracks instances and their resource allocations.
	ledger *ledger
	// cancelPoll will signal to the pollers to discontinue polling.
	cancelPoll context.CancelFunc
	// pollWait waits for all pollers to terminate polling.
	pollWait sync.WaitGroup
}

// ec2BuildletClient represents an EC2 buildlet client in the buildlet package.
type ec2BuildletClient interface {
	StartNewVM(ctx context.Context, buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *buildlet.VMOpts) (buildlet.Client, error)
}

// NewEC2Buildlet creates a new EC2 buildlet pool used to create and manage the lifecycle of
// EC2 buildlets. Information about ARM64 instance types is retrieved before starting the pool.
// EC2 quota types are also retrieved before starting the pool. The pool will continuously poll
// for quotas which limit the resources that can be consumed by the pool. It will also periodically
// search for VMs which are no longer in use or are untracked by the pool in order to delete them.
func NewEC2Buildlet(client *cloud.AWSClient, buildEnv *buildenv.Environment, hosts map[string]*dashboard.HostConfig, fn IsRemoteBuildletFunc, opts ...EC2Opt) (*EC2Buildlet, error) {
	if fn == nil {
		return nil, errors.New("remote buildlet check function is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &EC2Buildlet{
		awsClient:        client,
		buildEnv:         buildEnv,
		buildletClient:   buildlet.NewEC2Client(client),
		cancelPoll:       cancel,
		hosts:            hosts,
		isRemoteBuildlet: fn,
		ledger:           newLedger(),
	}
	for _, opt := range opts {
		opt(b)
	}
	if err := b.retrieveAndSetQuota(ctx); err != nil {
		return nil, fmt.Errorf("unable to create EC2 pool: %w", err)
	}
	if err := b.retrieveAndSetInstanceTypes(); err != nil {
		return nil, fmt.Errorf("unable to create EC2 pool: %w", err)
	}

	b.pollWait.Add(1)
	// polls for the EC2 quota data and sets the quota data in
	// the ledger. When the context has been cancelled, the polling will stop.
	go func() {
		go internal.PeriodicallyDo(ctx, time.Hour, func(ctx context.Context, _ time.Time) {
			log.Printf("retrieveing EC2 quota")
			_ = b.retrieveAndSetQuota(ctx)
		})
		b.pollWait.Done()
	}()

	b.pollWait.Add(1)
	// poll queries for VMs which are not tracked in the ledger and
	// deletes them. When the context has been cancelled, the polling will stop.
	go func() {
		go internal.PeriodicallyDo(ctx, 2*time.Minute, func(ctx context.Context, _ time.Time) {
			log.Printf("cleaning up unused EC2 instances")
			b.destroyUntrackedInstances(ctx)
		})
		b.pollWait.Done()
	}()

	// TODO(golang.org/issues/38337) remove once a package level variable is no longer
	// required by the main package.
	ec2Buildlet = b
	return b, nil
}

// GetBuildlet retrieves a buildlet client for a newly created buildlet.
func (eb *EC2Buildlet) GetBuildlet(ctx context.Context, hostType string, lg Logger, si *queue.SchedItem) (buildlet.Client, error) {
	hconf, ok := eb.hosts[hostType]
	if !ok {
		return nil, fmt.Errorf("ec2 pool: unknown host type %q", hostType)
	}
	instName := instanceName(hostType, 7)
	log.Printf("Creating EC2 VM %q for %s", instName, hostType)
	kp, err := buildlet.NewKeyPair()
	if err != nil {
		log.Printf("failed to create TLS key pair for %s: %s", hostType, err)
		return nil, fmt.Errorf("failed to create TLS key pair: %w", err)
	}

	qsp := lg.CreateSpan("awaiting_ec2_quota")
	err = eb.ledger.ReserveResources(ctx, instName, hconf.MachineType(), si)
	qsp.Done(err)
	if err != nil {
		return nil, err
	}

	ec2BuildletSpan := lg.CreateSpan("create_ec2_buildlet", instName)
	defer func() { ec2BuildletSpan.Done(err) }()

	var (
		createSpan      = lg.CreateSpan("create_ec2_instance", instName)
		waitBuildlet    spanlog.Span
		curSpan         = createSpan
		instanceCreated bool
	)
	bc, err := eb.buildletClient.StartNewVM(ctx, eb.buildEnv, hconf, instName, hostType, &buildlet.VMOpts{
		Zone:     "", // allow the EC2 api pick an availability zone with capacity
		TLS:      kp,
		Meta:     make(map[string]string),
		DeleteIn: determineDeleteTimeout(hconf),
		OnInstanceRequested: func() {
			log.Printf("EC2 VM %q now booting", instName)
		},
		OnInstanceCreated: func() {
			log.Printf("EC2 VM %q now running", instName)
			createSpan.Done(nil)
			instanceCreated = true
			waitBuildlet = lg.CreateSpan("wait_buildlet_start", instName)
			curSpan = waitBuildlet
		},
		OnGotEC2InstanceInfo: func(inst *cloud.Instance) {
			lg.LogEventTime("got_instance_info", "waiting_for_buildlet...")
			eb.ledger.UpdateReservation(instName, inst.ID)
		},
	})
	if err != nil {
		curSpan.Done(err)
		log.Printf("EC2 VM creation failed for %s: %v", hostType, err)
		if instanceCreated {
			log.Printf("EC2 VM %q failed initialize buildlet client. deleting...", instName)
			eb.buildletDone(instName)
		} else {
			eb.ledger.Remove(instName)
		}
		return nil, err
	}
	waitBuildlet.Done(nil)
	bc.SetDescription(fmt.Sprintf("EC2 VM: %s", instName))
	bc.SetOnHeartbeatFailure(func() {
		log.Printf("EC2 VM %q failed heartbeat", instName)
		eb.buildletDone(instName)
	})
	bc.SetInstanceName(instName)
	return bc, nil
}

func (eb *EC2Buildlet) QuotaStats() map[string]*queue.QuotaStats {
	return map[string]*queue.QuotaStats{
		"ec2-cpu": eb.ledger.cpuQueue.ToExported(),
	}
}

// String gives a report of capacity usage for the EC2 buildlet pool.
func (eb *EC2Buildlet) String() string {
	return fmt.Sprintf("EC2 pool capacity: %s", eb.capacityString())
}

// capacityString() gives a report of capacity usage.
func (eb *EC2Buildlet) capacityString() string {
	r := eb.ledger.Resources()
	return fmt.Sprintf("%d instances; %d/%d CPUs", r.InstCount, r.CPUUsed, r.CPULimit)
}

// WriteHTMLStatus writes the status of the EC2 buildlet pool to an io.Writer.
func (eb *EC2Buildlet) WriteHTMLStatus(w io.Writer) {
	fmt.Fprintf(w, "<b>EC2 pool</b> capacity: %s", eb.capacityString())

	active := eb.ledger.ResourceTime()
	if len(active) > 0 {
		fmt.Fprintf(w, "<ul>")
		for _, inst := range active {
			fmt.Fprintf(w, "<li>%v, %s</li>\n", html.EscapeString(inst.Name), friendlyDuration(time.Since(inst.Creation)))
		}
		fmt.Fprintf(w, "</ul>")
	}
}

// buildletDone issues a call to destroy the EC2 instance and removes
// the instance from the ledger. Removing the instance from the ledger
// also releases any resources allocated to that instance. If an instance
// is not found in the ledger or on EC2 then an error is logged. All
// untracked instances will be cleaned up by the polling cleanupUnusedVMs
// method.
func (eb *EC2Buildlet) buildletDone(instName string) {
	vmID := eb.ledger.InstanceID(instName)
	if vmID == "" {
		log.Printf("EC2 vm %s not found", instName)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eb.awsClient.DestroyInstances(ctx, vmID); err != nil {
		log.Printf("EC2 VM %s deletion failed: %s", instName, err)
	}
	eb.ledger.Remove(instName)
}

// Close stops the pollers used by the EC2Buildlet pool from running.
func (eb *EC2Buildlet) Close() {
	eb.cancelPoll()
	eb.pollWait.Wait()
}

// retrieveAndSetQuota queries EC2 for account relevant quotas and sets the quota in the ledger.
func (eb *EC2Buildlet) retrieveAndSetQuota(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cpuQuota, err := eb.awsClient.Quota(ctx, cloud.QuotaServiceEC2, cloud.QuotaCodeCPUOnDemand)
	if err != nil {
		log.Printf("unable to query for EC2 cpu quota: %s", err)
		return err
	}
	eb.ledger.SetCPULimit(cpuQuota)
	return nil
}

// retrieveAndSetInstanceTypes retrieves the ARM64 instance types from the EC2
// service and sets them in the ledger.
func (eb *EC2Buildlet) retrieveAndSetInstanceTypes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	its, err := eb.awsClient.InstanceTypesARM(ctx)
	if err != nil {
		return fmt.Errorf("unable to retrieve EC2 instance types: %w", err)
	}
	eb.ledger.UpdateInstanceTypes(its)
	log.Printf("ec2 buildlet pool instance types updated")
	return nil
}

// destroyUntrackedInstances searches for VMs which exist but are not being tracked in the
// ledger and deletes them.
func (eb *EC2Buildlet) destroyUntrackedInstances(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	insts, err := eb.awsClient.RunningInstances(ctx)
	if err != nil {
		log.Printf("failed to query for instances: %s", err)
		return
	}
	deleteInsts := make([]string, 0, len(insts))
	for _, inst := range insts {
		if !isBuildlet(inst.Name) {
			// Non-buildlets have not been created by the EC2 buildlet pool. Their lifecycle
			// should not be managed by the pool.
			log.Printf("destroyUntrackedInstances: skipping non-buildlet %q", inst.Name)
			continue
		}
		if eb.isRemoteBuildlet(inst.Name) {
			// Remote buildlets have their own expiration mechanism that respects active SSH sessions.
			log.Printf("destroyUntrackedInstances: skipping remote buildlet %q", inst.Name)
			continue
		}
		if id := eb.ledger.InstanceID(inst.Name); id != "" {
			continue
		}
		deleteInsts = append(deleteInsts, inst.ID)
		log.Printf("queued for deleting untracked EC2 VM %q with id %q", inst.Name, inst.ID)
	}
	if len(deleteInsts) == 0 {
		return
	}
	if err := eb.awsClient.DestroyInstances(ctx, deleteInsts...); err != nil {
		log.Printf("failed cleaning EC2 VMs: %s", err)
	}
}
