// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/servicequotas"
	"github.com/google/go-cmp/cmp"
)

type awsClient interface {
	vmClient
	quotaClient
}

var _ awsClient = (*fakeEC2Client)(nil)

type fakeEC2Client struct {
	mu sync.RWMutex
	// instances map of instanceId -> *ec2.Instance
	instances     map[string]*ec2.Instance
	instanceTypes []*ec2.InstanceTypeInfo
	serviceQuota  map[string]float64
}

func newFakeAWSClient() *fakeEC2Client {
	return &fakeEC2Client{
		instances:     make(map[string]*ec2.Instance),
		instanceTypes: []*ec2.InstanceTypeInfo{},
		serviceQuota:  make(map[string]float64),
	}
}

// filterFunc represents a function used to filter out instances.
type filterFunc func(*ec2.Instance) bool

// createFilter returns filtering functions for a subset of `ec2.Filter`.
// The response in the function returned indicates whether the instance
// should be included.
func createFilter(f *ec2.Filter) filterFunc {
	if *f.Name == "instance-state-name" {
		states := aws.StringValueSlice(f.Values)
		return func(i *ec2.Instance) bool {
			for _, s := range states {
				if *i.State.Name == s {
					return true
				}
			}
			return false
		}
	}
	// return noop filter for unsupported filters
	return func(i *ec2.Instance) bool { return true }
}

// createFilters creates a filtering function for a subset of `ec2.Filter`.
// The response for the returned function indicates whether the instance
// should be included after all of the supplied filters have been evaluated.
func createFilters(fs []*ec2.Filter) filterFunc {
	if len(fs) == 0 {
		// return noop filter for unsupported filters
		return func(i *ec2.Instance) bool { return true }
	}
	filters := make([]filterFunc, 0, len(fs))
	for _, f := range fs {
		filters = append(filters, createFilter(f))
	}
	return func(i *ec2.Instance) bool {
		for _, fn := range filters {
			if !fn(i) {
				return false
			}
		}
		return true
	}
}

func (f *fakeEC2Client) DescribeInstancesPagesWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, fn func(*ec2.DescribeInstancesOutput, bool) bool, opt ...request.Option) error {
	if input == nil || fn == nil {
		return errors.New("invalid input")
	}
	filters := createFilters(input.Filters)
	f.mu.RLock()
	defer f.mu.RUnlock()
	insts := make([]*ec2.Instance, 0, len(f.instances))
	for _, inst := range f.instances {
		if !filters(inst) {
			continue
		}
		insts = append(insts, inst)
	}
	for it, inst := range insts {
		fn(&ec2.DescribeInstancesOutput{
			Reservations: []*ec2.Reservation{
				&ec2.Reservation{
					Instances: []*ec2.Instance{
						inst,
					},
				},
			},
		}, it == len(insts)-1)
	}
	return nil
}

func (f *fakeEC2Client) DescribeInstancesWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, opt ...request.Option) (*ec2.DescribeInstancesOutput, error) {
	if ctx == nil || input == nil || len(input.InstanceIds) == 0 {
		return nil, request.ErrInvalidParams{}
	}
	filters := createFilters(input.Filters)
	instances := make([]*ec2.Instance, 0, len(input.InstanceIds))
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, id := range aws.StringValueSlice(input.InstanceIds) {
		inst, ok := f.instances[id]
		if !ok {
			return nil, errors.New("instance not found")
		}
		if !filters(inst) {
			continue
		}
		instances = append(instances, inst)
	}
	return &ec2.DescribeInstancesOutput{
		Reservations: []*ec2.Reservation{
			&ec2.Reservation{
				Instances: instances,
			},
		},
	}, nil
}

