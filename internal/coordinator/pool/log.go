// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package pool

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"cloud.google.com/go/datastore"
	"golang.org/x/build/internal/spanlog"
	"golang.org/x/build/types"
)

// EventTimeLogger is the logging interface used to log
// an event at a point in time.
type EventTimeLogger interface {
	LogEventTime(event string, optText ...string)
}

// Logger is the logging interface used within the coordinator.
// It can both log a message at a point in time, as well
// as log a span (something having a start and end time, as well as
// a final success status).
type Logger interface {
	EventTimeLogger // point in time
	spanlog.Logger  // action spanning time
}

type Process struct {
	processID        string
	processStartTime time.Time
}

var (
	process *Process
	once    sync.Once
)

// SetProcessMetadata sets metadata about the process. This should be called before any
// event logging operations.
func SetProcessMetadata(id string, startTime time.Time) *Process {
	once.Do(func() {
		process = &Process{
			processID:        id,
			processStartTime: startTime,
		}
	})
	return process
}

// CoordinatorProcess returns the package level process logger.
func CoordinatorProcess() *Process {
	return process
}

// ProcessRecord is a datastore record about the lifetime of a coordinator process.
//
// Example GQL query:
// SELECT * From Process where LastHeartbeat > datetime("2016-01-01T00:00:00Z")
type ProcessRecord struct {
	ID            string
	Start         time.Time
	LastHeartbeat time.Time

	// TODO: version, who deployed, CoreOS version, Docker version,
	// GCE instance type?
}

func (p *Process) UpdateInstanceRecord() {
	dsClient := NewGCEConfiguration().DSClient()
	if dsClient == nil {
		return
	}
	ctx := context.Background()
	for {
		key := datastore.NameKey("Process", p.processID, nil)
		_, err := dsClient.Put(ctx, key, &ProcessRecord{
			ID:            p.processID,
			Start:         p.processStartTime,
			LastHeartbeat: time.Now(),
		})
		if err != nil {
			log.Printf("datastore Process Put: %v", err)
		}
		time.Sleep(30 * time.Second)
	}
}

func (p *Process) PutBuildRecord(br *types.BuildRecord) {
	dsClient := NewGCEConfiguration().DSClient()
	if dsClient == nil {
		return
	}
	ctx := context.Background()
	key := datastore.NameKey("Build", br.ID, nil)
	if _, err := dsClient.Put(ctx, key, br); err != nil {
		log.Printf("datastore Build Put: %v", err)
	}
}

func (p *Process) PutSpanRecord(sr *types.SpanRecord) {
	dsClient := NewGCEConfiguration().DSClient()
	if dsClient == nil {
		return
	}
	ctx := context.Background()
	key := datastore.NameKey("Span", fmt.Sprintf("%s-%v-%v", sr.BuildID, sr.StartTime.UnixNano(), sr.Event), nil)
	if _, err := dsClient.Put(ctx, key, sr); err != nil {
		log.Printf("datastore Span Put: %v", err)
	}
}
