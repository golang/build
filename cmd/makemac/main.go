// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command makemac manages MacService instances for LUCI.
//
// It performs several different operations:
//
//   - Detects MacService leases that MacService thinks are running, but never
//     connected to LUCI (failed to boot?) and destroys them.
//   - Detects MacService leases that MacService thinks are running, but LUCI
//     thinks are dead (froze/crashed?) and destoys them.
//   - Renews MacService leases that both MacService and LUCI agree are healthy
//     to ensure they don't expire.
//   - Destroys MacService leases with images that are not requested by the
//     configuration in config.go.
//   - Launches new MacService leases to ensure that there are the at least as
//     many leases of each type as specified in the configuration in config.go.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
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
	createExpirationDuration       = 24 * time.Hour
	createExpirationDurationString = "86400s"

	// Shorter renew expiration is a workaround to detect newly-created
	// leases. See comment in handleMissingBots.
	renewExpirationDuration       = 23 * time.Hour
	renewExpirationDurationString = "82800s" // 23h
)

const (
	macServiceCustomer = "golang"

	// Leases managed by makemac have ProjectName "makemac/SWARMING_HOST",
	// indicating that it is managed by makemac, and which swarming host it
	// belongs to. Leases without this project prefix will not be touched.
	//
	// Note that we track the swarming host directly in the lease project
	// name because new leases may not have yet connected to the swarming
	// server, but we still need to know which host to count them towards.
	managedProjectPrefix = "makemac"
)

func main() {
	if err := secret.InitFlagSupport(context.Background()); err != nil {
		log.Fatalln(err)
	}
	flag.Parse()

	if err := run(); err != nil {
		log.Fatalln(err)
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

	// Initialize each swarming client.
	for sc, ic := range prodImageConfig {
		c, err := swarming.NewClient(ctx, swarming.ClientOptions{
			ServiceURL:          "https://" + sc.Host,
			AuthenticatedClient: ac,
		})
		if err != nil {
			return fmt.Errorf("error creating swarming client for %s: %w", sc.Host, err)
		}
		sc.client = c

		logImageConfig(sc, ic)
	}

	// Always run once at startup.
	runOnce(ctx, prodImageConfig, mc)

	if *period == 0 {
		// User only wants a single check. We're done.
		return nil
	}

	t := time.NewTicker(*period)
	for range t.C {
		runOnce(ctx, prodImageConfig, mc)
	}

	return nil
}

func runOnce(ctx context.Context, config map[*swarmingConfig][]imageConfig, mc macServiceClient) {
	bots, err := swarmingBots(ctx, config)
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
	handleObsoleteLeases(mc, config, leases)
	addNewLeases(mc, config, leases, secret.DefaultResolver.ResolveSecret)
}

// leaseSwarmingHost returns the swarming host a managed lease belongs to.
//
// Returns "" if this isn't a managed lease.
func leaseSwarmingHost(l macservice.Lease) string {
	prefix, host, ok := strings.Cut(l.VMResourceNamespace.ProjectName, "/")
	if !ok {
		// Malformed project name, must not be managed.
		return ""
	}
	if prefix != managedProjectPrefix {
		// Some other prefix. Not managed.
		return ""
	}
	return host
}

func leaseIsManaged(l macservice.Lease) bool {
	return leaseSwarmingHost(l) != ""
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

		swarming := leaseSwarmingHost(inst.Lease)
		if swarming == "" {
			swarming = "<unmanaged>"
		}

		image := inst.InstanceSpecification.DiskSelection.ImageHashes.BootSHA256

		log.Printf("\t%s: image=%s\tswarming=%s", k, image, swarming)
	}
}

// e.g., darwin-amd64-11--39b47cf6-2aaa-4c80-b9cb-b800844fb104.golang.c3.macservice.goog
var botIDRe = regexp.MustCompile(`.*--([0-9a-f-]+)\.golang\..*\.macservice.goog$`)

