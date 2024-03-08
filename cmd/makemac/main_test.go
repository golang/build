// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	spb "go.chromium.org/luci/swarming/proto/api_v2"
	"golang.org/x/build/internal/macservice"
	"golang.org/x/build/internal/secret"
)

func init() {
	secret.InitFlagSupport(context.Background())
}

// recordMacServiceClient is a macserviceClient that records mutating requests.
type recordMacServiceClient struct {
	lease  []macservice.LeaseRequest
	renew  []macservice.RenewRequest
	vacate []macservice.VacateRequest
}

func (r *recordMacServiceClient) Lease(req macservice.LeaseRequest) (macservice.LeaseResponse, error) {
	r.lease = append(r.lease, req)
	return macservice.LeaseResponse{}, nil // Perhaps fake LeaseResponse.PendingLease.LeaseID?
}

func (r *recordMacServiceClient) Renew(req macservice.RenewRequest) (macservice.RenewResponse, error) {
	r.renew = append(r.renew, req)
	return macservice.RenewResponse{}, nil // Perhaps fake RenewResponse.Expires?
}

func (r *recordMacServiceClient) Vacate(req macservice.VacateRequest) error {
	r.vacate = append(r.vacate, req)
	return nil
}

func (r *recordMacServiceClient) Find(req macservice.FindRequest) (macservice.FindResponse, error) {
	return macservice.FindResponse{}, fmt.Errorf("unimplemented")
}

