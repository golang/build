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
	"log"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/servicequotas"
)

const (
	// tagName denotes the text used for Name tags.
	tagName = "Name"
	// tagDescription denotes the text used for Description tags.
	tagDescription = "Description"
)

const (
	// QuotaCodeCPUOnDemand is the quota code for on-demand CPUs.
	QuotaCodeCPUOnDemand = "L-1216C47A"
	// QuotaServiceEC2 is the service code for the EC2 service.
	QuotaServiceEC2 = "ec2"
)

// vmClient defines the interface used to call the backing EC2 service. This is a partial interface
// based on the EC2 package defined at github.com/aws/aws-sdk-go/service/ec2.
type vmClient interface {
	DescribeInstancesPagesWithContext(context.Context, *ec2.DescribeInstancesInput, func(*ec2.DescribeInstancesOutput, bool) bool, ...request.Option) error
	DescribeInstancesWithContext(context.Context, *ec2.DescribeInstancesInput, ...request.Option) (*ec2.DescribeInstancesOutput, error)
	RunInstancesWithContext(context.Context, *ec2.RunInstancesInput, ...request.Option) (*ec2.Reservation, error)
	TerminateInstancesWithContext(context.Context, *ec2.TerminateInstancesInput, ...request.Option) (*ec2.TerminateInstancesOutput, error)
	WaitUntilInstanceRunningWithContext(context.Context, *ec2.DescribeInstancesInput, ...request.WaiterOption) error
	DescribeInstanceTypesPagesWithContext(context.Context, *ec2.DescribeInstanceTypesInput, func(*ec2.DescribeInstanceTypesOutput, bool) bool, ...request.Option) error
}

// quotaClient defines the interface used to call the backing service quotas service. This
// is a partial interface based on the service quota package defined at
// github.com/aws/aws-sdk-go/service/servicequotas.
type quotaClient interface {
	GetServiceQuota(*servicequotas.GetServiceQuotaInput) (*servicequotas.GetServiceQuotaOutput, error)
}

// EC2VMConfiguration is the configuration needed for an EC2 instance.
type EC2VMConfiguration struct {
	// Description is a user defined description of the instance. It is displayed
	// on the AWS UI. It is an optional field.
	Description string
	// ImageID is the ID of the image used to launch the instance. It is a required field.
	ImageID string
	// Name is a user defined name for the instance. It is displayed on the AWS UI. It is
	// an optional field.
	Name string
	// SSHKeyID is the name of the SSH key pair to use for access. It is a required field.
	SSHKeyID string
	// SecurityGroups contains the names of the security groups to be applied to the VM. If none
	// are provided the default security group will be used.
	SecurityGroups []string
	// Tags the tags to apply to the resources during launch.
	Tags map[string]string
	// Type is the type of instance.
	Type string
	// UserData is the user data to make available to the instance. This data is available
	// on the VM via the metadata endpoints. It must be a base64-encoded string. User
	// data is limited to 16 KB.
	UserData string
	// Zone the Availability Zone of the instance.
	Zone string
}

// Instance is a virtual machine.
type Instance struct {
	// CPUCount is the number of VCPUs the instance is configured with.
	CPUCount int64
	// CreatedAt is the time when the instance was launched.
	CreatedAt time.Time
	// Description is a user defined description of the instance.
	Description string
	// ID is the instance ID.
	ID string
	// IPAddressExternal is the public IPv4 address assigned to the instance.
	IPAddressExternal string
	// IPAddressInternal is the private IPv4 address assigned to the instance.
	IPAddressInternal string
	// ImageID is The ID of the AMI(image)  used to launch the instance.
	ImageID string
	// Name is a user defined name for the instance.
	Name string
	// SSHKeyID is the name of the SSH key pair to use for access. It is a required field.
	SSHKeyID string
	// SecurityGroups is the security groups for the instance.
	SecurityGroups []string
	// State contains the state of the instance.
	State string
	// Tags contains tags assigned to the instance.
	Tags map[string]string
	// Type is the name of instance type.
	Type string
	// Zone is the availability zone where the instance is deployed.
	Zone string
}

// AWSClient is a client for AWS services.
type AWSClient struct {
	ec2Client   vmClient
	quotaClient quotaClient
}

// AWSOpt is an optional configuration setting for the AWSClient.
type AWSOpt func(*AWSClient)

