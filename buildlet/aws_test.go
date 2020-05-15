// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/dashboard"
)

type fakeEC2Client struct {
	// returned in describe instances
	PrivateIP *string
	PublicIP  *string
}

func (f *fakeEC2Client) DescribeInstancesWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, opt ...request.Option) (*ec2.DescribeInstancesOutput, error) {
	if ctx == nil || input == nil || len(input.InstanceIds) == 0 {
		return nil, request.ErrInvalidParams{}
	}
	return &ec2.DescribeInstancesOutput{
		Reservations: []*ec2.Reservation{
			&ec2.Reservation{
				Instances: []*ec2.Instance{
					&ec2.Instance{
						InstanceId:       input.InstanceIds[0],
						PrivateIpAddress: f.PrivateIP,
						PublicIpAddress:  f.PublicIP,
					},
				},
			},
		},
	}, nil
}

func (f *fakeEC2Client) RunInstancesWithContext(ctx context.Context, input *ec2.RunInstancesInput, opts ...request.Option) (*ec2.Reservation, error) {
	if ctx == nil || input == nil {
		return nil, request.ErrInvalidParams{}
	}
	if input.ImageId == nil || input.InstanceType == nil || input.MinCount == nil || input.Placement == nil {
		return nil, errors.New("invalid instance configuration")
	}
	return &ec2.Reservation{
		Instances: []*ec2.Instance{
			&ec2.Instance{
				ImageId:      input.ImageId,
				InstanceType: input.InstanceType,
				InstanceId:   aws.String("44"),
				Placement:    input.Placement,
			},
		},
		ReservationId: aws.String("res_id"),
	}, nil
}

func (f *fakeEC2Client) TerminateInstancesWithContext(ctx context.Context, input *ec2.TerminateInstancesInput, opts ...request.Option) (*ec2.TerminateInstancesOutput, error) {
	if ctx == nil || input == nil || len(input.InstanceIds) == 0 {
		return nil, request.ErrInvalidParams{}
	}
	for _, id := range input.InstanceIds {
		if *id == "" {
			return nil, errors.New("invalid instance id")
		}
	}
	return &ec2.TerminateInstancesOutput{
		TerminatingInstances: nil,
	}, nil
}

func (f *fakeEC2Client) WaitUntilInstanceRunningWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, opt ...request.WaiterOption) error {
	if ctx == nil || input == nil || len(input.InstanceIds) == 0 {
		return request.ErrInvalidParams{}
	}
	return nil
}

func TestRetrieveVMInfo(t *testing.T) {
	wantVMID := "22"
	ctx := context.Background()
	c := &AWSClient{
		client: &fakeEC2Client{},
	}
	gotInst, gotErr := c.RetrieveVMInfo(ctx, wantVMID)
	if gotErr != nil {
		t.Fatalf("RetrieveVMInfo(%v, %q) failed with error %s", ctx, wantVMID, gotErr)
	}
	if gotInst == nil || *gotInst.InstanceId != wantVMID {
		t.Errorf("RetrieveVMInfo(%v, %q) failed with error %s", ctx, wantVMID, gotErr)
	}
}

func TestStartNewVM(t *testing.T) {
	kp, err := NewKeyPair()
	if err != nil {
		t.Fatalf("unable to generate key pair: %s", err)
	}
	buildEnv := &buildenv.Environment{}
	hconf := &dashboard.HostConfig{}
	vmName := "sample-vm"
	hostType := "host-sample-os"
	opts := &VMOpts{
		Zone:        "us-west",
		ProjectID:   "project1",
		TLS:         kp,
		Description: "Golang builder for sample",
		Meta: map[string]string{
			"Owner": "george",
		},
		DeleteIn:                 45 * time.Second,
		SkipEndpointVerification: true,
	}
	c := &AWSClient{
		client: &fakeEC2Client{
			PrivateIP: aws.String("8.8.8.8"),
			PublicIP:  aws.String("9.9.9.9"),
		},
	}
	gotClient, gotErr := c.StartNewVM(context.Background(), buildEnv, hconf, vmName, hostType, opts)
	if gotErr != nil {
		t.Fatalf("error is not nil: %v", gotErr)
	}
	if gotClient == nil {
		t.Fatalf("response is nil")
	}
}