func TestHandleMissingBots(t *testing.T) {
	// Test leases:
	// * "healthy" connected to LUCI, and is healthy.
	// * "dead" connected to LUCI, but later died.
	// * "newBooting" never connected to LUCI, but was just created 5min ago.
	// * "neverBooted" never connected to LUCI, and was created 5hr ago.
	// * "neverBootedUnmanaged" never connected to LUCI, and was created 5hr ago, but is not managed by makemac.
	//
	// handleMissingBots should vacate neverBooted and none of the others.
	bots := map[string]*spb.BotInfo{
		"healthy": {BotId: "healthy"},
		"dead":    {BotId: "dead", IsDead: true},
	}
	leases := map[string]macservice.Instance{
		"healthy": {
			Lease: macservice.Lease{
				LeaseID:             "healthy",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				Expires:             time.Now().Add(createExpirationDuration - 2*time.Hour),
			},
		},
		"newBooting": {
			Lease: macservice.Lease{
				LeaseID:             "newBooting",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				Expires:             time.Now().Add(createExpirationDuration - 5*time.Minute),
			},
		},
		"neverBooted": {
			Lease: macservice.Lease{
				LeaseID:             "neverBooted",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				Expires:             time.Now().Add(createExpirationDuration - 2*time.Hour),
			},
		},
		"neverBootedUnmanaged": {
			Lease: macservice.Lease{
				LeaseID:             "neverBootedUnmanaged",
				VMResourceNamespace: macservice.Namespace{ProjectName: "other"},
				Expires:             time.Now().Add(createExpirationDuration - 2*time.Hour),
			},
		},
	}

	var mc recordMacServiceClient
	handleMissingBots(&mc, bots, leases)

	got := mc.vacate
	want := []macservice.VacateRequest{{LeaseID: "neverBooted"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Vacated leases mismatch (-want +got):\n%s", diff)
	}

	if _, ok := leases["neverBooted"]; ok {
		t.Errorf("neverBooted present in leases, want deleted")
	}
}

func TestHandleDeadBots(t *testing.T) {
	// Test leases:
	// * "healthy" connected to LUCI, and is healthy.
	// * "dead" connected to LUCI, but later died, and the lease is gone from MacService.
	// * "deadLeasePresent" connected to LUCI, but later died, and the lease is still present on MacService.
	// * "deadLeasePresentUnmanaged" connected to LUCI, but later died, and the lease is still present on MacService, but is not managed by makemac.
	// * "neverBooted" never connected to LUCI, and was created 5hr ago.
	//
	// handleDeadBots should vacate deadLeasePresent and none of the others.
	bots := map[string]*spb.BotInfo{
		"healthy":          {BotId: "healthy"},
		"dead":             {BotId: "dead", IsDead: true},
		"deadLeasePresent": {BotId: "deadLeasePresent", IsDead: true},
	}
	leases := map[string]macservice.Instance{
		"healthy": {
			Lease: macservice.Lease{
				LeaseID:             "healthy",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				Expires:             time.Now().Add(createExpirationDuration - 2*time.Hour),
			},
		},
		"deadLeasePresent": {
			Lease: macservice.Lease{
				LeaseID:             "deadLeasePresent",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				// Lease created 5 minutes ago. Doesn't matter;
				// new lease checked don't apply here. See
				// comment in handleDeadBots.
				Expires: time.Now().Add(createExpirationDuration - 5*time.Minute),
			},
		},
		"deadLeasePresentUnmanaged": {
			Lease: macservice.Lease{
				LeaseID:             "deadLeasePresentUnmanaged",
				VMResourceNamespace: macservice.Namespace{ProjectName: "other"},
				Expires:             time.Now().Add(createExpirationDuration - 5*time.Minute),
			},
		},
		"neverBooted": {
			Lease: macservice.Lease{
				LeaseID:             "neverBooted",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				Expires:             time.Now().Add(createExpirationDuration - 2*time.Hour),
			},
		},
	}

	var mc recordMacServiceClient
	handleDeadBots(&mc, bots, leases)

	got := mc.vacate
	want := []macservice.VacateRequest{{LeaseID: "deadLeasePresent"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Vacated leases mismatch (-want +got):\n%s", diff)
	}

	if _, ok := leases["deadLeasePresent"]; ok {
		t.Errorf("deadLeasePresent present in leases, want deleted")
	}
}

func TestRenewLeases(t *testing.T) {
	// Test leases:
	// * "new" was created <1hr ago.
	// * "standard" was created >1hr ago.
	// * "unmanaged" was created >1hr ago, but is not managed by makemac.
	//
	// renewLeases should renew "standard" and none of the others.
	leases := map[string]macservice.Instance{
		"new": {
			Lease: macservice.Lease{
				LeaseID:             "new",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				Expires:             time.Now().Add(createExpirationDuration - 5*time.Minute),
			},
		},
		"standard": {
			Lease: macservice.Lease{
				LeaseID:             "standard",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
				Expires:             time.Now().Add(renewExpirationDuration - 5*time.Minute),
			},
		},
		"unmanaged": {
			Lease: macservice.Lease{
				LeaseID:             "unmanaged",
				VMResourceNamespace: macservice.Namespace{ProjectName: "other"},
				Expires:             time.Now().Add(renewExpirationDuration - 5*time.Minute),
			},
		},
	}

	var mc recordMacServiceClient
	renewLeases(&mc, leases)

	got := mc.renew
	want := []macservice.RenewRequest{{LeaseID: "standard", Duration: renewExpirationDurationString}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Renewed leases mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleObsoleteLeases(t *testing.T) {
	// Test leases:
	// * "active" uses image "active-image"
	// * "obsolete" uses image "obsolete-image"
	// * "unmanaged" uses image "obsolete-image", but is not managed by makemac.
	//
	// handleObsoleteLeases should vacate "obsolute" and none of the others.
	config := []imageConfig{
		{
			Hostname: "active",
			Cert:     "dummy-cert",
			Key:      "dummy-key",
			Image:    "active-image",
			MinCount: 1,
		},
	}
	leases := map[string]macservice.Instance{
		"active": {
			Lease: macservice.Lease{
				LeaseID:             "active",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
			},
			InstanceSpecification: macservice.InstanceSpecification{
				DiskSelection: macservice.DiskSelection{
					ImageHashes: macservice.ImageHashes{
						BootSHA256: "active-image",
					},
				},
			},
		},
		"obsolete": {
			Lease: macservice.Lease{
				LeaseID:             "obsolete",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
			},
			InstanceSpecification: macservice.InstanceSpecification{
				DiskSelection: macservice.DiskSelection{
					ImageHashes: macservice.ImageHashes{
						BootSHA256: "obsolete-image",
					},
				},
			},
		},
		"unmanaged": {
			Lease: macservice.Lease{
				LeaseID:             "obsolete",
				VMResourceNamespace: macservice.Namespace{ProjectName: "other"},
			},
			InstanceSpecification: macservice.InstanceSpecification{
				DiskSelection: macservice.DiskSelection{
					ImageHashes: macservice.ImageHashes{
						BootSHA256: "obsolete-image",
					},
				},
			},
		},
	}

	var mc recordMacServiceClient
	handleObsoleteLeases(&mc, config, leases)

	got := mc.vacate
	want := []macservice.VacateRequest{{LeaseID: "obsolete"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Vacated leases mismatch (-want +got):\n%s", diff)
	}
}

func TestAddNewLeases(t *testing.T) {
	// Test leases:
	// * "image-a-1" uses image "image-a"
	// * "unmanaged" uses image "image-a", but is not managed by makemac.
	//
	// Test images:
	// * "image-a" wants 2 instances.
	// * "image-b" wants 2 instances.
	//
	// addNewLeases should create 1 "image-a" instance (ignoring
	// "unmanaged") and 2 "image-b" instances.
	config := []imageConfig{
		{
			Hostname: "a",
			Cert:     "dummy-cert",
			Key:      "dummy-key",
			Image:    "image-a",
			MinCount: 2,
		},
		{
			Hostname: "b",
			Cert:     "dummy-cert",
			Key:      "dummy-key",
			Image:    "image-b",
			MinCount: 2,
		},
	}
	leases := map[string]macservice.Instance{
		"image-a-1": {
			Lease: macservice.Lease{
				LeaseID:             "image-a-1",
				VMResourceNamespace: macservice.Namespace{ProjectName: managedProject},
			},
			InstanceSpecification: macservice.InstanceSpecification{
				DiskSelection: macservice.DiskSelection{
					ImageHashes: macservice.ImageHashes{
						BootSHA256: "image-a",
					},
				},
			},
		},
		"unmanaged": {
			Lease: macservice.Lease{
				LeaseID:             "unmanaged",
				VMResourceNamespace: macservice.Namespace{ProjectName: "other"},
			},
			InstanceSpecification: macservice.InstanceSpecification{
				DiskSelection: macservice.DiskSelection{
					ImageHashes: macservice.ImageHashes{
						BootSHA256: "image-a",
					},
				},
			},
		},
	}

	var mc recordMacServiceClient
	addNewLeases(&mc, config, leases)

	leaseA, err := makeLeaseRequest(&config[0])
	if err != nil {
		t.Fatalf("makeLeaseRequest(a) got err %v want nil", err)
	}
	leaseB, err := makeLeaseRequest(&config[1])
	if err != nil {
		t.Fatalf("makeLeaseRequest(b) got err %v want nil", err)
	}

	got := mc.lease
	want := []macservice.LeaseRequest{leaseA, leaseB, leaseB}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Lease request mismatch (-want +got):\n%s", diff)
	}
}
