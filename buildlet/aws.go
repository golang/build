// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/dashboard"
)

// EC2UserData is stored in the user data for each EC2 instance. This is
// used to store metadata about the running instance. The buildlet will retrieve
// this on EC2 instances before allowing connections from the coordinator.
type EC2UserData struct {
	BuildletBinaryURL string            `json:"buildlet_binary_url,omitempty"`
	BuildletHostType  string            `json:"buildlet_host_type,omitempty"`
	BuildletImageURL  string            `json:"buildlet_image_url,omitempty"`
	BuildletName      string            `json:"buildlet_name,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	TLSCert           string            `json:"tls_cert,omitempty"`
	TLSKey            string            `json:"tls_key,omitempty"`
	TLSPassword       string            `json:"tls_password,omitempty"`
}

// ec2Client represents the EC2 specific calls made durring the
// lifecycle of a buildlet.
type ec2Client interface {
	DescribeInstancesWithContext(context.Context, *ec2.DescribeInstancesInput, ...request.Option) (*ec2.DescribeInstancesOutput, error)
	RunInstancesWithContext(context.Context, *ec2.RunInstancesInput, ...request.Option) (*ec2.Reservation, error)
	TerminateInstancesWithContext(context.Context, *ec2.TerminateInstancesInput, ...request.Option) (*ec2.TerminateInstancesOutput, error)
	WaitUntilInstanceRunningWithContext(context.Context, *ec2.DescribeInstancesInput, ...request.WaiterOption) error
}

// AWSClient is the client used to create and destroy buildlets on AWS.
type AWSClient struct {
	client ec2Client
}

// NewAWSClient creates a new AWSClient.
func NewAWSClient(region, keyID, accessKey string) (*AWSClient, error) {
	s, err := session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewStaticCredentials(keyID, accessKey, ""), // Token is only required for STS
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %v", err)
	}
	return &AWSClient{
		client: ec2.New(s),
	}, nil
}

// StartNewVM boots a new VM on EC2, waits until the client is accepting connections
// on the configured port and returns a buildlet client configured communicate with it.
func (c *AWSClient) StartNewVM(ctx context.Context, buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *VMOpts) (*Client, error) {
	// check required params
	if opts == nil || opts.TLS.IsZero() {
		return nil, errors.New("TLS keypair is not set")
	}
	if buildEnv == nil {
		return nil, errors.New("invalid build enviornment")
	}
	if hconf == nil {
		return nil, errors.New("invalid host configuration")
	}
	if vmName == "" || hostType == "" {
		return nil, fmt.Errorf("invalid vmName: %q and hostType: %q", vmName, hostType)
	}

	// configure defaults
	if opts.Description == "" {
		opts.Description = fmt.Sprintf("Go Builder for %s", hostType)
	}
	if opts.Zone == "" {
		opts.Zone = buildEnv.RandomEC2VMZone()
	}
	if opts.DeleteIn == 0 {
		opts.DeleteIn = 30 * time.Minute
	}

	vmConfig := c.configureVM(buildEnv, hconf, vmName, hostType, opts)

	vmID, err := c.createVM(ctx, vmConfig, opts)
	if err != nil {
		return nil, err
	}
	if err = c.WaitUntilVMExists(ctx, vmID, opts); err != nil {
		return nil, err
	}
	vm, err := c.RetrieveVMInfo(ctx, vmID)
	if err != nil {
		return nil, err
	}
	buildletURL, ipPort, err := ec2BuildletParams(vm, opts)
	if err != nil {
		return nil, err
	}
	return buildletClient(ctx, buildletURL, ipPort, opts)
}

// createVM submits a request for the creation of a VM.
func (c *AWSClient) createVM(ctx context.Context, vmConfig *ec2.RunInstancesInput, opts *VMOpts) (string, error) {
	runResult, err := c.client.RunInstancesWithContext(ctx, vmConfig)
	if err != nil {
		return "", fmt.Errorf("unable to create instance: %w", err)
	}
	condRun(opts.OnInstanceRequested)
	return *runResult.Instances[0].InstanceId, nil
}

// WaitUntilVMExists submits a request which waits until an instance exists before returning.
func (c *AWSClient) WaitUntilVMExists(ctx context.Context, instID string, opts *VMOpts) error {
	err := c.client.WaitUntilInstanceRunningWithContext(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instID)},
	})
	if err != nil {
		return fmt.Errorf("failed waiting for vm instance: %w", err)
	}
	condRun(opts.OnInstanceCreated)
	return err
}

// RetrieveVMInfo retrives the information about a VM.
func (c *AWSClient) RetrieveVMInfo(ctx context.Context, instID string) (*ec2.Instance, error) {
	instances, err := c.client.DescribeInstancesWithContext(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instID)},
	})
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve instance %q information: %w", instID, err)
	}

	instance, err := ec2Instance(instances)
	if err != nil {
		return nil, fmt.Errorf("failed to read instance description: %w", err)
	}
	return instance, err
}

// configureVM creates a configuration for an EC2 VM instance.
func (c *AWSClient) configureVM(buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *VMOpts) *ec2.RunInstancesInput {
	vmConfig := &ec2.RunInstancesInput{
		ImageId:      aws.String(hconf.VMImage),
		InstanceType: aws.String(hconf.MachineType()),
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		Placement: &ec2.Placement{
			AvailabilityZone: aws.String(opts.Zone),
		},
		KeyName:                           aws.String("ec2-go-builders"),
		InstanceInitiatedShutdownBehavior: aws.String("terminate"),
		TagSpecifications: []*ec2.TagSpecification{
			&ec2.TagSpecification{
				ResourceType: aws.String("instance"),
				Tags: []*ec2.Tag{
					&ec2.Tag{
						Key:   aws.String("Name"),
						Value: aws.String(vmName),
					},
					&ec2.Tag{
						Key:   aws.String("Description"),
						Value: aws.String(opts.Description),
					},
				},
			},
		},
	}
	c.vmUserDataSpec(vmConfig, buildEnv, hconf, vmName, hostType, opts)
	return vmConfig
}

func (c *AWSClient) vmUserDataSpec(vmConfig *ec2.RunInstancesInput, buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *VMOpts) {
	// add custom metadata to the user data.
	ud := EC2UserData{
		BuildletName:      vmName,
		BuildletBinaryURL: hconf.BuildletBinaryURL(buildEnv),
		BuildletHostType:  hostType,
		BuildletImageURL:  hconf.ContainerVMImage(),
		Metadata:          make(map[string]string),
		TLSCert:           opts.TLS.CertPEM,
		TLSKey:            opts.TLS.KeyPEM,
		TLSPassword:       opts.TLS.Password(),
	}
	for k, v := range opts.Meta {
		ud.Metadata[k] = v
	}
	jsonUserData, err := json.Marshal(ud)
	if err != nil {
		log.Printf("unable to marshal user data: %v", err)
	}
	// user data must be base64 encoded
	jud := base64.StdEncoding.EncodeToString([]byte(jsonUserData))
	vmConfig.SetUserData(jud)
}

// DestroyVM submits a request to destroy a VM.
func (c *AWSClient) DestroyVM(ctx context.Context, vmID string) error {
	_, err := c.client.TerminateInstancesWithContext(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []*string{aws.String(vmID)},
	})
	if err != nil {
		return fmt.Errorf("unable to destroy vm: %w", err)
	}
	return err
}

// ec2Instance extracts the first instance found in the the describe instances output.
func ec2Instance(dio *ec2.DescribeInstancesOutput) (*ec2.Instance, error) {
	if dio == nil || dio.Reservations == nil || dio.Reservations[0].Instances == nil {
		return nil, errors.New("describe instances output does not contain a valid instance")
	}
	return dio.Reservations[0].Instances[0], nil
}

// ec2InstanceIPs returns the internal and external ip addresses for the VM.
func ec2InstanceIPs(inst *ec2.Instance) (intIP, extIP string, err error) {
	if inst.PrivateIpAddress == nil || *inst.PrivateIpAddress == "" {
		return "", "", errors.New("internal IP address is not set")
	}
	if inst.PublicIpAddress == nil || *inst.PublicIpAddress == "" {
		return "", "", errors.New("external IP address is not set")
	}
	return *inst.PrivateIpAddress, *inst.PublicIpAddress, nil
}

// ec2BuildletParams returns the necessary information to connect to an EC2 buildlet. A
// buildlet URL and an IP address port are required to connect to a buildlet.
func ec2BuildletParams(inst *ec2.Instance, opts *VMOpts) (string, string, error) {
	_, extIP, err := ec2InstanceIPs(inst)
	if err != nil {
		return "", "", fmt.Errorf("failed to retrieve IP addresses: %w", err)
	}
	buildletURL := fmt.Sprintf("https://%s", extIP)
	ipPort := net.JoinHostPort(extIP, "443")

	if opts.OnGotEC2InstanceInfo != nil {
		opts.OnGotEC2InstanceInfo(inst)
	}
	return buildletURL, ipPort, err
}