func (f *fakeEC2Client) RunInstancesWithContext(ctx context.Context, input *ec2.RunInstancesInput, opts ...request.Option) (*ec2.Reservation, error) {
	if ctx == nil || input == nil {
		return nil, request.ErrInvalidParams{}
	}
	if input.ImageId == nil || aws.StringValue(input.ImageId) == "" ||
		input.InstanceType == nil || aws.StringValue(input.InstanceType) == "" ||
		input.MinCount == nil || aws.Int64Value(input.MinCount) == 0 ||
		input.Placement == nil || aws.StringValue(input.Placement.AvailabilityZone) == "" {
		return nil, errors.New("invalid instance configuration")
	}
	instCount := int(aws.Int64Value(input.MaxCount))
	instances := make([]*ec2.Instance, 0, instCount)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < instCount; i++ {
		inst := &ec2.Instance{
			CpuOptions: &ec2.CpuOptions{
				CoreCount: aws.Int64(4),
			},
			ImageId:          input.ImageId,
			InstanceType:     input.InstanceType,
			InstanceId:       aws.String(fmt.Sprintf("instance-%s", randHex(10))),
			Placement:        input.Placement,
			PrivateIpAddress: aws.String(randIPv4()),
			PublicIpAddress:  aws.String(randIPv4()),
			State: &ec2.InstanceState{
				Name: aws.String("running"),
			},
			Tags:           []*ec2.Tag{},
			KeyName:        input.KeyName,
			SecurityGroups: []*ec2.GroupIdentifier{},
			LaunchTime:     aws.Time(time.Now()),
		}
		for _, id := range input.SecurityGroups {
			inst.SecurityGroups = append(inst.SecurityGroups, &ec2.GroupIdentifier{
				GroupId: id,
			})
		}
		for _, tagSpec := range input.TagSpecifications {
			for _, tag := range tagSpec.Tags {
				inst.Tags = append(inst.Tags, tag)
			}
		}
		f.instances[*inst.InstanceId] = inst
		instances = append(instances, inst)
	}
	return &ec2.Reservation{
		Instances:     instances,
		ReservationId: aws.String(fmt.Sprintf("reservation-%s", randHex(10))),
	}, nil
}

func (f *fakeEC2Client) TerminateInstancesWithContext(ctx context.Context, input *ec2.TerminateInstancesInput, opts ...request.Option) (*ec2.TerminateInstancesOutput, error) {
	if ctx == nil || input == nil || len(input.InstanceIds) == 0 {
		return nil, request.ErrInvalidParams{}
	}
	isc := make([]*ec2.InstanceStateChange, 0, len(input.InstanceIds))
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range input.InstanceIds {
		if *id == "" {
			return nil, errors.New("invalid instance id")
		}
		var prevState string
		inst, ok := f.instances[*id]
		if !ok {
			return nil, errors.New("instance not found")
		}
		prevState = *inst.State.Name
		inst.State.Name = aws.String(ec2.InstanceStateNameTerminated)
		isc = append(isc, &ec2.InstanceStateChange{
			CurrentState: &ec2.InstanceState{
				Name: aws.String(prevState),
			},
			InstanceId: id,
			PreviousState: &ec2.InstanceState{
				Code: nil,
				Name: aws.String(ec2.InstanceStateNameTerminated),
			},
		})
	}
	return &ec2.TerminateInstancesOutput{
		TerminatingInstances: isc,
	}, nil
}

