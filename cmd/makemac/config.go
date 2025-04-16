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
		// amd64
		{
			Hostname: "darwin-amd64-11",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-11-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-11-key",
			Image:    "0b7fcc509b134c56dfa73b685f87ff396c6f8c35cc8c8398ca697c6bfd3b4d6a",
			Count:    5, // Fewer because it runs on release branches only.
		},
		{
			Hostname: "darwin-amd64-12",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-12-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-12-key",
			Image:    "d2969d6dcdfbfb5b19871d6e8ec6f7448bdff1dfd2c4b6a551c51c8d670852ca",
			Count:    8,
		},
		{
			Hostname: "darwin-amd64-13",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-13-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-13-key",
			Image:    "5de75fb250397fbd253d4a0b568485db73d5a7b0bd2cdeb999b0cdf0160d6e69",
			Count:    8,
		},
		{
			Hostname: "darwin-amd64-14",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-14-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-14-key",
			Image:    "88491078fb25b3bd6db3fe519d0bca63448cddf3f7f10177da2e46019664a85b",
			Count:    14, // More to cover longtest, race, nocgo, etc.
		},
		// arm64
		{
			Hostname: "darwin-arm64-14",
			Cert:     "secret:symbolic-datum-552/darwin-arm64-14-cert",
			Key:      "secret:symbolic-datum-552/darwin-arm64-14-key",
			Image:    "edbf519386cb968127d9e682ac5bbc9ca9fbc9b8e8657bade9488089bb842405",
			Count:    10,
		},
		{
			Hostname: "darwin-arm64-15",
			Cert:     "secret:symbolic-datum-552/darwin-arm64-15-cert",
			Key:      "secret:symbolic-datum-552/darwin-arm64-15-key",
			Image:    "203520220bdfe6b42592d2ae731c9cb985768c701c569ff1bc572d1573e63ad6",
			Count:    10,
		},
	},
	internalSwarming: {
		// amd64
		{
			Hostname: "darwin-amd64-11-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-11-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-11-security-key",
			Image:    "0b7fcc509b134c56dfa73b685f87ff396c6f8c35cc8c8398ca697c6bfd3b4d6a",
			Count:    3,
		},
		{
			Hostname: "darwin-amd64-12-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-12-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-12-security-key",
			Image:    "d2969d6dcdfbfb5b19871d6e8ec6f7448bdff1dfd2c4b6a551c51c8d670852ca",
			Count:    3,
		},
		{
			Hostname: "darwin-amd64-13-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-13-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-13-security-key",
			Image:    "5de75fb250397fbd253d4a0b568485db73d5a7b0bd2cdeb999b0cdf0160d6e69",
			Count:    3,
		},
		{
			Hostname: "darwin-amd64-14-security",
			Cert:     "secret:symbolic-datum-552/darwin-amd64-14-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-amd64-14-security-key",
			Image:    "88491078fb25b3bd6db3fe519d0bca63448cddf3f7f10177da2e46019664a85b",
			Count:    6, // More to cover longtest, race, nocgo, etc.
		},
		// arm64
		{
			Hostname: "darwin-arm64-14-security",
			Cert:     "secret:symbolic-datum-552/darwin-arm64-14-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-arm64-14-security-key",
			Image:    "edbf519386cb968127d9e682ac5bbc9ca9fbc9b8e8657bade9488089bb842405",
			Count:    5,
		},
		{
			Hostname: "darwin-arm64-15-security",
			Cert:     "secret:symbolic-datum-552/darwin-arm64-15-security-cert",
			Key:      "secret:symbolic-datum-552/darwin-arm64-15-security-key",
			Image:    "203520220bdfe6b42592d2ae731c9cb985768c701c569ff1bc572d1573e63ad6",
			Count:    5,
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