// NewAWSClient creates a new AWS client.
func NewAWSClient(region, keyID, accessKey string, opts ...AWSOpt) (*AWSClient, error) {
	s, err := session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewStaticCredentials(keyID, accessKey, ""), // Token is only required for STS
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %v", err)
	}
	c := &AWSClient{
		ec2Client:   ec2.New(s),
		quotaClient: servicequotas.New(s),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Instance retrieves an EC2 instance by instance ID.
func (ac *AWSClient) Instance(ctx context.Context, instID string) (*Instance, error) {
	dio, err := ac.ec2Client.DescribeInstancesWithContext(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instID)},
	})
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve instance %q information: %w", instID, err)
	}

	if dio == nil || len(dio.Reservations) != 1 || len(dio.Reservations[0].Instances) != 1 {
		return nil, errors.New("describe instances output does not contain a valid instance")
	}
	ec2Inst := dio.Reservations[0].Instances[0]
	return ec2ToInstance(ec2Inst), err
}

// RunningInstances retrieves all EC2 instances in a region which have not been terminated or stopped.
func (ac *AWSClient) RunningInstances(ctx context.Context) ([]*Instance, error) {
	instances := make([]*Instance, 0)

	fn := func(page *ec2.DescribeInstancesOutput, lastPage bool) bool {
		for _, res := range page.Reservations {
			for _, inst := range res.Instances {
				instances = append(instances, ec2ToInstance(inst))
			}
		}
		return true
	}
	err := ac.ec2Client.DescribeInstancesPagesWithContext(ctx, &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []*string{aws.String(ec2.InstanceStateNameRunning), aws.String(ec2.InstanceStateNamePending)},
			},
		},
	}, fn)
	if err != nil {
		return nil, err
	}
	return instances, nil
}

// CreateInstance creates an EC2 VM instance.
func (ac *AWSClient) CreateInstance(ctx context.Context, config *EC2VMConfiguration) (*Instance, error) {
	if config == nil {
		return nil, errors.New("unable to create a VM with a nil instance")
	}
	runResult, err := ac.ec2Client.RunInstancesWithContext(ctx, vmConfig(config))
	if err != nil {
		return nil, fmt.Errorf("unable to create instance: %w", err)
	}
	if runResult == nil || len(runResult.Instances) != 1 {
		return nil, fmt.Errorf("unexpected number of instances. want 1; got %d", len(runResult.Instances))
	}
	return ec2ToInstance(runResult.Instances[0]), nil
}

// DestroyInstances terminates EC2 VM instances.
func (ac *AWSClient) DestroyInstances(ctx context.Context, instIDs ...string) error {
	ids := aws.StringSlice(instIDs)
	_, err := ac.ec2Client.TerminateInstancesWithContext(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: ids,
	})
	if err != nil {
		return fmt.Errorf("unable to destroy vm: %w", err)
	}
	return err
}

// WaitUntilInstanceRunning waits until a stopping condition is met. The stopping conditions are:
// - The requested instance state is `running`.
// - The passed in context is cancelled or the deadline expires.
// - 40 requests are made with a 15 second delay between each request.
func (ac *AWSClient) WaitUntilInstanceRunning(ctx context.Context, instID string) error {
	err := ac.ec2Client.WaitUntilInstanceRunningWithContext(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instID)},
	})
	if err != nil {
		return fmt.Errorf("failed waiting for vm instance: %w", err)
	}
	return err
}

// InstanceType contains information about an EC2 vm instance type.
type InstanceType struct {
	// Type is the textual label used to describe an instance type.
	Type string
	// CPU is the Default vCPU count.
	CPU int64
}

// InstanceTypesARM retrieves all EC2 instance types in a region which support the
// ARM64 architecture.
func (ac *AWSClient) InstanceTypesARM(ctx context.Context) ([]*InstanceType, error) {
	var its []*InstanceType
	contains := func(strs []*string, want string) bool {
		for _, s := range strs {
			if aws.StringValue(s) == want {
				return true
			}
		}
		return false
	}
	fn := func(page *ec2.DescribeInstanceTypesOutput, lastPage bool) bool {
		for _, it := range page.InstanceTypes {
			if !contains(it.ProcessorInfo.SupportedArchitectures, "arm64") {
				continue
			}
			its = append(its, &InstanceType{
				Type: aws.StringValue(it.InstanceType),
				CPU:  aws.Int64Value(it.VCpuInfo.DefaultVCpus),
			})
		}
		return true
	}
	err := ac.ec2Client.DescribeInstanceTypesPagesWithContext(ctx, &ec2.DescribeInstanceTypesInput{}, fn)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve arm64 instance types: %w", err)
	}
	return its, nil
}

