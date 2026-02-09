// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/cloud"
)

// awsClient represents the AWS specific calls made during the
// lifecycle of a buildlet. This is a partial implementation of the AWSClient found at
// `golang.org/x/internal/cloud`.
type awsClient interface {
	Instance(ctx context.Context, instID string) (*cloud.Instance, error)
	CreateInstance(ctx context.Context, config *cloud.EC2VMConfiguration) (*cloud.Instance, error)
	WaitUntilInstanceRunning(ctx context.Context, instID string) error
}

// EC2Client is the client used to create buildlets on EC2.
type EC2Client struct {
	client awsClient
}

// NewEC2Client creates a new EC2Client.
func NewEC2Client(client *cloud.AWSClient) *EC2Client {
	return &EC2Client{
		client: client,
	}
}

// StartNewVM boots a new VM on EC2, waits until the client is accepting connections
// on the configured port and returns a buildlet client configured communicate with it.
func (c *EC2Client) StartNewVM(ctx context.Context, buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *VMOpts) (Client, error) {
	// check required params
	if opts == nil || opts.TLS.IsZero() {
		return nil, errors.New("TLS keypair is not set")
	}
	if buildEnv == nil {
		return nil, errors.New("invalid build environment")
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
	if opts.DeleteIn == 0 {
		// Note: This implements a short default in the rare case the caller doesn't care.
		opts.DeleteIn = 30 * time.Minute
	}

	vmConfig := configureVM(buildEnv, hconf, vmName, hostType, opts)

	vm, err := c.createVM(ctx, vmConfig, opts)
	if err != nil {
		return nil, err
	}
	if err = c.waitUntilVMExists(ctx, vm.ID, opts); err != nil {
		return nil, err
	}
	// once the VM is up and running then all of the configuration data is available
	// when the API is querried for the VM.
	vm, err = c.client.Instance(ctx, vm.ID)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve instance %q information: %w", vm.ID, err)
	}
	buildletURL, ipPort, err := ec2BuildletParams(vm, opts)
	if err != nil {
		return nil, err
	}
	return buildletClient(ctx, buildletURL, ipPort, opts)
}

// createVM submits a request for the creation of a VM.
func (c *EC2Client) createVM(ctx context.Context, config *cloud.EC2VMConfiguration, opts *VMOpts) (*cloud.Instance, error) {
	if config == nil || opts == nil {
		return nil, errors.New("invalid parameter")
	}
	inst, err := c.client.CreateInstance(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to create instance: %w", err)
	}
	condRun(opts.OnInstanceRequested)
	return inst, nil
}

// waitUntilVMExists submits a request which waits until an instance exists before returning.
func (c *EC2Client) waitUntilVMExists(ctx context.Context, instID string, opts *VMOpts) error {
	if err := c.client.WaitUntilInstanceRunning(ctx, instID); err != nil {
		return fmt.Errorf("failed waiting for vm instance: %w", err)
	}
	condRun(opts.OnInstanceCreated)
	return nil
}

// configureVM creates a configuration for an EC2 VM instance.
func configureVM(buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *VMOpts) *cloud.EC2VMConfiguration {
	return &cloud.EC2VMConfiguration{
		Description:    opts.Description,
		ImageID:        hconf.VMImage,
		Name:           vmName,
		SSHKeyID:       "ec2-go-builders",
		SecurityGroups: []string{buildEnv.AWSSecurityGroup},
		Tags:           make(map[string]string),
		Type:           hconf.MachineType(),
		UserData:       vmUserDataSpec(buildEnv, hconf, vmName, hostType, opts),
		Zone:           opts.Zone,
	}
}

func vmUserDataSpec(buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *VMOpts) string {
	// add custom metadata to the user data.
	ud := cloud.EC2UserData{
		BuildletName:      vmName,
		BuildletBinaryURL: hconf.BuildletBinaryURL(buildEnv),
		BuildletHostType:  hostType,
		BuildletImageURL:  hconf.ContainerVMImage(),
		Metadata:          make(map[string]string),
		TLSCert:           opts.TLS.CertPEM,
		TLSKey:            opts.TLS.KeyPEM,
		TLSPassword:       opts.TLS.Password(),
	}
	maps.Copy(ud.Metadata, opts.Meta)
	return ud.EncodedString()
}

// ec2BuildletParams returns the necessary information to connect to an EC2 buildlet. A
// buildlet URL and an IP address port are required to connect to a buildlet.
func ec2BuildletParams(inst *cloud.Instance, opts *VMOpts) (string, string, error) {
	if inst.IPAddressExternal == "" {
		return "", "", errors.New("external IP address is not set")
	}
	extIP := inst.IPAddressExternal
	buildletURL := fmt.Sprintf("https://%s", extIP)
	ipPort := net.JoinHostPort(extIP, "443")

	if opts.OnGotEC2InstanceInfo != nil {
		opts.OnGotEC2InstanceInfo(inst)
	}
	return buildletURL, ipPort, nil
}
