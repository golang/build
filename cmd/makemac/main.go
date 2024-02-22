// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command makemac manages MacService instances for LUCI.
//
// It performs several different operations:
//
// * Detects MacService leases that MacService thinks are running, but never
//   connected to LUCI (failed to boot?) and destroys them.
// * Detects MacService leases that MacService thinks are running, but LUCI
//   thinks are dead (froze/crashed?) and destoys them.
// * Renews MacService leases that both MacService and LUCI agree are healthy
//   to ensure they don't expire.
// * Destroys MacService leases with images that are not requested by the
//   configuration in config.go.
// * Launches new MacService leases to ensure that there are the at least as
//   many leases of each type as specified in the configuration in config.go.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"regexp"
	"sort"
	"time"

	"go.chromium.org/luci/swarming/client/swarming"
	spb "go.chromium.org/luci/swarming/proto/api_v2"
	"golang.org/x/build/internal/macservice"
	"golang.org/x/build/internal/secret"
	"golang.org/x/oauth2/google"
)

var (
	apiKey = secret.Flag("macservice-api-key", "MacService API key")
	period = flag.Duration("period", 1*time.Hour, "How often to check bots and leases. As a special case, -period=0 checks exactly once and then exits")
	dryRun = flag.Bool("dry-run", false, "Print the actions that would be taken without actually performing them")
)

const (
	createExpirationDuration = 24*time.Hour
	createExpirationDurationString = "86400s"

	// Shorter renew expiration is a workaround to detect newly-created
	// leases. See comment in handleMissingBots.
	renewExpirationDuration = 23*time.Hour
	renewExpirationDurationString = "82800s" // 23h
)

const (
	swarmingService = "https://chromium-swarm.appspot.com"
	swarmingPool    = "luci.golang.shared-workers"
)

const (
	macServiceCustomer = "golang"

	// Leases managed by makemac have ProjectName "makemac". Leases without
	// this project will not be touched.
	managedProject = "makemac"
)

func main() {
	secret.InitFlagSupport(context.Background())
	flag.Parse()

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()

	var mc macServiceClient
	mc = macservice.NewClient(*apiKey)
	if *dryRun {
		mc = readOnlyMacServiceClient{mc: mc}
	}

	// Use service account / application default credentials for swarming
	// authentication.
	ac, err := google.DefaultClient(ctx)
	if err != nil {
		return fmt.Errorf("error creating authenticated client: %w", err)
	}

	sc, err := swarming.NewClient(ctx, swarming.ClientOptions{
		ServiceURL:          swarmingService,
		AuthenticatedClient: ac,
	})
	if err != nil {
		return fmt.Errorf("error creating swarming client: %w", err)
	}

	logImageConfig(prodImageConfig)

	// Always run once at startup.
	runOnce(ctx, sc, mc)

	if *period == 0 {
		// User only wants a single check. We're done.
		return nil
	}

	t := time.NewTicker(*period)
	for range t.C {
		runOnce(ctx, sc, mc)
	}

	return nil
}

func runOnce(ctx context.Context, sc swarming.Client, mc macServiceClient) {
	bots, err := swarmingBots(ctx, sc)
	if err != nil {
		log.Printf("Error looking up swarming bots: %v", err)
		return
	}

	leases, err := macServiceLeases(mc)
	if err != nil {
		log.Printf("Error looking up MacService leases: %v", err)
		return
	}

	logSummary(bots, leases)

	// These directly correspond to the operation described in the package
	// comment above.
	handleMissingBots(mc, bots, leases)
	handleDeadBots(mc, bots, leases)
	renewLeases(mc, leases)
	handleObsoleteLeases(mc, prodImageConfig, leases)
	addNewLeases(mc, prodImageConfig, leases)
}

func leaseIsManaged(l macservice.Lease) bool {
	return l.VMResourceNamespace.ProjectName == managedProject
}

func logSummary(bots map[string]*spb.BotInfo, leases map[string]macservice.Instance) {
	keys := make([]string, 0, len(bots))
	for k := range bots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	log.Printf("Swarming bots:")
	for _, k := range keys {
		b := bots[k]

		alive := true
		if b.GetIsDead() {
			alive = false
		}

		os := "<unknown OS version>"
		dimensions := b.GetDimensions()
		for _, d := range dimensions {
			if d.Key != "os" {
				continue
			}
			if len(d.Value) == 0 {
				continue
			}
			os = d.Value[len(d.Value)-1] // most specific value last.
		}

		log.Printf("\t%s: alive=%t\tos=%s", k, alive, os)
	}

	keys = make([]string, 0, len(leases))
	for k := range leases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	log.Printf("MacService leases:")
	for _, k := range keys {
		inst := leases[k]

		managed := false
		if leaseIsManaged(inst.Lease) {
			managed = true
		}

		image := inst.InstanceSpecification.DiskSelection.ImageHashes.BootSHA256

		log.Printf("\t%s: managed=%t\timage=%s", k, managed, image)
	}
}

// e.g., darwin-amd64-11--39b47cf6-2aaa-4c80-b9cb-b800844fb104.golang.c3.macservice.goog
var botIDRe = regexp.MustCompile(`.*--([0-9a-f-]+)\.golang\..*\.macservice.goog$`)