// Quota retrieves the requested service quota for the service.
func (ac *AWSClient) Quota(ctx context.Context, service, code string) (int64, error) {
	// TODO(golang.org/issue/36841): use ctx
	sq, err := ac.quotaClient.GetServiceQuota(&servicequotas.GetServiceQuotaInput{
		QuotaCode:   aws.String(code),
		ServiceCode: aws.String(service),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve quota: %w", err)
	}
	return int64(aws.Float64Value(sq.Quota.Value)), nil
}

// ec2ToInstance converts an `ec2.Instance` to an `Instance`
func ec2ToInstance(inst *ec2.Instance) *Instance {
	secGroup := make([]string, 0, len(inst.SecurityGroups))
	for _, sg := range inst.SecurityGroups {
		secGroup = append(secGroup, aws.StringValue(sg.GroupId))
	}
	i := &Instance{
		CreatedAt:         aws.TimeValue(inst.LaunchTime),
		ID:                *inst.InstanceId,
		IPAddressExternal: aws.StringValue(inst.PublicIpAddress),
		IPAddressInternal: aws.StringValue(inst.PrivateIpAddress),
		ImageID:           aws.StringValue(inst.ImageId),
		SSHKeyID:          aws.StringValue(inst.KeyName),
		SecurityGroups:    secGroup,
		State:             aws.StringValue(inst.State.Name),
		Tags:              make(map[string]string),
		Type:              aws.StringValue(inst.InstanceType),
	}
	if inst.Placement != nil {
		i.Zone = aws.StringValue(inst.Placement.AvailabilityZone)
	}
	if inst.CpuOptions != nil {
		i.CPUCount = aws.Int64Value(inst.CpuOptions.CoreCount)
	}
	for _, tag := range inst.Tags {
		switch *tag.Key {
		case tagName:
			i.Name = *tag.Value
		case tagDescription:
			i.Description = *tag.Value
		default:
			i.Tags[*tag.Key] = *tag.Value
		}
	}
	return i
}

// vmConfig converts a configuration into a request to create an instance.
func vmConfig(config *EC2VMConfiguration) *ec2.RunInstancesInput {
	ri := &ec2.RunInstancesInput{
		ImageId:      aws.String(config.ImageID),
		InstanceType: aws.String(config.Type),
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		Placement: &ec2.Placement{
			AvailabilityZone: aws.String(config.Zone),
		},
		KeyName:                           aws.String(config.SSHKeyID),
		InstanceInitiatedShutdownBehavior: aws.String(ec2.ShutdownBehaviorTerminate),
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: aws.String("instance"),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String(tagName),
						Value: aws.String(config.Name),
					},
					{
						Key:   aws.String(tagDescription),
						Value: aws.String(config.Description),
					},
				},
			},
		},
		SecurityGroups: aws.StringSlice(config.SecurityGroups),
		UserData:       aws.String(config.UserData),
	}
	for k, v := range config.Tags {
		ri.TagSpecifications[0].Tags = append(ri.TagSpecifications[0].Tags, &ec2.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}
	return ri
}

// EC2UserData is stored in the user data for each EC2 instance. This is
// used to store metadata about the running instance. The buildlet will retrieve
// this on EC2 instances before allowing connections from the coordinator.
type EC2UserData struct {
	// BuildletBinaryURL is the url to the buildlet binary stored on GCS.
	BuildletBinaryURL string `json:"buildlet_binary_url,omitempty"`
	// BuildletHostType is the host type used by the buildlet. For example, `host-linux-arm64-aws`.
	BuildletHostType string `json:"buildlet_host_type,omitempty"`
	// BuildletImageURL is the url for the buildlet container image.
	BuildletImageURL string `json:"buildlet_image_url,omitempty"`
	// BuildletName is the name which should be passed onto the buildlet.
	BuildletName string `json:"buildlet_name,omitempty"`
	// Metadata provides a location for arbitrary metadata to be stored.
	Metadata map[string]string `json:"metadata,omitempty"`
	// TLSCert is the TLS certificate used by the buildlet.
	TLSCert string `json:"tls_cert,omitempty"`
	// TLSKey is the TLS key used by the buildlet.
	TLSKey string `json:"tls_key,omitempty"`
	// TLSPassword contains the SHA1 of the TLS key used by the buildlet for basic authentication.
	TLSPassword string `json:"tls_password,omitempty"`
}

// EncodedString converts `EC2UserData` into JSON which is base64 encoded.
// User data must be base64 encoded upon creation.
func (ud *EC2UserData) EncodedString() string {
	jsonUserData, err := json.Marshal(ud)
	if err != nil {
		log.Printf("unable to marshal user data: %v", err)
	}
	return base64.StdEncoding.EncodeToString([]byte(jsonUserData))
}
