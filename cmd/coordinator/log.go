// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"log"
	"time"

	"google.golang.org/cloud/datastore"

	"golang.org/x/net/context"
)

// Process is a datastore record about the lifetime of a coordinator process.
//
// Example GQL query:
// SELECT * From Process where LastHeartbeat > datetime("2016-01-01T00:00:00Z")
type Process struct {
	ID            string
	Start         time.Time
	LastHeartbeat time.Time

	// TODO: version, who deployed, CoreOS version, Docker version,
	// GCE instance type?
}

func updateInstanceRecord() {
	if dsClient == nil {
		return
	}
	ctx := context.Background()
	for {
		key := datastore.NewKey(ctx, "Process", processID, 0, nil)
		_, err := dsClient.Put(ctx, key, &Process{
			ID:            processID,
			Start:         processStartTime,
			LastHeartbeat: time.Now(),
		})
		if err != nil {
			log.Printf("datastore Process Put: %v", err)
		}
		time.Sleep(30 * time.Second)
	}
}