// swarmingBots returns set of bots backed by MacService, as seen by swarming.
// The map key is the MacService lease ID.
// Bots may be dead.
func swarmingBots(ctx context.Context, sc swarming.Client) (map[string]*spb.BotInfo, error) {
	dimensions := []*spb.StringPair{
		{
			Key:   "pool",
			Value: swarmingPool,
		},
		{
			Key:   "os",
			Value: "Mac",
		},
	}
	bb, err := sc.ListBots(ctx, dimensions)
	if err != nil {
		return nil, fmt.Errorf("error listing bots: %w", err)
	}

	m := make(map[string]*spb.BotInfo)

	for _, b := range bb {
		id := b.GetBotId()
		match := botIDRe.FindStringSubmatch(id)
		if match == nil {
			log.Printf("Swarming bot %s is not a MacService bot, skipping...", id)
			continue
		}

		lease := match[1]
		m[lease] = b
	}

	return m, nil
}

// macServiceLeases returns the set of active MacService leases.
func macServiceLeases(mc macServiceClient) (map[string]macservice.Instance, error) {
	resp, err := mc.Find(macservice.FindRequest{
		VMResourceNamespace: macservice.Namespace{
			CustomerName: "golang",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("error finding leases: %v", err)
	}

	m := make(map[string]macservice.Instance)

	for _, i := range resp.Instances {
		m[i.Lease.LeaseID] = i
	}

	return m, nil
}

// handleMissingBots detects MacService leases that MacService thinks are
// running, but never connected to LUCI (i.e., missing completely from LUCI)
// and destroys them.
//
// These are bots that perhaps never successfully booted?
func handleMissingBots(mc macServiceClient, bots map[string]*spb.BotInfo, leases map[string]macservice.Instance) {
	log.Printf("Checking for missing bots...")

	var missing []string
	for id := range leases {
		if _, ok := bots[id]; !ok {
			missing = append(missing, id)
		}
	}
	// Sort to make the logs easier to follow when comparing vs a bot/lease
	// list.
	sort.Strings(missing)

	for _, id := range missing {
		lease := leases[id]

		if !leaseIsManaged(lease.Lease) {
			log.Printf("Lease %s missing from LUCI, but not managed by makemac; skipping", id)
			continue
		}

		// There is a race window here: if this lease was created in
		// the last few minutes, the initial boot may still be ongoing,
		// and thus being missing from LUCI is expected. We don't want
		// to destroy these leases.
		//
		// Unfortunately MacService doesn't report lease creation time,
		// so we can't trivially check for this case. It does report
		// expiration time. As a workaround, we create new leases with
		// a 24h expiration time, but renew leases with a 23h
		// expiration. Thus if we see expiration is >23h from now then
		// this lease must have been created in the last hour.
		untilExpiration := time.Until(lease.Lease.Expires)
		if untilExpiration > renewExpirationDuration {
			log.Printf("Lease %s missing from LUCI, but created in the last hour (still booting?); skipping", id)
			continue
		}

		log.Printf("Lease %s missing from LUCI; failed initial boot?", id)
		log.Printf("Vacating lease %s...", id)
		if err := mc.Vacate(macservice.VacateRequest{LeaseID: id}); err != nil {
			log.Printf("Error vacating lease %s: %v", id, err)
			continue
		}
		delete(leases, id) // Drop from map so future calls know it is gone.
	}
}

// handleDeadBots detects MacService leases that MacService thinks are running,
// but LUCI thinks are dead (froze/crashed?) and destoys them.
//
// These are bots that perhaps froze/crashed at some point after starting.
func handleDeadBots(mc macServiceClient, bots map[string]*spb.BotInfo, leases map[string]macservice.Instance) {
	log.Printf("Checking for dead bots...")

	var dead []string
	for id, b := range bots {
		if b.GetIsDead() {
			dead = append(dead, id)
		}
	}
	// Sort to make the logs easier to follow when comparing vs a bot/lease
	// list.
	sort.Strings(dead)

	for _, id := range dead {
		lease, ok := leases[id]
		if !ok {
			// Dead bot already gone from MacService; nothing to do.
			continue
		}

		if !leaseIsManaged(lease.Lease) {
			log.Printf("Lease %s is dead on LUCI, but still present on MacService, but not managed by makemac; skipping", id)
			continue
		}

		// No need to check for newly created leases like we do in
		// handleMissingBots. If a bot appears as dead on LUCI then it
		// must have successfully connected at some point.

		log.Printf("Lease %s is dead on LUCI, but still present on MacService; VM froze/crashed?", id)
		log.Printf("Vacating lease %s...", id)
		if err := mc.Vacate(macservice.VacateRequest{LeaseID: id}); err != nil {
			log.Printf("Error vacating lease %s: %v", id, err)
			continue
		}
		delete(leases, id) // Drop from map so future calls know it is gone.
	}
}

// renewLeases renews lease expiration on all makemac-managed leases. Note that
// this may renew leases that will later be removed because their image is no
// longer required. This is harmless.
func renewLeases(mc macServiceClient, leases map[string]macservice.Instance) {
	log.Printf("Renewing leases...")

	var ids []string
	for id := range leases {
		ids = append(ids, id)
	}
	// Sort to make the logs easier to follow when comparing vs a bot/lease
	// list.
	sort.Strings(ids)

	for _, id := range ids {
		lease := leases[id]

		if !leaseIsManaged(lease.Lease) {
			log.Printf("Lease %s is not managed by makemac; skipping renew", id)
			continue
		}

		// Extra spaces to make expiration line up with the renewal message below.
		log.Printf("Lease ID: %s currently expires:    %v", lease.Lease.LeaseID, lease.Lease.Expires)

		// Newly created leases have a longer expiration duration than
		// our renewal expiration duration. Don't renew these, which
		// would would unintentionally shorten their expiration. See
		// comment in handleMissingBots.
		until := time.Until(lease.Lease.Expires)
		if until > renewExpirationDuration {
			log.Printf("Lease ID: %s skip renew, current expiration further out than renew expiration", lease.Lease.LeaseID)
			continue
		}

		rr, err := mc.Renew(macservice.RenewRequest{
			LeaseID:  lease.Lease.LeaseID,
			Duration: renewExpirationDurationString,
		})
		if err == nil {
			log.Printf("Lease ID: %s renewed, now expires: %v", lease.Lease.LeaseID, rr.Expires)
		} else {
			log.Printf("Lease ID: %s error renewing %v", lease.Lease.LeaseID, err)
		}
	}
}

// handleObsoleteLeases vacates any makemac-managed leases with images that are
// not requested by imageConfigs. This typically occurs when updating makemac
// to roll out a new image version.
func handleObsoleteLeases(mc macServiceClient, config []imageConfig, leases map[string]macservice.Instance) {
	log.Printf("Checking for leases with obsolete images...")

	configMap := imageConfigMap(config)

	var ids []string
	for id := range leases {
		ids = append(ids, id)
	}
	// Sort to make the logs easier to follow when comparing vs a bot/lease
	// list.
	sort.Strings(ids)

	for _, id := range ids {
		lease := leases[id]

		if !leaseIsManaged(lease.Lease) {
			log.Printf("Lease %s is not managed by makemac; skipping image check", id)
			continue
		}

		image := lease.InstanceSpecification.DiskSelection.ImageHashes.BootSHA256
		if _, ok := configMap[image]; ok {
			continue
		}

		// Config doesn't want instances with this image. Vacate.
		log.Printf("Lease %s uses obsolete image %s", id, image)
		log.Printf("Vacating lease %s...", id)
		if err := mc.Vacate(macservice.VacateRequest{LeaseID: id}); err != nil {
			log.Printf("Error vacating lease %s: %v", id, err)
			continue
		}
		delete(leases, id) // Drop from map so future calls know it is gone.
	}
}

func makeLeaseRequest(image string) macservice.LeaseRequest {
	return macservice.LeaseRequest{
		VMResourceNamespace: macservice.Namespace{
			CustomerName: macServiceCustomer,
			ProjectName:  managedProject,
		},
		InstanceSpecification: macservice.InstanceSpecification{
			Profile: macservice.V1_MEDIUM_VM,
			AccessLevel: macservice.GOLANG_OSS,
			DiskSelection: macservice.DiskSelection{
				ImageHashes: macservice.ImageHashes{
					BootSHA256: image,
				},
			},
		},
		Duration: createExpirationDurationString,
	}
}

// addNewLeases adds new MacService leases as needed to ensure that there are
// at least MinCount makemac-managed leases of each configured image type.
func addNewLeases(mc macServiceClient, config []imageConfig, leases map[string]macservice.Instance) {
	log.Printf("Checking if new leases are required...")

	configMap := imageConfigMap(config)

	imageCount := make(map[string]int)

	for _, lease := range leases {
		if !leaseIsManaged(lease.Lease) {
			// Don't count leases we don't manage.
			continue
		}

		image := lease.InstanceSpecification.DiskSelection.ImageHashes.BootSHA256
		imageCount[image]++
	}

	var images []string
	for image := range configMap {
		images = append(images, image)
	}
	sort.Strings(images)

	log.Printf("Current image lease count:")
	for _, image := range images {
		config := configMap[image]
		gotCount := imageCount[config.Image]
		log.Printf("\t%s: have %d leases\twant %d leases", config.Image, gotCount, config.MinCount)
	}

	for _, image := range images {
		config := configMap[image]
		gotCount := imageCount[config.Image]
		need := config.MinCount - gotCount
		if need <= 0 {
			continue
		}

		log.Printf("Image %s: creating %d new leases", config.Image, need)
		for i := 0; i < need; i++ {
			log.Printf("Image %s: creating lease %d...", config.Image, i)
			resp, err := mc.Lease(makeLeaseRequest(config.Image))
			if err != nil {
				log.Printf("Image %s: creating lease %d: error %v", config.Image, i, err)
				continue
			}
			log.Printf("Image %s: created lease %s", config.Image, resp.PendingLease.LeaseID)
		}
	}
}