func (f *fakeEC2Client) WaitUntilInstanceRunningWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, opt ...request.WaiterOption) error {
	if ctx == nil || input == nil || len(input.InstanceIds) == 0 {
		return request.ErrInvalidParams{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range input.InstanceIds {
		inst, ok := f.instances[*id]
		if !ok {
			return fmt.Errorf("instance %s not found", *id)
		}
		inst.State = &ec2.InstanceState{
			Name: aws.String("running"),
		}
	}
	return nil
}

func (f *fakeEC2Client) DescribeInstanceTypesPagesWithContext(ctx context.Context, input *ec2.DescribeInstanceTypesInput, fn func(*ec2.DescribeInstanceTypesOutput, bool) bool, opt ...request.Option) error {
	if ctx == nil || input == nil || fn == nil {
		return errors.New("invalid input")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	for it, its := range f.instanceTypes {
		fn(&ec2.DescribeInstanceTypesOutput{
			InstanceTypes: []*ec2.InstanceTypeInfo{its},
		}, it == len(f.instanceTypes)-1)
	}
	return nil
}

func (f *fakeEC2Client) GetServiceQuota(input *servicequotas.GetServiceQuotaInput) (*servicequotas.GetServiceQuotaOutput, error) {
	if input == nil || input.QuotaCode == nil || input.ServiceCode == nil {
		return nil, request.ErrInvalidParams{}
	}
	v, ok := f.serviceQuota[aws.StringValue(input.ServiceCode)+"-"+aws.StringValue(input.QuotaCode)]
	if !ok {
		return nil, errors.New("quota not found")
	}
	return &servicequotas.GetServiceQuotaOutput{
		Quota: &servicequotas.ServiceQuota{
			Value: aws.Float64(v),
		},
	}, nil
}

type option func(*fakeEC2Client)

func WithServiceQuota(service, quota string, value float64) option {
	return func(c *fakeEC2Client) {
		c.serviceQuota[service+"-"+quota] = value
	}
}

func WithInstanceType(name, arch string, numCPU int64) option {
	return func(c *fakeEC2Client) {
		c.instanceTypes = append(c.instanceTypes, &ec2.InstanceTypeInfo{
			InstanceType: aws.String(name),
			ProcessorInfo: &ec2.ProcessorInfo{
				SupportedArchitectures: []*string{aws.String(arch)},
			},
			VCpuInfo: &ec2.VCpuInfo{
				DefaultVCpus: aws.Int64(numCPU),
			},
		})
	}
}

func fakeClient(opts ...option) *AWSClient {
	fc := newFakeAWSClient()
	for _, opt := range opts {
		opt(fc)
	}
	return &AWSClient{
		ec2Client:   fc,
		quotaClient: fc,
	}
}

func fakeClientWithInstances(t *testing.T, count int, opts ...option) (*AWSClient, []*Instance) {
	c := fakeClient(opts...)
	ctx := context.Background()
	insts := make([]*Instance, 0, count)
	for i := 0; i < count; i++ {
		inst, err := c.CreateInstance(ctx, randomVMConfig())
		if err != nil {
			t.Fatalf("unable to create instance: %s", err)
		}
		insts = append(insts, inst)
	}
	return c, insts
}

func randomVMConfig() *EC2VMConfiguration {
	return &EC2VMConfiguration{
		Description:    "description-" + randHex(4),
		ImageID:        "image-" + randHex(4),
		Name:           "name-" + randHex(4),
		SSHKeyID:       "ssh-key-id-" + randHex(4),
		SecurityGroups: []string{"sg-" + randHex(4)},
		Tags: map[string]string{
			"tag-key-" + randHex(4): "tag-value-" + randHex(4),
		},
		Type:     "type-" + randHex(4),
		UserData: "user-data-" + randHex(4),
		Zone:     "zone-" + randHex(4),
	}
}

func TestRunningInstances(t *testing.T) {
	t.Run("query-all-instances", func(t *testing.T) {
		c, wantInsts := fakeClientWithInstances(t, 4)
		gotInsts, gotErr := c.RunningInstances(context.Background())
		if gotErr != nil {
			t.Fatalf("Instances(ctx) = %+v, %s; want nil, nil", gotInsts, gotErr)
		}
		if len(gotInsts) != len(wantInsts) {
			t.Errorf("got instance count %d: want %d", len(gotInsts), len(wantInsts))
		}
	})
	t.Run("query-with-a-terminated-instance", func(t *testing.T) {
		ctx := context.Background()
		c, wantInsts := fakeClientWithInstances(t, 4)
		gotErr := c.DestroyInstances(ctx, wantInsts[0].ID)
		if gotErr != nil {
			t.Fatalf("unable to destroy instance: %s", gotErr)
		}
		gotInsts, gotErr := c.RunningInstances(ctx)
		if gotErr != nil {
			t.Fatalf("Instances(ctx) = %+v, %s; want nil, nil", gotInsts, gotErr)
		}
		if len(gotInsts) != len(wantInsts)-1 {
			t.Errorf("got instance count %d: want %d", len(gotInsts), len(wantInsts)-1)
		}
	})
}

func TestInstanceTypesARM(t *testing.T) {
	opts := []option{
		WithInstanceType("zz.large", "x86_64", 10),
		WithInstanceType("aa.xlarge", "arm64", 20),
	}

	t.Run("query-arm64-instances", func(t *testing.T) {
		c := fakeClient(opts...)
		gotInstTypes, gotErr := c.InstanceTypesARM(context.Background())
		if gotErr != nil {
			t.Fatalf("InstanceTypesArm(ctx) = %+v, %s; want nil, nil", gotInstTypes, gotErr)
		}
		if len(gotInstTypes) != 1 {
			t.Errorf("got instance type count %d: want %d", len(gotInstTypes), 1)
		}
	})
	t.Run("nil-request", func(t *testing.T) {
		c := fakeClient(opts...)
		gotInstTypes, gotErr := c.InstanceTypesARM(nil)
		if gotErr == nil {
			t.Fatalf("InstanceTypesArm(nil) = %+v, %s; want nil, error", gotInstTypes, gotErr)
		}
	})
}

func TestQuota(t *testing.T) {
	t.Run("on-demand-vcpu", func(t *testing.T) {
		wantQuota := int64(384)
		c := fakeClient(WithServiceQuota(QuotaServiceEC2, QuotaCodeCPUOnDemand, float64(wantQuota)))
		gotQuota, gotErr := c.Quota(context.Background(), QuotaServiceEC2, QuotaCodeCPUOnDemand)
		if gotErr != nil || wantQuota != gotQuota {
			t.Fatalf("Quota(ctx, %s, %s) = %+v, %s; want %d, nil", QuotaServiceEC2, QuotaCodeCPUOnDemand, gotQuota, gotErr, wantQuota)
		}
	})
	t.Run("nil-request", func(t *testing.T) {
		wantQuota := int64(384)
		c := fakeClient(WithServiceQuota(QuotaServiceEC2, QuotaCodeCPUOnDemand, float64(wantQuota)))
		gotQuota, gotErr := c.Quota(context.Background(), "", "")
		if gotErr == nil || gotQuota != 0 {
			t.Fatalf("Quota(ctx, %s, %s) = %+v, %s; want 0, error", QuotaServiceEC2, QuotaCodeCPUOnDemand, gotQuota, gotErr)
		}
	})
}

func TestInstance(t *testing.T) {
	t.Run("query-instance", func(t *testing.T) {
		c, wantInsts := fakeClientWithInstances(t, 1)
		wantInst := wantInsts[0]
		gotInst, gotErr := c.Instance(context.Background(), wantInst.ID)
		if gotErr != nil || gotInst == nil || gotInst.ID != wantInst.ID {
			t.Errorf("Instance(ctx, %s) = %+v, %s; want no error", wantInst.ID, gotInst, gotErr)
		}
	})
	t.Run("query-terminated-instance", func(t *testing.T) {
		c, wantInsts := fakeClientWithInstances(t, 1)
		wantInst := wantInsts[0]
		ctx := context.Background()
		gotErr := c.DestroyInstances(ctx, wantInst.ID)
		if gotErr != nil {
			t.Fatalf("unable to destroy instance: %s", gotErr)
		}
		gotInst, gotErr := c.Instance(ctx, wantInst.ID)
		if gotErr != nil || gotInst == nil || gotInst.ID != wantInst.ID {
			t.Errorf("Instance(ctx, %s) = %+v, %s; want no error", wantInst.ID, gotInst, gotErr)
		}
	})
}

func TestCreateInstance(t *testing.T) {
	ud := &EC2UserData{
		BuildletBinaryURL: "b-url",
		BuildletHostType:  "b-host-type",
		BuildletImageURL:  "b-image-url",
		BuildletName:      "b-name",
		Metadata: map[string]string{
			"tag-a": "value-b",
		},
		TLSCert:     "cert-a",
		TLSKey:      "key-a",
		TLSPassword: "pass-a",
	}
	config := &EC2VMConfiguration{
		Description:    "description-a",
		ImageID:        "my-image",
		Name:           "my-instance",
		SSHKeyID:       "my-key",
		SecurityGroups: []string{"test-key"},
		Tags: map[string]string{
			"tag-1": "value-1",
		},
		Type:     "xby.large",
		UserData: ud.EncodedString(),
		Zone:     "us-west-14",
	}
	c := fakeClient()
	gotInst, gotErr := c.CreateInstance(context.Background(), config)
	if gotErr != nil {
		t.Errorf("CreateInstance(ctx, %v) = %+v, %s; want no error", config, gotInst, gotErr)
	}
	if gotInst.Description != config.Description {
		t.Errorf("Instance.Description = %s; want %s", gotInst.Description, config.Description)
	}
	if gotInst.ImageID != config.ImageID {
		t.Errorf("Instance.ImageID = %s; want %s", gotInst.ImageID, config.ImageID)
	}
	if gotInst.Name != config.Name {
		t.Errorf("Instance.Name = %s; want %s", gotInst.Name, config.Name)
	}
	if gotInst.SSHKeyID != config.SSHKeyID {
		t.Errorf("Instance.SSHKeyID = %s; want %s", gotInst.SSHKeyID, config.SSHKeyID)
	}
	if !cmp.Equal(gotInst.SecurityGroups, config.SecurityGroups) {
		t.Errorf("Instance.SecruityGroups = %v; want %v", gotInst.SecurityGroups, config.SecurityGroups)
	}
	if !cmp.Equal(gotInst.Tags, config.Tags) {
		t.Errorf("Instance.Tags = %v want %v", gotInst.Tags, config.Tags)
	}
	if gotInst.Type != config.Type {
		t.Errorf("Instance.Type = %s; want %s", gotInst.Type, config.Type)
	}
	if gotInst.Zone != config.Zone {
		t.Errorf("Instance.Zone = %s; want %s", gotInst.Zone, config.Zone)
	}
}

func TestCreateInstanceError(t *testing.T) {
	testCases := []struct {
		desc     string
		vmConfig *EC2VMConfiguration
	}{
		{
			desc:     "missing-vmConfig",
			vmConfig: nil,
		},
		{
			desc: "missing-image-type",
			vmConfig: &EC2VMConfiguration{
				Type: "type-a",
				Zone: "eu-15",
			},
		},
		{
			desc: "missing-vm-type",
			vmConfig: &EC2VMConfiguration{
				ImageID: "ami-15",
				Zone:    "eu-15",
			},
		},
		{
			desc: "missing-zone",
			vmConfig: &EC2VMConfiguration{
				ImageID: "ami-15",
				Type:    "abc.large",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := fakeClient()
			gotInst, gotErr := c.CreateInstance(context.Background(), tc.vmConfig)
			if gotErr == nil || gotInst != nil {
				t.Errorf("CreateInstance(ctx, %+v) = %+v, %s; want error", tc.vmConfig, gotInst, gotErr)
			}
		})
	}
}

func TestDestroyInstances(t *testing.T) {
	testCases := []struct {
		desc    string
		ctx     context.Context
		vmCount int
		wantErr bool
	}{
		{"baseline request", context.Background(), 1, false},
		{"nil context", nil, 1, true},
		{"missing vmID", context.Background(), 0, true},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c, insts := fakeClientWithInstances(t, tc.vmCount)
			instIDs := make([]string, 0, tc.vmCount)
			for _, inst := range insts {
				instIDs = append(instIDs, inst.ID)
			}
			gotErr := c.DestroyInstances(tc.ctx, instIDs...)
			if (gotErr != nil) != tc.wantErr {
				t.Errorf("DestroyVM(%v, %+v) = %v; want error %t", tc.ctx, instIDs, gotErr, tc.wantErr)
			}
		})
	}
}

func TestWaitUntilInstanceRunning(t *testing.T) {
	c, wantInsts := fakeClientWithInstances(t, 1)
	wantInst := wantInsts[0]
	ctx := context.Background()
	gotErr := c.WaitUntilInstanceRunning(ctx, wantInst.ID)
	if gotErr != nil {
		t.Errorf("WaitUntilVMExists(%v, %v) failed with error %s", ctx, wantInst.ID, gotErr)
	}
}

func TestWaitUntilInstanceRunningErr(t *testing.T) {
	testCases := []struct {
		desc    string
		ctx     context.Context
		vmCount int
	}{
		{"nil-context", nil, 1},
		{"missing vmID", context.Background(), 0},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c, wantInsts := fakeClientWithInstances(t, tc.vmCount)
			ctx := context.Background()
			wantID := ""
			if len(wantInsts) > 0 {
				wantID = wantInsts[0].ID
			}
			gotErr := c.WaitUntilInstanceRunning(tc.ctx, wantID)
			if gotErr == nil {
				t.Errorf("WaitUntilVMExists(%v, %v) = %s: want error", ctx, wantID, gotErr)
			}
		})
	}
}

