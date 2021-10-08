// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/cloud"
)

func TestStartNewVM(t *testing.T) {
	kp, err := NewKeyPair()
	if err != nil {
		t.Fatalf("unable to generate key pair: %s", err)
	}
	buildEnv := &buildenv.Environment{}
	hconf := &dashboard.HostConfig{
		VMImage: "image-x",
	}
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
	c := &EC2Client{
		client: cloud.NewFakeAWSClient(),
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
			c := &EC2Client{
				client: cloud.NewFakeAWSClient(),
			}
			gotClient, gotErr := c.StartNewVM(context.Background(), tc.buildEnv, tc.hconf, tc.vmName, tc.hostType, tc.opts)
			if gotErr == nil {
				t.Errorf("StartNewVM(ctx, %+v, %+v, %s, %s, %+v) = %+v, nil; want error", tc.buildEnv, tc.hconf, tc.vmName, tc.hostType, tc.opts, gotClient)
			}
			if gotClient != nil {
				t.Errorf("got %+v; expected nil", gotClient)
			}
		})
	}
}

func TestWaitUntilInstanceExists(t *testing.T) {
	vmConfig := &cloud.EC2VMConfiguration{
		ImageID: "foo",
		Type:    "type-a",
		Zone:    "eu-15",
	}
	invoked := false
	opts := &VMOpts{
		OnInstanceCreated: func() {
			invoked = true
		},
	}
	ctx := context.Background()
	c := &EC2Client{
		client: cloud.NewFakeAWSClient(),
	}
	gotVM, gotErr := c.createVM(ctx, vmConfig, opts)
	if gotErr != nil {
		t.Fatalf("createVM(ctx, %v, %v) failed with %s", vmConfig, opts, gotErr)
	}
	gotErr = c.waitUntilVMExists(ctx, gotVM.ID, opts)
	if gotErr != nil {
		t.Fatalf("WaitUntilVMExists(%v, %v, %v) failed with error %s", ctx, gotVM.ID, opts, gotErr)
	}
	if !invoked {
		t.Errorf("OnInstanceCreated() was not invoked")
	}
}

func TestCreateVM(t *testing.T) {
	vmConfig := &cloud.EC2VMConfiguration{
		ImageID: "foo",
		Type:    "type-a",
		Zone:    "eu-15",
	}
	invoked := false
	opts := &VMOpts{
		OnInstanceRequested: func() {
			invoked = true
		},
	}
	c := &EC2Client{
		client: cloud.NewFakeAWSClient(),
	}
	gotVM, gotErr := c.createVM(context.Background(), vmConfig, opts)
	if gotErr != nil {
		t.Fatalf("createVM(ctx, %v, %v) failed with %s", vmConfig, opts, gotErr)
	}
	if gotVM.ImageID != vmConfig.ImageID || gotVM.Type != vmConfig.Type || gotVM.Zone != vmConfig.Zone {
		t.Errorf("createVM(ctx, %+v, %+v) = %+v, nil; want vm to match config", vmConfig, opts, gotVM)
	}
	if !invoked {
		t.Errorf("OnInstanceRequested() was not invoked")
	}
}

func TestCreateVMError(t *testing.T) {
	testCases := []struct {
		desc     string
		vmConfig *cloud.EC2VMConfiguration
		opts     *VMOpts
	}{
		{
			desc: "missing-vmConfig",
		},
		{
			desc: "missing-image-id",
			vmConfig: &cloud.EC2VMConfiguration{
				Type: "type-a",
				Zone: "eu-15",
			},
			opts: &VMOpts{
				OnInstanceRequested: func() {},
			},
		},
		{
			desc: "missing-instance-id",
			vmConfig: &cloud.EC2VMConfiguration{
				ImageID: "foo",
				Zone:    "eu-15",
			},
			opts: &VMOpts{
				OnInstanceRequested: func() {},
			},
		},
		{
			desc: "missing-placement",
			vmConfig: &cloud.EC2VMConfiguration{
				Name: "foo",
				Type: "type-a",
			},
			opts: &VMOpts{
				OnInstanceRequested: func() {},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			c := &EC2Client{
				client: cloud.NewFakeAWSClient(),
				//client: &fakeAWSClient{},
			}
			gotVM, gotErr := c.createVM(context.Background(), tc.vmConfig, tc.opts)
			if gotErr == nil {
				t.Errorf("createVM(ctx, %v, %v) = %s, %v; want error", tc.vmConfig, tc.opts, gotVM.ID, gotErr)
			}
			if gotVM != nil {
				t.Errorf("createVM(ctx, %v, %v) = %s, %v; %q, error", tc.vmConfig, tc.opts, gotVM.ID, gotErr, "")
			}
		})
	}
}

func TestEC2BuildletParams(t *testing.T) {
	testCases := []struct {
		desc       string
		inst       *cloud.Instance
		opts       *VMOpts
		wantURL    string
		wantPort   string
		wantCalled bool
		wantErr    bool
	}{
		{
			desc: "base-case",
			inst: &cloud.Instance{
				IPAddressExternal: "8.8.8.8",
				IPAddressInternal: "3.3.3.3",
			},
			opts:       &VMOpts{},
			wantCalled: true,
			wantURL:    "https://8.8.8.8",
			wantPort:   "8.8.8.8:443",
			wantErr:    false,
		},
		{
			desc: "missing-int-ip",
			inst: &cloud.Instance{
				IPAddressExternal: "8.8.8.8",
			},
			opts:       &VMOpts{},
			wantCalled: true,
			wantURL:    "https://8.8.8.8",
			wantPort:   "8.8.8.8:443",
			wantErr:    false,
		},
		{
			desc: "missing-ext-ip",
			inst: &cloud.Instance{
				IPAddressInternal: "3.3.3.3",
			},
			opts:       &VMOpts{},
			wantCalled: true,
			wantURL:    "",
			wantPort:   "",
			wantErr:    true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			gotURL, gotPort, gotErr := ec2BuildletParams(tc.inst, tc.opts)
			if gotURL != tc.wantURL || gotPort != tc.wantPort || tc.wantErr != (gotErr != nil) {
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
			wantInstanceType:  "e2-highcpu-2",
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
			wantInstanceType:  "e2-highcpu-2",
			wantName:          "base-vm",
			wantZone:          "sa-west",
			wantBuildletName:  "base-vm",
			wantBuildletImage: "gcr.io/symbolic-datum-552/gobuilder-arm64-aws",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			got := configureVM(tc.buildEnv, tc.hconf, tc.vmName, tc.hostType, tc.opts)
			if got.ImageID != tc.wantImageID {
				t.Errorf("ImageId got %s; want %s", got.ImageID, tc.wantImageID)
			}
			if got.Type != tc.wantInstanceType {
				t.Errorf("Type got %s; want %s", got.Type, tc.wantInstanceType)
			}
			if got.Zone != tc.wantZone {
				t.Errorf("Zone got %s; want %s", got.Zone, tc.wantZone)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name got %s; want %s", got.Name, tc.wantName)
			}
			if got.Description != tc.wantDesc {
				t.Errorf("Description got %s; want %s", got.Description, tc.wantDesc)
			}
			gotUDJson, err := base64.StdEncoding.DecodeString(got.UserData)
			if err != nil {
				t.Fatalf("unable to base64 decode string %q: %s", got.UserData, err)
			}
			gotUD := &cloud.EC2UserData{}
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