func TestStartNewVMError(t *testing.T) {
	kp, err := NewKeyPair()
	if err != nil {
		t.Fatalf("unable to generate key pair: %s", err)
	}

	testCases := []struct {
		desc     string
		buildEnv *buildenv.Environment
		hconf    *dashboard.HostConfig
		vmName   string
		hostType string
		opts     *VMOpts
	}{
		{
			desc:     "nil-buildenv",
			hconf:    &dashboard.HostConfig{},
			vmName:   "sample-vm",
			hostType: "host-sample-os",
			opts: &VMOpts{
				Zone:        "us-west",
				ProjectID:   "project1",
				TLS:         kp,
				Description: "Golang builder for sample",
				Meta: map[string]string{
					"Owner": "george",
				},
				DeleteIn: 45 * time.Second,
			},
		},
		{
			desc:     "nil-hconf",
			buildEnv: &buildenv.Environment{},
			vmName:   "sample-vm",
			hostType: "host-sample-os",
			opts: &VMOpts{
				Zone:        "us-west",
				ProjectID:   "project1",
				TLS:         kp,
				Description: "Golang builder for sample",
				Meta: map[string]string{
					"Owner": "george",
				},
				DeleteIn: 45 * time.Second,
			},
		},
		{
			desc:     "empty-vnName",
			buildEnv: &buildenv.Environment{},
			hconf:    &dashboard.HostConfig{},
			vmName:   "",
			hostType: "host-sample-os",
			opts: &VMOpts{
				Zone:        "us-west",
				ProjectID:   "project1",
				TLS:         kp,
				Description: "Golang builder for sample",
				Meta: map[string]string{
					"Owner": "george",
				},
				DeleteIn: 45 * time.Second,
			},
		},
		{
			desc:     "empty-hostType",
			buildEnv: &buildenv.Environment{},
			hconf:    &dashboard.HostConfig{},
			vmName:   "sample-vm",
			hostType: "",
			opts: &VMOpts{
				Zone:        "us-west",
				ProjectID:   "project1",
				TLS:         kp,
				Description: "Golang builder for sample",
				Meta: map[string]string{
					"Owner": "george",
				},
				DeleteIn: 45 * time.Second,
			},
		},
		{
			desc:     "missing-certs",
			buildEnv: &buildenv.Environment{},
			hconf:    &dashboard.HostConfig{},
			vmName:   "sample-vm",
			hostType: "host-sample-os",
			opts: &VMOpts{
				Zone:        "us-west",
				ProjectID:   "project1",
				Description: "Golang builder for sample",
				Meta: map[string]string{
					"Owner": "george",
				},
				DeleteIn: 45 * time.Second,
			},
		},
		{
			desc:     "nil-opts",
			buildEnv: &buildenv.Environment{},
			hconf:    &dashboard.HostConfig{},
			vmName:   "sample-vm",
			hostType: "host-sample-os",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := &AWSClient{
				client: &fakeEC2Client{},
			}
			gotClient, gotErr := c.StartNewVM(context.Background(), tc.buildEnv, tc.hconf, tc.vmName, tc.hostType, tc.opts)
			if gotErr == nil {
				t.Errorf("expected error did not occur")
			}
			if gotClient != nil {
				t.Errorf("got %+v; expected nil", gotClient)
			}
		})
	}
}

func TestWaitUntilInstanceExists(t *testing.T) {
	vmID := "22"
	invoked := false
	opts := &VMOpts{
		OnInstanceCreated: func() {
			invoked = true
		},
	}
	ctx := context.Background()
	c := &AWSClient{
		client: &fakeEC2Client{},
	}
	gotErr := c.WaitUntilVMExists(ctx, vmID, opts)
	if gotErr != nil {
		t.Fatalf("WaitUntilVMExists(%v, %v, %v) failed with error %s", ctx, vmID, opts, gotErr)
	}
	if !invoked {
		t.Errorf("OnInstanceCreated() was not invoked")
	}
}

