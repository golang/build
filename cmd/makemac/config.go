// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"log"
)

// imageConfig describes how many instances of a specific image type should
// exist.
type imageConfig struct {
	Hostname string // LUCI hostname prefix
	Cert     string // Bot certificate (resolved with internal/secret)
	Key      string // bot key (resolved with internal/secret)
	Image    string // image SHA
	MinCount int    // minimum instance count to maintain
}

// Production image configuration.
//
// After changing an image here, makemac will automatically destroy instances
// with the old image. Changing hostname, cert, or key will _not_ automatically
// destroy instances.
//
// TODO(prattmic): rather than storing secrets in secret manager, makemac could
// use genbotcert to generate valid certificate/key pairs on the fly, unique to
// each lease, which could then have unique hostnames.
var prodImageConfig = []imageConfig{
	{
		Hostname: "darwin-amd64-10_15",
		Cert:     "secret:symbolic-datum-552/darwin-amd64-10_15-cert",
		Key:      "secret:symbolic-datum-552/darwin-amd64-10_15-key",
		Image:    "4aaca93eedef29a20715259ee1a5a5f4309528dc1ef8a0ab2a0dafa08286ca57",
		MinCount: 5, // release branches only
	},
	{
		Hostname: "darwin-amd64-11",
		Cert:     "secret:symbolic-datum-552/darwin-amd64-11-cert",
		Key:      "secret:symbolic-datum-552/darwin-amd64-11-key",
		Image:    "f0cc898922b37726f6d5ad7b260e92b0443c6289b535cb0a32fd2955abe8adcc",
		MinCount: 10,
	},
	{
		Hostname: "darwin-amd64-12",
		Cert:     "secret:symbolic-datum-552/darwin-amd64-12-cert",
		Key:      "secret:symbolic-datum-552/darwin-amd64-12-key",
		Image:    "0a45171fb12a7efc3e7c5170b3292e592822dfc63c15aca0d093d94621097b8d",
		MinCount: 10,
	},
	{
		Hostname: "darwin-amd64-13",
		Cert:     "secret:symbolic-datum-552/darwin-amd64-13-cert",
		Key:      "secret:symbolic-datum-552/darwin-amd64-13-key",
		Image:    "f1bda73984f0725f2fa147d277ef87498bdec170030e1c477ee3576b820f1fb6",
		MinCount: 10,
	},
	{
		Hostname: "darwin-amd64-14",
		Cert:     "secret:symbolic-datum-552/darwin-amd64-14-cert",
		Key:      "secret:symbolic-datum-552/darwin-amd64-14-key",
		Image:    "ad1a56b7fec85ead9992b04444c4b5aef81becf38f85529976646f14a9ce5410",
		MinCount: 10,
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

func init() {
	// Panic if prodImageConfig contains duplicates.
	imageConfigMap(prodImageConfig)
}

func logImageConfig(cc []imageConfig) {
	log.Printf("Image configuration:")
	for _, c := range cc {
		log.Printf("\t%s: image=%s\tcount=%d", c.Hostname, c.Image, c.MinCount)
	}
}
