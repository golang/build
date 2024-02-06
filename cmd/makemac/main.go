// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command makemac ensures that MacService instances continue running.
// Currently, it simply renews any existing leases.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"golang.org/x/build/internal/macservice"
	"golang.org/x/build/internal/secret"
)

var (
	apiKey = secret.Flag("api-key", "MacService API key")
	dryRun = flag.Bool("dry-run", false, "Print the actions that would be taken without actually performing them")
	period = flag.Duration("period", 2*time.Hour, "How often to check leases. As a special case, -period=0 checks exactly once and then exits")
)

const renewDuration = "86400s" // 24h

func main() {
	secret.InitFlagSupport(context.Background())
	flag.Parse()

	c := macservice.NewClient(*apiKey)

	// Always check once at startup.
	checkAndRenewLeases(c)

	if *period == 0 {
		// User only wants a single check. We're done.
		return
	}

	t := time.NewTicker(*period)
	for range t.C {
		checkAndRenewLeases(c)
	}
}

func checkAndRenewLeases(c *macservice.Client) {
	log.Printf("Renewing leases...")

	resp, err := c.Find(macservice.FindRequest{
		VMResourceNamespace: macservice.Namespace{
			CustomerName: "golang",
		},
	})
	if err != nil {
		log.Printf("Error finding leases: %v", err)
		return
	}

	if len(resp.Instances) == 0 {
		log.Printf("No leases found")
		return
	}

	for _, i := range resp.Instances {
		log.Printf("Renewing lease ID: %s; currently expires: %v...", i.Lease.LeaseID, i.Lease.Expires)
		if *dryRun {
			continue
		}

		rr, err := c.Renew(macservice.RenewRequest{
			LeaseID:  i.Lease.LeaseID,
			Duration: renewDuration,
		})
		if err == nil {
			// Extra spaces to make fields line up with the message above.
			log.Printf("Renewed  lease ID: %s; now       expires: %v", i.Lease.LeaseID, rr.Expires)
		} else {
			log.Printf("Error renewing lease ID: %s: %v", i.Lease.LeaseID, err)
		}
	}
}