// swarmingBots returns set of bots backed by MacService, as seen by swarming.
// The map key is the MacService lease ID.
// Bots may be dead.
func swarmingBots(ctx context.Context, config map[*swarmingConfig][]imageConfig) (map[string]*spb.BotInfo, error) {
	m := make(map[string]*spb.BotInfo)

	scs := sortedSwarmingConfigs(config)
	for _, sc := range scs {
		dimensions := []*spb.StringPair{
			{
				Key:   "pool",
				Value: sc.Pool,
			},
			{
				Key:   "os",
				Value: "Mac",
			},
		}
		bb, err := sc.client.ListBots(ctx, dimensions)
		if err != nil {
			return nil, fmt.Errorf("error listing bots: %w", err)
		}

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
// longer required or there are fewer of them requested. This is harmless.
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
// not requested by imageConfigs. This typically occurs when makemac is updated
// to roll out a new image version or if Count is reduced for an existing image.
func handleObsoleteLeases(mc macServiceClient, config map[*swarmingConfig][]imageConfig, leases map[string]macservice.Instance) {
	log.Printf("Checking for leases with obsolete images...")

	// swarming host -> image sha -> image config
	swarmingImages := make(map[string]map[string]*imageConfig)
	for sc, ic := range config {
		swarmingImages[sc.Host] = imageConfigMap(ic)
	}
	// swarming host -> image sha -> count
	swarmingImageCount := make(map[string]map[string]int)

	var ids []string
	for id := range leases {
		ids = append(ids, id)
	}
	// Sort to make the logs easier to follow when comparing vs a bot/lease
	// list.
	sort.Strings(ids)

	for _, id := range ids {
		lease := leases[id]

		swarming := leaseSwarmingHost(lease.Lease)
		if swarming == "" {
			log.Printf("Lease %s is not managed by makemac; skipping image check", id)
			continue
		}

		images, ok := swarmingImages[swarming]
		if !ok {
			log.Printf("Lease %s belongs to unknown swarming host %s; skipping image check", id, swarming)
			continue
		}

		var vacateReason string // A non-empty string means to vacate with said reason.

		image := lease.InstanceSpecification.DiskSelection.ImageHashes.BootSHA256
		if config, ok := images[image]; !ok {
			vacateReason = fmt.Sprintf("it uses an obsolete image %s", image)
		} else {
			if _, ok := swarmingImageCount[swarming]; !ok {
				swarmingImageCount[swarming] = make(map[string]int)
			}
			swarmingImageCount[swarming][image]++

			haveCount := swarmingImageCount[swarming][image]
			extra := haveCount - config.Count
			if extra > 0 {
				vacateReason = fmt.Sprintf("it is instance number %d, want only %d instances", haveCount, config.Count)
			}
		}

		if vacateReason == "" {
			continue
		}

		// Config doesn't want this instance. Vacate.
		log.Printf("Lease %s is being vacated because %s", id, vacateReason)
		log.Printf("Vacating lease %s...", id)
		if err := mc.Vacate(macservice.VacateRequest{LeaseID: id}); err != nil {
			log.Printf("Error vacating lease %s: %v", id, err)
			continue
		}
		delete(leases, id) // Drop from map so future calls know it is gone.
	}
}

func makeLeaseRequest(sc *swarmingConfig, ic *imageConfig, resolveSecret func(string) (string, error)) (macservice.LeaseRequest, error) {
	cert, err := resolveSecret(ic.Cert)
	if err != nil {
		return macservice.LeaseRequest{}, fmt.Errorf("error resolving certificate secret: %w", err)
	}
	key, err := resolveSecret(ic.Key)
	if err != nil {
		return macservice.LeaseRequest{}, fmt.Errorf("error resolving key secret: %w", err)
	}

	return macservice.LeaseRequest{
		VMResourceNamespace: macservice.Namespace{
			CustomerName: macServiceCustomer,
			ProjectName:  managedProjectPrefix + "/" + sc.Host,
		},
		InstanceSpecification: macservice.InstanceSpecification{
			Profile:     macservice.V1_MEDIUM_VM,
			AccessLevel: macservice.GOLANG_OSS,
			DiskSelection: macservice.DiskSelection{
				ImageHashes: macservice.ImageHashes{
					BootSHA256: ic.Image,
				},
			},
			Metadata: []macservice.MetadataEntry{
				{
					Key:   "golang.swarming",
					Value: sc.Host,
				},
				{
					Key:   "golang.hostname",
					Value: ic.Hostname,
				},
				{
					Key:   "golang.cert",
					Value: cert,
				},
				{
					Key:   "golang.key",
					Value: key,
				},
			},
		},
		Duration: createExpirationDurationString,
	}, nil
}

// addNewLeases adds new MacService leases as needed to ensure that there are
// at least Count makemac-managed leases of each configured image type.
// Removing leases if they exceed Count is done by handleObsoleteLeases.
func addNewLeases(
	mc macServiceClient, config map[*swarmingConfig][]imageConfig, leases map[string]macservice.Instance,
	resolveSecret func(string) (string, error),
) {
	log.Printf("Checking if new leases are required...")

	// Count images per swarming host. Each host gets a different
	// configuration. Map of swarming host -> image sha -> count.
	swarmingImageCount := make(map[string]map[string]int)
	for _, lease := range leases {
		swarming := leaseSwarmingHost(lease.Lease)
		if swarming == "" {
			// Don't count leases we don't manage.
			continue
		}
		if _, ok := swarmingImageCount[swarming]; !ok {
			swarmingImageCount[swarming] = make(map[string]int)
		}

		image := lease.InstanceSpecification.DiskSelection.ImageHashes.BootSHA256
		swarmingImageCount[swarming][image]++
	}

	// Iterate through configs in swarming order, then image order.
	swarmingOrder := sortedSwarmingConfigs(config)
	imageMap := make([]map[string]*imageConfig, 0, len(swarmingOrder))
	imageOrder := make([][]string, 0, len(swarmingOrder))
	for _, sc := range swarmingOrder {
		m := imageConfigMap(config[sc])
		order := make([]string, 0, len(m))
		for image := range m {
			order = append(order, image)
		}
		sort.Strings(order)
		imageMap = append(imageMap, m)
		imageOrder = append(imageOrder, order)
	}

	log.Printf("Current image lease count:")
	for i, sc := range swarmingOrder {
		for _, image := range imageOrder[i] {
			config := imageMap[i][image]
			gotCount := swarmingImageCount[sc.Host][config.Image]
			log.Printf("\tHost %s: image %s: have %d leases\twant %d leases", sc.Host, config.Image, gotCount, config.Count)
		}
	}

	for i, sc := range swarmingOrder {
		for _, image := range imageOrder[i] {
			config := imageMap[i][image]
			gotCount := swarmingImageCount[sc.Host][config.Image]
			need := config.Count - gotCount
			if need <= 0 {
				continue
			}

			log.Printf("Host %s: image %s: creating %d new leases", sc.Host, config.Image, need)
			req, err := makeLeaseRequest(sc, config, resolveSecret)
			if err != nil {
				log.Printf("Host %s: image %s: creating lease request: error %v", sc.Host, config.Image, err)
				continue
			}

			for i := 0; i < need; i++ {
				log.Printf("Host %s: image %s: creating lease %d...", sc.Host, config.Image, i)
				resp, err := mc.Lease(req)
				if err != nil {
					log.Printf("Host %s: image %s: creating lease %d: error %v", sc.Host, config.Image, i, err)
					continue
				}
				log.Printf("Host %s: image %s: created lease %s", sc.Host, config.Image, resp.PendingLease.LeaseID)
			}
		}
	}
}
