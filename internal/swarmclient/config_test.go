// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package swarmclient

import (
	"context"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestListSwarmingBots(t *testing.T) {
	contents, err := os.ReadFile("testdata/bb-sample.cfg")
	if err != nil {
		t.Fatalf("os.ReadFile() = nil, %s", err)
	}
	ctx := context.Background()
	client := NewMemoryConfigClient(ctx, []*ConfigEntry{
		&ConfigEntry{"cr-buildbucket.cfg", contents},
	})
	bots, err := client.ListSwarmingBots(ctx)
	if err != nil {
		t.Fatalf("ListSwarmingBots() = nil, %s", err)
	}
	wantLen := 21
	if len(bots) != wantLen {
		t.Errorf("len(bots) = %d; want %d", len(bots), wantLen)
	}
	bot := bots[0]
	if bot.BucketName != "ci" {
		t.Errorf("bot.BucketName = %q, want %q", bot.BucketName, "ci")
	}
	wantDimensions := []string{"cpu:x86-64", "os:Linux", "pool:luci.golang.ci"}
	if diff := cmp.Diff(bot.Dimensions, wantDimensions); diff != "" {
		t.Errorf("bot.Dimensions mismatch (-want +got): \n%s", diff)
	}
	wantHost := "chromium-swarm.appspot.com"
	if bot.Host != wantHost {
		t.Errorf("bot.Host = %q, want %q", bot.Host, wantHost)
	}
	wantName := "go1.20-darwin-amd64"
	if bot.Name != wantName {
		t.Errorf("bot.Name = %q, want %q", bot.Name, wantName)
	}
}