func TestCreateVM(t *testing.T) {
	vmConfig := &ec2.RunInstancesInput{
		ImageId:      aws.String("foo"),
		InstanceType: aws.String("type-a"),
		MinCount:     aws.Int64(15),
		Placement: &ec2.Placement{
			AvailabilityZone: aws.String("eu-15"),
		},
	}
	invoked := false
	opts := &VMOpts{
		OnInstanceRequested: func() {
			invoked = true
		},
	}
	wantVMID := aws.String("44")

	c := &AWSClient{
		client: &fakeEC2Client{},
	}
	gotVMID, gotErr := c.createVM(context.Background(), vmConfig, opts)
	if gotErr != nil {
		t.Fatalf("createVM(ctx, %v, %v) failed with %s", vmConfig, opts, gotErr)
	}
	if gotVMID != *wantVMID {
		t.Errorf("createVM(ctx, %v, %v) = %s, nil; want %s, nil", vmConfig, opts, gotVMID, *wantVMID)
	}
	if !invoked {
		t.Errorf("OnInstanceRequested() was not invoked")
	}
}

func TestCreateVMError(t *testing.T) {
	testCases := []struct {
		desc     string
		vmConfig *ec2.RunInstancesInput
		opts     *VMOpts
	}{
		{
			desc: "missing-vmConfig",
		},
		{
			desc: "missing-image-id",
			vmConfig: &ec2.RunInstancesInput{
				InstanceType: aws.String("type-a"),
				MinCount:     aws.Int64(15),
				Placement: &ec2.Placement{
					AvailabilityZone: aws.String("eu-15"),
				},
			},
			opts: &VMOpts{
				OnInstanceRequested: func() {},
			},
		},
		{
			desc: "missing-instance-id",
			vmConfig: &ec2.RunInstancesInput{
				ImageId:  aws.String("foo"),
				MinCount: aws.Int64(15),
				Placement: &ec2.Placement{
					AvailabilityZone: aws.String("eu-15"),
				},
			},
			opts: &VMOpts{
				OnInstanceRequested: func() {},
			},
		},
		{
			desc: "missing-placement",
			vmConfig: &ec2.RunInstancesInput{
				ImageId:      aws.String("foo"),
				InstanceType: aws.String("type-a"),
				MinCount:     aws.Int64(15),
			},
			opts: &VMOpts{
				OnInstanceRequested: func() {},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := &AWSClient{
				client: &fakeEC2Client{},
			}
			gotVMID, gotErr := c.createVM(context.Background(), tc.vmConfig, tc.opts)
			if gotErr == nil {
				t.Errorf("createVM(ctx, %v, %v) = %s, %v; want error", tc.vmConfig, tc.opts, gotVMID, gotErr)
			}
			if gotVMID != "" {
				t.Errorf("createVM(ctx, %v, %v) = %s, %v; %q, error", tc.vmConfig, tc.opts, gotVMID, gotErr, "")
			}
		})
	}
}

func TestDestroyVM(t *testing.T) {
	testCases := []struct {
		desc    string
		ctx     context.Context
		vmID    string
		wantErr bool
	}{
		{"baseline request", context.Background(), "vm-20", false},
		{"nil context", nil, "vm-20", true},
		{"nil context", context.Background(), "", true},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := &AWSClient{
				client: &fakeEC2Client{},
			}
			gotErr := c.DestroyVM(tc.ctx, tc.vmID)
			if (gotErr != nil) != tc.wantErr {
				t.Errorf("DestroyVM(%v, %q) = %v; want error %t", tc.ctx, tc.vmID, gotErr, tc.wantErr)
			}
		})
	}
}

