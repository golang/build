// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package pool

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/build/internal/cloud"
	"golang.org/x/build/internal/coordinator/pool/queue"
)

// entry contains the resource usage of an instance as well as
// identifying information.
type entry struct {
	createdAt    time.Time
	instanceID   string
	instanceName string
	instanceType string
	vCPUCount    int64
	quota        *queue.Item
}

// ledger contains a record of the instances and their resource
// consumption. Before an instance is created, a call to the ledger
// will ensure that there are available resources for the new instance.
type ledger struct {
	mu sync.RWMutex
	// cpuQueue is the queue for on-demand vCPU VMs created on EC2.
	cpuQueue *queue.Quota
	// entries contains a mapping of instance name to entries for each instance
	// that has resources allocated to it.
	entries map[string]*entry
	// types contains a mapping of instance type names to instance types for each
	// ARM64 EC2 instance.
	types map[string]*cloud.InstanceType
}

// newLedger creates a new ledger.
func newLedger() *ledger {
	l := &ledger{
		entries:  make(map[string]*entry),
		cpuQueue: queue.NewQuota(),
		types:    make(map[string]*cloud.InstanceType),
	}
	return l
}

// ReserveResources attempts to reserve the resources required for an instance to be created.
// It will attempt to reserve the resources that an instance type would require. This will
// attempt to reserve the resources until the context deadline is reached.
func (l *ledger) ReserveResources(ctx context.Context, instName, vmType string, si *queue.SchedItem) error {
	instType, err := l.PrepareReservationRequest(instName, vmType)
	if err != nil {
		return err
	}

	// should never happen
	if instType.CPU <= 0 {
		return fmt.Errorf("invalid allocation requested: %d", instType.CPU)
	}
	item := l.cpuQueue.Enqueue(int(instType.CPU), si)
	if err := item.Await(ctx); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[instName]
	if ok {
		e.vCPUCount = instType.CPU
	} else {
		l.entries[instName] = &entry{
			instanceName: instName,
			vCPUCount:    instType.CPU,
			instanceType: instType.Type,
			quota:        item,
		}
	}
	return nil
}

// PrepareReservationRequest ensures all the preconditions necessary for a reservation request are
// met. If the conditions are met then an instance type for the requested VM type is returned. If
// not an error is returned.
func (l *ledger) PrepareReservationRequest(instName, vmType string) (*cloud.InstanceType, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	instType, ok := l.types[vmType]
	if !ok {
		return nil, fmt.Errorf("unknown EC2 vm type: %s", vmType)
	}
	_, ok = l.entries[instName]
	if ok {
		return nil, fmt.Errorf("quota has already been allocated for %s of type %s", instName, vmType)
	}
	return instType, nil
}

// releaseResources deletes the entry associated with an instance. The resources associated with the
// instance will also be released. An error is returned if the instance entry is not found.
// Lock l.mu must be held by the caller.
func (l *ledger) releaseResources(instName string) error {
	e, ok := l.entries[instName]
	if !ok {
		return fmt.Errorf("instance not found for releasing quota: %s", instName)
	}
	e.quota.ReturnQuota()
	return nil
}

// UpdateReservation updates the entry for an instance with the id value for that instance. If
// an entry for the instance does not exist then an error will be returned. Another mechanism should
// be used to manage untracked instances. Updating the reservation acts as a signal that the instance
// has actually been created since the instance ID is known.
func (l *ledger) UpdateReservation(instName, instID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[instName]
	if !ok {
		return fmt.Errorf("unable to update reservation: instance not found %s", instName)
	}
	e.createdAt = time.Now()
	e.instanceID = instID
	return nil
}

// Remove releases any reserved resources for an instance and deletes the associated entry.
// An error is returned if and entry does not exist for the instance.
func (l *ledger) Remove(instName string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.releaseResources(instName); err != nil {
		return fmt.Errorf("unable to remove instance: %w", err)
	}
	delete(l.entries, instName)
	return nil
}

// InstanceID retrieves the instance ID for an instance by looking up the instance name.
// If an instance is not found, an empty string is returned.
func (l *ledger) InstanceID(instName string) string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	e, ok := l.entries[instName]
	if !ok {
		return ""
	}
	return e.instanceID
}

// SetCPULimit sets the vCPU limit used to determine if a CPU allocation would
// cross the threshold for available CPU for on-demand instances.
func (l *ledger) SetCPULimit(numCPU int64) {
	l.cpuQueue.UpdateLimit(int(numCPU))
}

// UpdateInstanceTypes updates the map of instance types used to map instance
// type to the resources required for the instance.
func (l *ledger) UpdateInstanceTypes(types []*cloud.InstanceType) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, it := range types {
		l.types[it.Type] = it
	}
}

// resources contains the current limit and usage of instance related resources.
type resources struct {
	// InstCount is the count of how many on-demand instances are tracked in the ledger.
	InstCount int64
	// CPUUsed is a count of the vCPU's for on-demand instances are currently allocated in the ledger.
	CPUUsed int64
	// CPULimit is the limit of how many vCPU's for on-demand instances can be allocated.
	CPULimit int64
}

// Resources retrieves the resource usage and limits for instances in the
// store.
func (l *ledger) Resources() *resources {
	l.mu.RLock()
	defer l.mu.RUnlock()

	usage := l.cpuQueue.Quotas()
	return &resources{
		InstCount: int64(len(l.entries)),
		CPUUsed:   int64(usage.Used),
		CPULimit:  int64(usage.Limit),
	}
}

// ResourceTime give a ResourceTime entry for each active instance.
// The resource time slice is storted by creation time.
func (l *ledger) ResourceTime() []ResourceTime {
	l.mu.RLock()
	defer l.mu.RUnlock()

	ret := make([]ResourceTime, 0, len(l.entries))
	for name, data := range l.entries {
		ret = append(ret, ResourceTime{
			Name:     name,
			Creation: data.createdAt,
		})
	}
	sort.Sort(ByCreationTime(ret))
	return ret
}
