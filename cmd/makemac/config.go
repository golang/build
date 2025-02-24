// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"fmt"
	"log"
	"slices"

	"go.chromium.org/luci/swarming/client/swarming"
)

// swarmingConfig describes a swarming server.
type swarmingConfig struct {
	Host string // Swarming host URL
	Pool string // Pool containing MacService bots

	client swarming.Client
}

var (
	// Public swarming host.
	publicSwarming = &swarmingConfig{
		Host: "chromium-swarm.appspot.com",
		Pool: "luci.golang.shared-workers",
	}
	// Security swarming host.
	internalSwarming = &swarmingConfig{
		Host: "chrome-swarming.appspot.com",
		Pool: "luci.golang.security-try-workers",
	}
)

// imageConfig describes how many instances of a specific image type should
// exist.
type imageConfig struct {
	Hostname string // LUCI hostname prefix
	Cert     string // Bot certificate (resolved with internal/secret)
	Key      string // bot key (resolved with internal/secret)
	Image    string // image SHA
	Count    int    // target instance count to maintain
}

// Production image configuration for each swarming host.
//
// After changing an image here, makemac will automatically destroy instances
// with the old image. Changing hostname, cert, or key will _not_ automatically
// destroy instances.
//
// TODO(prattmic): rather than storing secrets in secret manager, makemac could
// use genbotcert to generate valid certificate/key pairs on the fly, unique to
// each lease, which could then have unique hostnames.
var prodImageConfig = map[*swarmingConfig][]imageConfig{
	publicSwarming: {
		{
			Hostname: "darwin-amd64-11",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-11-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-11-key",
			Image:    "3279e7f8aef8a1d02ba0897de44e5306f94c8cacec3c8c662a897b810879f655",
			Count:    5, // Fewer because it runs on release branches only.
		},
		{
			Hostname: "darwin-amd64-12",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-12-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-12-key",
			Image:    "959a409833522fcba0be62c0c818d68b29d4e1be28d3cbf43dbbc81cb3e3fdeb",
			Count:    8,
		},
		{
			Hostname: "darwin-amd64-13",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-13-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-13-key",
			Image:    "30efbbd26e846da8158a7252d47b3adca15b30270668a95620ace3502cdcaa36",
			Count:    8,
		},
		{
			Hostname: "darwin-amd64-14",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-14-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-14-key",
			Image:    "88491078fb25b3bd6db3fe519d0bca63448cddf3f7f10177da2e46019664a85b",
			Count:    14, // More to cover longtest, race, nocgo, etc.
		},
	},
	internalSwarming: {
		{
			Hostname: "darwin-amd64-11-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-11-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-11-security-key",
			Image:    "3279e7f8aef8a1d02ba0897de44e5306f94c8cacec3c8c662a897b810879f655",
			Count:    3,
		},
		{
			Hostname: "darwin-amd64-12-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-12-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-12-security-key",
			Image:    "959a409833522fcba0be62c0c818d68b29d4e1be28d3cbf43dbbc81cb3e3fdeb",
			Count:    3,
		},
		{
			Hostname: "darwin-amd64-13-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-13-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-13-security-key",
			Image:    "30efbbd26e846da8158a7252d47b3adca15b30270668a95620ace3502cdcaa36",
			Count:    3,
		},
		{
			Hostname: "darwin-amd64-14-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-14-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-14-security-key",
			Image:    "88491078fb25b3bd6db3fe519d0bca63448cddf3f7f10177da2e46019664a85b",
			Count:    6, // More to cover longtest, race, nocgo, etc.
		},
	},
}

// imageConfigMap returns a map from imageConfig.Image to imageConfig.
func imageConfigMap(cc []imageConfig) map[string]*imageConfig {
	m := make(map[string]*imageConfig)
	for _, c := range cc {
		c := c
		if _, ok := m[c.Image]; ok {
			panic(fmt.Sprintf("duplicate image %s in image config", c.Image))
		}
		m[c.Image] = &c
	}
	return m
}

// sortedSwarmingConfigs returns the swarming configs in c, sorted by host.
func sortedSwarmingConfigs(c map[*swarmingConfig][]imageConfig) []*swarmingConfig {
	scs := make([]*swarmingConfig, 0, len(c))
	for sc := range c {
		scs = append(scs, sc)
	}
	slices.SortFunc(scs, func(a, b *swarmingConfig) int {
		return cmp.Compare(a.Host, b.Host)
	})
	return scs
}

func init() {
	// Panic if prodImageConfig contains duplicates.
	for _, c := range prodImageConfig {
		imageConfigMap(c)
	}
}

func logImageConfig(sc *swarmingConfig, cc []imageConfig) {
	log.Printf("%s image configuration:", sc.Host)
	for _, c := range cc {
		log.Printf("\t%s: image=%s\tcount=%d", c.Hostname, c.Image, c.Count)
	}
}