func TestEC2ToInstance(t *testing.T) {
	wantCreationTime := time.Unix(1, 1)
	wantDescription := "my-desc"
	wantID := "inst-55"
	wantIPExt := "1.1.1.1"
	wantIPInt := "2.2.2.2"
	wantImage := "ami-56"
	wantKey := "my-key"
	wantName := "my-name"
	wantSecurityGroup := "22"
	wantTagKey := "tag1"
	wantTagValue := "taggy1"
	wantType := "type-1"
	wantZone := "us-east-22"
	wantState := "running"
	var wantCPUCount int64 = 66

	ei := &ec2.Instance{
		CpuOptions: &ec2.CpuOptions{
			CoreCount: aws.Int64(wantCPUCount),
		},
		ImageId:      aws.String(wantImage),
		InstanceId:   aws.String(wantID),
		InstanceType: aws.String(wantType),
		KeyName:      aws.String(wantKey),
		LaunchTime:   aws.Time(wantCreationTime),
		Placement: &ec2.Placement{
			AvailabilityZone: aws.String(wantZone),
		},
		PrivateIpAddress: aws.String(wantIPInt),
		PublicIpAddress:  aws.String(wantIPExt),
		SecurityGroups: []*ec2.GroupIdentifier{
			&ec2.GroupIdentifier{
				GroupId: aws.String(wantSecurityGroup),
			},
		},
		State: &ec2.InstanceState{
			Name: aws.String(wantState),
		},
		Tags: []*ec2.Tag{
			&ec2.Tag{
				Key:   aws.String(tagName),
				Value: aws.String(wantName),
			},
			&ec2.Tag{
				Key:   aws.String(tagDescription),
				Value: aws.String(wantDescription),
			},
			&ec2.Tag{
				Key:   aws.String(wantTagKey),
				Value: aws.String(wantTagValue),
			},
		},
	}
	gotInst := ec2ToInstance(ei)
	if gotInst.CPUCount != wantCPUCount {
		t.Errorf("CPUCount %d; want %d", gotInst.CPUCount, wantCPUCount)
	}
	if gotInst.CreatedAt != wantCreationTime {
		t.Errorf("CreatedAt %s; want %s", gotInst.CreatedAt, wantCreationTime)
	}
	if gotInst.Description != wantDescription {
		t.Errorf("Description %s; want %s", gotInst.Description, wantDescription)
	}
	if gotInst.ID != wantID {
		t.Errorf("ID %s; want %s", gotInst.ID, wantID)
	}
	if gotInst.IPAddressExternal != wantIPExt {
		t.Errorf("IPAddressExternal %s; want %s", gotInst.IPAddressExternal, wantIPExt)
	}
	if gotInst.IPAddressInternal != wantIPInt {
		t.Errorf("IPAddressInternal %s; want %s", gotInst.IPAddressInternal, wantIPInt)
	}
	if gotInst.ImageID != wantImage {
		t.Errorf("Image %s; want %s", gotInst.ImageID, wantImage)
	}
	if gotInst.Name != wantName {
		t.Errorf("Name %s; want %s", gotInst.Name, wantName)
	}
	if gotInst.SSHKeyID != wantKey {
		t.Errorf("SSHKeyID %s; want %s", gotInst.SSHKeyID, wantKey)
	}
	found := false
	for _, sg := range gotInst.SecurityGroups {
		if sg == wantSecurityGroup {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SecurityGroups not found")
	}
	if gotInst.State != wantState {
		t.Errorf("State %s; want %s", gotInst.State, wantState)
	}
	if gotInst.Type != wantType {
		t.Errorf("Type %s; want %s", gotInst.Type, wantType)
	}
	if gotInst.Zone != wantZone {
		t.Errorf("Zone %s; want %s", gotInst.Zone, wantZone)
	}
	gotValue, ok := gotInst.Tags[wantTagKey]
	if !ok || gotValue != wantTagValue {
		t.Errorf("Tags[%s] = %s, %t; want %s, %t", wantTagKey, gotValue, ok, wantTagValue, true)
	}
}

func TestVMConfig(t *testing.T) {
	wantDescription := "desc"
	wantImage := "ami-56"
	wantName := "my-instance"
	wantKey := "my-key"
	wantSecurityGroups := []string{"22"}
	wantTags := map[string]string{
		"tag1": "taggy1",
		"tag2": "taggy2",
	}
	wantType := "type-1"
	wantUserData := "user-data-x"
	wantZone := "us-east-22"

	rii := vmConfig(&EC2VMConfiguration{
		Description:    wantDescription,
		ImageID:        wantImage,
		Name:           wantName,
		SSHKeyID:       wantKey,
		SecurityGroups: wantSecurityGroups,
		Tags:           wantTags,
		Type:           wantType,
		UserData:       wantUserData,
		Zone:           wantZone,
	})

	if *rii.ImageId != wantImage {
		t.Errorf("image id %s; want %s", *rii.ImageId, wantImage)
	}
	if *rii.InstanceType != wantType {
		t.Errorf("image id %s; want %s", *rii.ImageId, wantImage)
	}
	if *rii.MinCount != 1 {
		t.Errorf("MinCount %d; want %d", *rii.MinCount, 1)
	}
	if *rii.MaxCount != 1 {
		t.Errorf("MaxCount %d; want %d", *rii.MaxCount, 1)
	}
	if *rii.Placement.AvailabilityZone != wantZone {
		t.Errorf("AvailabilityZone %s; want %s", *rii.Placement.AvailabilityZone, wantZone)
	}
	if !cmp.Equal(*rii.KeyName, wantKey) {
		t.Errorf("SSHKeyID %+v; want %+v", *rii.KeyName, wantKey)
	}
	if *rii.InstanceInitiatedShutdownBehavior != ec2.ShutdownBehaviorTerminate {
		t.Errorf("Shutdown Behavior %s; want %s", *rii.InstanceInitiatedShutdownBehavior, ec2.ShutdownBehaviorTerminate)
	}
	if *rii.UserData != wantUserData {
		t.Errorf("UserData %s; want %s", *rii.UserData, wantUserData)
	}
	contains := func(tagSpec []*ec2.TagSpecification, key, value string) bool {
		for _, ts := range tagSpec {
			for _, t := range ts.Tags {
				if *t.Key == key && *t.Value == value {
					return true
				}
			}
		}
		return false
	}
	if !contains(rii.TagSpecifications, tagName, wantName) {
		t.Errorf("want Tag Key: %s, Value: %s", tagName, wantName)
	}
	if !contains(rii.TagSpecifications, tagDescription, wantDescription) {
		t.Errorf("want Tag Key: %s, Value: %s", tagDescription, wantDescription)
	}
	for k, v := range wantTags {
		if !contains(rii.TagSpecifications, k, v) {
			t.Errorf("want Tag Key: %s, Value: %s", k, v)
		}
	}
	if !cmp.Equal(aws.StringValueSlice(rii.SecurityGroups), wantSecurityGroups) {
		t.Errorf("SecurityGroups %v; want %v", aws.StringValueSlice(rii.SecurityGroups), wantSecurityGroups)
	}
}

func TestEncodedString(t *testing.T) {
	ud := EC2UserData{
		BuildletBinaryURL: "binary_url_b",
		BuildletHostType:  "host_type_a",
		BuildletImageURL:  "image_url_c",
		BuildletName:      "name_d",
		Metadata: map[string]string{
			"key": "value",
		},
		TLSCert:     "x",
		TLSKey:      "y",
		TLSPassword: "z",
	}
	jsonUserData, err := json.Marshal(ud)
	if err != nil {
		t.Fatalf("unable to marshal user data to json: %s", err)
	}
	wantUD := base64.StdEncoding.EncodeToString([]byte(jsonUserData))
	if ud.EncodedString() != wantUD {
		t.Errorf("EncodedString() = %s; want %s", ud.EncodedString(), wantUD)
	}
}