func TestEC2BuildletParams(t *testing.T) {
	testCases := []struct {
		desc       string
		inst       *ec2.Instance
		opts       *VMOpts
		wantURL    string
		wantPort   string
		wantCalled bool
	}{
		{
			desc: "base case",
			inst: &ec2.Instance{
				PrivateIpAddress: aws.String("9.9.9.9"),
				PublicIpAddress:  aws.String("8.8.8.8"),
			},
			opts:       &VMOpts{},
			wantCalled: true,
			wantURL:    "https://8.8.8.8",
			wantPort:   "8.8.8.8:443",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			gotURL, gotPort, gotErr := ec2BuildletParams(tc.inst, tc.opts)
			if gotErr != nil {
				t.Fatalf("ec2BuildletParams(%v, %v) failed; %v", tc.inst, tc.opts, gotErr)
			}
			if gotURL != tc.wantURL || gotPort != tc.wantPort {
				t.Errorf("ec2BuildletParams(%v, %v) = %q, %q, nil; want %q, %q, nil", tc.inst, tc.opts, gotURL, gotPort, tc.wantURL, tc.wantPort)
			}
		})
	}
}

func TestConfigureVM(t *testing.T) {
	testCases := []struct {
		desc              string
		buildEnv          *buildenv.Environment
		hconf             *dashboard.HostConfig
		hostType          string
		opts              *VMOpts
		vmName            string
		wantDesc          string
		wantImageID       string
		wantInstanceType  string
		wantName          string
		wantZone          string
		wantBuildletName  string
		wantBuildletImage string
	}{
		{
			desc:     "default-values",
			buildEnv: &buildenv.Environment{},
			hconf: &dashboard.HostConfig{
				KonletVMImage: "gcr.io/symbolic-datum-552/gobuilder-arm64-aws",
			},
			vmName:            "base_vm",
			hostType:          "host-foo-bar",
			opts:              &VMOpts{},
			wantInstanceType:  "n1-highcpu-2",
			wantName:          "base_vm",
			wantBuildletName:  "base_vm",
			wantBuildletImage: "gcr.io/symbolic-datum-552/gobuilder-arm64-aws",
		},
		{
			desc:     "full-configuration",
			buildEnv: &buildenv.Environment{},
			hconf: &dashboard.HostConfig{
				VMImage:       "awesome_image",
				KonletVMImage: "gcr.io/symbolic-datum-552/gobuilder-arm64-aws",
			},
			vmName:   "base-vm",
			hostType: "host-foo-bar",
			opts: &VMOpts{
				Zone: "sa-west",
				TLS: KeyPair{
					CertPEM: "abc",
					KeyPEM:  "xyz",
				},
				Description: "test description",
				Meta: map[string]string{
					"sample": "value",
				},
			},
			wantDesc:          "test description",
			wantImageID:       "awesome_image",
			wantInstanceType:  "n1-highcpu-2",
			wantName:          "base-vm",
			wantZone:          "sa-west",
			wantBuildletName:  "base-vm",
			wantBuildletImage: "gcr.io/symbolic-datum-552/gobuilder-arm64-aws",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := &AWSClient{}
			got := c.configureVM(tc.buildEnv, tc.hconf, tc.vmName, tc.hostType, tc.opts)
			if *got.ImageId != tc.wantImageID {
				t.Errorf("ImageId got %s; want %s", *got.ImageId, tc.wantImageID)
			}
			if *got.InstanceType != tc.wantInstanceType {
				t.Errorf("InstanceType got %s; want %s", *got.InstanceType, tc.wantInstanceType)
			}

			if *got.MinCount != 1 {
				t.Errorf("MinCount got %d; want %d", *got.MinCount, 1)
			}
			if *got.MaxCount != 1 {
				t.Errorf("MaxCount got %d; want %d", *got.MaxCount, 1)
			}
			if *got.Placement.AvailabilityZone != tc.wantZone {
				t.Errorf("AvailabilityZone got %s; want %s", *got.Placement.AvailabilityZone, tc.wantZone)
			}
			if *got.InstanceInitiatedShutdownBehavior != "terminate" {
				t.Errorf("InstanceType got %s; want %s", *got.InstanceInitiatedShutdownBehavior, "terminate")
			}
			if *got.TagSpecifications[0].ResourceType != "instance" {
				t.Errorf("Tag Resource Type got %s; want %s", *got.TagSpecifications[0].ResourceType, "instance")
			}
			if *got.TagSpecifications[0].Tags[0].Key != "Name" {
				t.Errorf("First Tag Key got %s; want %s", *got.TagSpecifications[0].Tags[0].Key, "Name")
			}
			if *got.TagSpecifications[0].Tags[0].Value != tc.wantName {
				t.Errorf("First Tag Value got %s; want %s", *got.TagSpecifications[0].Tags[0].Value, tc.wantName)
			}
			if *got.TagSpecifications[0].Tags[1].Key != "Description" {
				t.Errorf("Second Tag Key got %s; want %s", *got.TagSpecifications[0].Tags[1].Key, "Description")
			}
			if *got.TagSpecifications[0].Tags[1].Value != tc.wantDesc {
				t.Errorf("Second Tag Value got %s; want %s", *got.TagSpecifications[0].Tags[1].Value, tc.wantDesc)
			}
			gotUD := &EC2UserData{}
			gotUDJson, err := base64.StdEncoding.DecodeString(*got.UserData)
			if err != nil {
				t.Fatalf("unable to base64 decode string %q: %s", *got.UserData, err)
			}
			err = json.Unmarshal([]byte(gotUDJson), gotUD)
			if err != nil {
				t.Errorf("unable to unmarshal user data: %v", err)
			}
			if gotUD.BuildletBinaryURL != tc.hconf.BuildletBinaryURL(tc.buildEnv) {
				t.Errorf("buildletBinaryURL got %s; want %s", gotUD.BuildletBinaryURL, tc.hconf.BuildletBinaryURL(tc.buildEnv))
			}
			if gotUD.BuildletHostType != tc.hostType {
				t.Errorf("buildletHostType got %s; want %s", gotUD.BuildletHostType, tc.hostType)
			}
			if gotUD.BuildletName != tc.wantBuildletName {
				t.Errorf("buildletName got %s; want %s", gotUD.BuildletName, tc.wantBuildletName)
			}
			if gotUD.BuildletImageURL != tc.wantBuildletImage {
				t.Errorf("buildletImageURL got %s; want %s", gotUD.BuildletImageURL, tc.wantBuildletImage)
			}

			if gotUD.TLSCert != tc.opts.TLS.CertPEM {
				t.Errorf("TLSCert got %s; want %s", gotUD.TLSCert, tc.opts.TLS.CertPEM)
			}
			if gotUD.TLSKey != tc.opts.TLS.KeyPEM {
				t.Errorf("TLSKey got %s; want %s", gotUD.TLSKey, tc.opts.TLS.KeyPEM)
			}
			if gotUD.TLSPassword != tc.opts.TLS.Password() {
				t.Errorf("TLSPassword got %s; want %s", gotUD.TLSPassword, tc.opts.TLS.Password())
			}
		})
	}
}

