// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package swarmclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	bbpb "go.chromium.org/luci/buildbucket/proto"
	luciconfig "go.chromium.org/luci/config"
	"go.chromium.org/luci/config/cfgclient"
	"go.chromium.org/luci/config/impl/memory"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/prototext"
)

// ConfigClient is a client for the LUCI configuration service.
type ConfigClient struct {
	config luciconfig.Interface
}

// NewConfigClient creates a client to access the LUCI configuration service.
func NewConfigClient(ctx context.Context) (*ConfigClient, error) {
	cc, err := cfgclient.New(ctx, cfgclient.Options{
		ServiceHost: "luci-config.appspot.com",
		ClientFactory: func(context.Context) (*http.Client, error) {
			return http.DefaultClient, nil
		},
		GetPerRPCCredsFn: func(context.Context) (credentials.PerRPCCredentials, error) {
			return nil, errors.New("GetPerRPCCredsFn unimplemented")
		},
	})
	if err != nil {
		return nil, fmt.Errorf("cfgclient.New() = nil, %w", err)
	}
	return &ConfigClient{config: cc}, nil
}

// ConfigEntry represents a configuration file in the configuration directory. It
// should only be used for testing.
type ConfigEntry struct {
	Filename string // name of the file.
	Contents []byte // contents of the configuration file.
}

// NewMemoryConfigClient creates a config client where the configuration files are stored in memory.
// See https://go.chromium.org/luci/config/impl/filesystem for the expected directory layout.
// This should only be used while testing.
func NewMemoryConfigClient(ctx context.Context, files []*ConfigEntry) *ConfigClient {
	f := make(map[string]string)
	for _, entry := range files {
		f[entry.Filename] = string(entry.Contents)
	}
	cc := memory.New(map[luciconfig.Set]memory.Files{
		luciconfig.Set("projects/golang"): f,
	})
	return &ConfigClient{config: cc}
}

// SwarmingBot contains the metadata for a LUCI swarming bot.
type SwarmingBot struct {
	// BucketName is the name of the bucket the builder is defined in.
	BucketName string
	// Dimensions contains attributes about the builder. Form is in
	// <key>:<value> or <time>:<key>:<value>
	Dimensions []string
	// Host is the hostname of the swarming instance.
	Host string
	// Name of the builder.
	Name string
}

// ListSwarmingBots lists all of the swarming bots in the golang project defined in the
// cr-buildbucket.cfg configuration file.
func (cc *ConfigClient) ListSwarmingBots(ctx context.Context) ([]*SwarmingBot, error) {
	bb, err := cc.config.GetConfig(ctx, luciconfig.Set("projects/golang"), "cr-buildbucket.cfg", false)
	if err != nil {
		return nil, fmt.Errorf("client.GetConfig() = nil, %s", err)
	}
	bbc := &bbpb.BuildbucketCfg{}
	if err := prototext.Unmarshal([]byte(bb.Content), bbc); err != nil {
		return nil, fmt.Errorf("prototext.Unmarshal() = %w", err)
	}
	var bots []*SwarmingBot
	for _, bucket := range bbc.GetBuckets() {
		for _, builder := range bucket.GetSwarming().GetBuilders() {
			bots = append(bots, &SwarmingBot{
				BucketName: bucket.GetName(),
				Dimensions: builder.GetDimensions(),
				Host:       builder.GetSwarmingHost(),
				Name:       builder.GetName(),
			})
		}
	}
	return bots, nil
}
