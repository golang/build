// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cloud

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	mrand "math/rand"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/service/ec2"
)

// FakeAWSClient provides a fake AWS Client used to test the AWS client
// functionality.
type FakeAWSClient struct {
	mu            sync.RWMutex
	instances     map[string]*Instance
	instanceTypes []*InstanceType
	serviceQuotas map[serviceQuotaKey]int64
}

// serviceQuotaKey should be used as the key in the serviceQuotas map.
type serviceQuotaKey struct {
	code    string
	service string
}

// NewFakeAWSClient crates a fake AWS client.
func NewFakeAWSClient() *FakeAWSClient {
	return &FakeAWSClient{
		instances: make(map[string]*Instance),
		instanceTypes: []*InstanceType{
			{"ab.large", 10},
			{"ab.xlarge", 20},
			{"ab.small", 30},
		},
		serviceQuotas: map[serviceQuotaKey]int64{
			{QuotaCodeCPUOnDemand, QuotaServiceEC2}: 384,
		},
	}
}

// Instance returns the `Instance` record for the requested instance. The instance record will
// return records for recently terminated instances. If an instance is not found an error will
// be returned.
func (f *FakeAWSClient) Instance(ctx context.Context, instID string) (*Instance, error) {
	if ctx == nil || instID == "" {
		return nil, errors.New("invalid params")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	inst, ok := f.instances[instID]
	if !ok {
		return nil, errors.New("instance not found")
	}
	return copyInstance(inst), nil
}

// Instances retrieves all EC2 instances in a region which have not been terminated or stopped.
func (f *FakeAWSClient) RunningInstances(ctx context.Context) ([]*Instance, error) {
	if ctx == nil {
		return nil, errors.New("invalid params")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	instances := make([]*Instance, 0, len(f.instances))
	for _, inst := range f.instances {
		if inst.State != ec2.InstanceStateNameRunning && inst.State != ec2.InstanceStateNamePending {
			continue
		}
		instances = append(instances, copyInstance(inst))
	}
	return instances, nil
}

// InstanceTypesARM retrieves all EC2 instance types in a region which support the ARM64 architecture.
func (f *FakeAWSClient) InstanceTypesARM(ctx context.Context) ([]*InstanceType, error) {
	if ctx == nil {
		return nil, errors.New("invalid params")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	instanceTypes := make([]*InstanceType, 0, len(f.instanceTypes))
	for _, it := range f.instanceTypes {
		instanceTypes = append(instanceTypes, &InstanceType{it.Type, it.CPU})
	}
	return instanceTypes, nil
}

// Quota retrieves the requested service quota for the service.
func (f *FakeAWSClient) Quota(ctx context.Context, service, code string) (int64, error) {
	if ctx == nil || service == "" || code == "" {
		return 0, errors.New("invalid params")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	v, ok := f.serviceQuotas[serviceQuotaKey{code, service}]
	if !ok {
		return 0, errors.New("service quota not found")
	}
	return v, nil
}

// CreateInstance creates an EC2 VM instance.
func (f *FakeAWSClient) CreateInstance(ctx context.Context, config *EC2VMConfiguration) (*Instance, error) {
	if ctx == nil || config == nil {
		return nil, errors.New("invalid params")
	}
	if config.ImageID == "" {
		return nil, errors.New("invalid Image ID")
	}
	if config.Type == "" {
		return nil, errors.New("invalid Type")
	}
	if config.Zone == "" {
		return nil, errors.New("invalid Zone")
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	inst := &Instance{
		CPUCount:          4,
		CreatedAt:         time.Now(),
		Description:       config.Description,
		ID:                fmt.Sprintf("instance-%s", randHex(10)),
		IPAddressExternal: randIPv4(),
		IPAddressInternal: randIPv4(),
		ImageID:           config.ImageID,
		Name:              config.Name,
		SSHKeyID:          config.SSHKeyID,
		SecurityGroups:    config.SecurityGroups,
		State:             ec2.InstanceStateNameRunning,
		Tags:              make(map[string]string),
		Type:              config.Type,
		Zone:              config.Zone,
	}
	for k, v := range config.Tags {
		inst.Tags[k] = v
	}
	f.instances[inst.ID] = inst
	return copyInstance(inst), nil
}

// DestroyInstances terminates EC2 VM instances.
func (f *FakeAWSClient) DestroyInstances(ctx context.Context, instIDs ...string) error {
	if ctx == nil || len(instIDs) == 0 {
		return errors.New("invalid params")
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, id := range instIDs {
		inst, ok := f.instances[id]
		if !ok {
			return errors.New("instance not found")
		}
		inst.State = ec2.InstanceStateNameTerminated
	}
	return nil
}

// WaitUntilInstanceRunning returns when an instance has transitioned into the running state.
func (f *FakeAWSClient) WaitUntilInstanceRunning(ctx context.Context, instID string) error {
	if ctx == nil || instID == "" {
		return errors.New("invalid params")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()

	inst, ok := f.instances[instID]
	if !ok {
		return errors.New("instance not found")
	}
	if inst.State != ec2.InstanceStateNameRunning {
		return errors.New("timed out waiting for instance to enter running state")
	}
	return nil
}

// copyInstance copies the contents of a pointer to an instance and returns a newly created
// instance with the same data as the original instance.
func copyInstance(inst *Instance) *Instance {
	i := &Instance{
		CPUCount:          inst.CPUCount,
		CreatedAt:         inst.CreatedAt,
		Description:       inst.Description,
		ID:                inst.ID,
		IPAddressExternal: inst.IPAddressExternal,
		IPAddressInternal: inst.IPAddressInternal,
		ImageID:           inst.ImageID,
		Name:              inst.Name,
		SSHKeyID:          inst.SSHKeyID,
		SecurityGroups:    inst.SecurityGroups,
		State:             inst.State,
		Tags:              make(map[string]string),
		Type:              inst.Type,
		Zone:              inst.Zone,
	}
	for k, v := range inst.Tags {
		i.Tags[k] = v
	}
	return i
}

// randHex creates a random hex string of length n.
func randHex(n int) string {
	buf := make([]byte, n/2+1)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%x", buf)[:n]
}

// randIPv4 creates a random IPv4 address.
func randIPv4() string {
	return fmt.Sprintf("%d.%d.%d.%d", mrand.Intn(255), mrand.Intn(255), mrand.Intn(255), mrand.Intn(255))
}