func TestEC2Instance(t *testing.T) {
	instSample1 := &ec2.Instance{
		InstanceId: aws.String("id1"),
	}
	instSample2 := &ec2.Instance{
		InstanceId: aws.String("id2"),
	}
	resSample1 := &ec2.Reservation{
		Instances: []*ec2.Instance{
			instSample1,
		},
		RequesterId:   aws.String("user1"),
		ReservationId: aws.String("reservation12"),
	}
	resSample2 := &ec2.Reservation{
		Instances: []*ec2.Instance{
			instSample2,
		},
		RequesterId:   aws.String("user2"),
		ReservationId: aws.String("reservation22"),
	}

	testCases := []struct {
		desc     string
		dio      *ec2.DescribeInstancesOutput
		wantInst *ec2.Instance
	}{
		{
			desc: "single reservation",
			dio: &ec2.DescribeInstancesOutput{
				Reservations: []*ec2.Reservation{
					resSample1,
				},
			},
			wantInst: instSample1,
		},
		{
			desc: "multiple reservations",
			dio: &ec2.DescribeInstancesOutput{
				Reservations: []*ec2.Reservation{
					resSample2,
					resSample1,
				},
			},
			wantInst: instSample2,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			gotInst, gotErr := ec2Instance(tc.dio)
			if gotErr != nil {
				t.Errorf("ec2Instance(%v) failed: %v",
					tc.dio, gotErr)
			}
			if !cmp.Equal(gotInst, tc.wantInst) {
				t.Errorf("ec2Instance(%v) = %s; want %s",
					tc.dio, gotInst, tc.wantInst)
			}
		})
	}
}

