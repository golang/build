// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"log"

	"golang.org/x/build/internal/macservice"
)

// Interface matching macservice.Client to use for test mocking.
type macServiceClient interface {
	Lease(macservice.LeaseRequest) (macservice.LeaseResponse, error)
	Renew(macservice.RenewRequest) (macservice.RenewResponse, error)
	Vacate(macservice.VacateRequest) error
	Find(macservice.FindRequest) (macservice.FindResponse, error)
}

// readOnlyMacServiceClient wraps a macServiceClient, logging instead of
// performing mutating actions. Used for dry run mode.
type readOnlyMacServiceClient struct {
	mc macServiceClient
}

func (r readOnlyMacServiceClient) Lease(req macservice.LeaseRequest) (macservice.LeaseResponse, error) {
	log.Printf("DRY RUN: Create lease with image %s", req.InstanceSpecification.DiskSelection.ImageHashes.BootSHA256)
	return macservice.LeaseResponse{
		PendingLease: macservice.Lease{LeaseID: "dry-run-lease"},
	}, nil
}

func (r readOnlyMacServiceClient) Renew(req macservice.RenewRequest) (macservice.RenewResponse, error) {
	log.Printf("DRY RUN: Renew lease %s with duration %s", req.LeaseID, req.Duration)
	return macservice.RenewResponse{}, nil // Perhaps fake RenewResponse.Expires?
}

func (r readOnlyMacServiceClient) Vacate(req macservice.VacateRequest) error {
	log.Printf("DRY RUN: Vacate lease %s", req.LeaseID)
	return nil
}

func (r readOnlyMacServiceClient) Find(req macservice.FindRequest) (macservice.FindResponse, error) {
	return r.mc.Find(req)
}