func TestEC2InstanceError(t *testing.T) {
	testCases := []struct {
		desc string
		dio  *ec2.DescribeInstancesOutput
	}{
		{
			desc: "nil input",
			dio:  nil,
		},
		{
			desc: "nil reservation",
			dio: &ec2.DescribeInstancesOutput{
				Reservations: nil,
			},
		},
		{
			desc: "nil instances",
			dio: &ec2.DescribeInstancesOutput{
				Reservations: []*ec2.Reservation{
					&ec2.Reservation{
						Instances:     nil,
						RequesterId:   aws.String("user1"),
						ReservationId: aws.String("reservation12"),
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			_, gotErr := ec2Instance(tc.dio)
			if gotErr == nil {
				t.Errorf("ec2Instance(%v) did not fail", tc.dio)
			}
		})
	}
}

func TestEC2InstanceIPs(t *testing.T) {
	testCases := []struct {
		desc      string
		inst      *ec2.Instance
		wantIntIP string
		wantExtIP string
	}{
		{
			desc: "base case",
			inst: &ec2.Instance{
				PrivateIpAddress: aws.String("1.1.1.1"),
				PublicIpAddress:  aws.String("8.8.8.8"),
			},
			wantIntIP: "1.1.1.1",
			wantExtIP: "8.8.8.8",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			gotIntIP, gotExtIP, gotErr := ec2InstanceIPs(tc.inst)
			if gotErr != nil {
				t.Errorf("ec2InstanceIPs(%v) failed: %v",
					tc.inst, gotErr)
			}
			if gotIntIP != tc.wantIntIP || gotExtIP != tc.wantExtIP {
				t.Errorf("ec2InstanceIPs(%v) = %s, %s, %v; want %s, %s, nil",
					tc.inst, gotIntIP, gotExtIP, gotErr, tc.wantIntIP, tc.wantExtIP)
			}
		})
	}
}

func TestEC2InstanceIPsErrors(t *testing.T) {
	testCases := []struct {
		desc string
		inst *ec2.Instance
	}{
		{
			desc: "default vallues",
			inst: &ec2.Instance{},
		},
		{
			desc: "missing public ip",
			inst: &ec2.Instance{
				PrivateIpAddress: aws.String("1.1.1.1"),
			},
		},
		{
			desc: "missing private ip",
			inst: &ec2.Instance{
				PublicIpAddress: aws.String("8.8.8.8"),
			},
		},
		{
			desc: "empty public ip",
			inst: &ec2.Instance{
				PrivateIpAddress: aws.String("1.1.1.1"),
				PublicIpAddress:  aws.String(""),
			},
		},
		{
			desc: "empty private ip",
			inst: &ec2.Instance{
				PrivateIpAddress: aws.String(""),
				PublicIpAddress:  aws.String("8.8.8.8"),
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			_, _, gotErr := ec2InstanceIPs(tc.inst)
			if gotErr == nil {
				t.Errorf("ec2InstanceIPs(%v) = nil: want error", tc.inst)
			}
		})
	}
}
