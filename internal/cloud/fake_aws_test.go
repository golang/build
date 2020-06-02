// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cloud

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestFakeAWSClientInstance(t *testing.T) {
	t.Run("invalid-params", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance: %s", gotErr)
		}
		if gotInst, gotErr := f.Instance(nil, inst.ID); gotErr == nil {
			t.Errorf("Instance(nil, %s) = %+v, nil, want error", inst.ID, gotInst)
		}
		if gotInst, gotErr := f.Instance(ctx, ""); gotErr == nil {
			t.Errorf("Instance(ctx, %s) = %+v, nil, want error", "", gotInst)
		}
	})
	t.Run("existing-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		gotInst, gotErr := f.Instance(ctx, inst.ID)
		if gotErr != nil || gotInst == nil || gotInst.ID != inst.ID {
			t.Errorf("Instance(ctx, %s) = %v, %s, want %+v, nil", inst.ID, gotInst, gotErr, inst)
		}
	})
	t.Run("non-existing-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		instID := "instance-random"
		gotInst, gotErr := f.Instance(ctx, instID)
		if gotErr == nil || gotInst != nil {
			t.Errorf("Instance(ctx, %s) = %v, %s, want error", instID, gotInst, gotErr)
		}
	})
	t.Run("terminated-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		if gotErr := f.DestroyInstances(ctx, inst.ID); gotErr != nil {
			t.Fatalf("unable to destroy instance")
		}
		gotInst, gotErr := f.Instance(ctx, inst.ID)
		if gotErr != nil || gotInst == nil || gotInst.ID != inst.ID {
			t.Errorf("Instance(ctx, %s) = %v, %s, want %+v, nil", inst.ID, gotInst, gotErr, inst)
		}
	})
}

func TestFakeAWSClientRunningInstances(t *testing.T) {
	t.Run("invalid-params", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		_, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance: %s", gotErr)
		}
		if gotInst, gotErr := f.RunningInstances(nil); gotErr == nil {
			t.Errorf("RunningInstances(nil) = %+v, nil, want error", gotInst)
		}
	})
	t.Run("no-instances", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		gotInsts, gotErr := f.RunningInstances(ctx)
		if gotErr != nil {
			t.Errorf("RunningInstances() error = %v, no error", gotErr)
		}
		if !cmp.Equal(gotInsts, []*Instance{inst}) {
			t.Errorf("RunningInstances() = %+v, %s; want %+v", gotInsts, gotErr, []*Instance{inst})
		}
	})
	t.Run("single-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		gotInsts, gotErr := f.RunningInstances(ctx)
		if gotErr != nil {
			t.Errorf("RunningInstances() error = %v, no error", gotErr)
		}
		if !cmp.Equal(gotInsts, []*Instance{inst}) {
			t.Errorf("RunningInstances() = %+v, %s; want %+v", gotInsts, gotErr, []*Instance{inst})
		}
	})
	t.Run("multiple-instances", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		create := []*EC2VMConfiguration{
			generateVMConfig(),
			generateVMConfig(),
			generateVMConfig(),
		}
		insts := make([]*Instance, 0, len(create))
		for _, config := range create {
			inst, gotErr := f.CreateInstance(ctx, config)
			if gotErr != nil {
				t.Fatalf("unable to create instance")
			}
			insts = append(insts, inst)
		}
		gotInsts, gotErr := f.RunningInstances(ctx)
		if gotErr != nil {
			t.Errorf("RunningInstances() error = %v, no error", gotErr)
		}
		opt := cmpopts.SortSlices(func(i, j *Instance) bool { return i.ID < j.ID })
		if !cmp.Equal(gotInsts, insts, opt) {
			t.Errorf("RunningInstances() = %+v, %s; want %+v", gotInsts, gotErr, insts)
		}
	})

	t.Run("multiple-instances-with-one-termination", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		create := []*EC2VMConfiguration{
			generateVMConfig(),
			generateVMConfig(),
			generateVMConfig(),
		}
		insts := make([]*Instance, 0, len(create))
		for _, config := range create {
			inst, gotErr := f.CreateInstance(ctx, config)
			if gotErr != nil {
				t.Fatalf("unable to create instance")
			}
			insts = append(insts, inst)
		}
		if gotErr := f.DestroyInstances(ctx, insts[0].ID); gotErr != nil {
			t.Fatalf("unable to destroy instance")
		}
		gotInsts, gotErr := f.RunningInstances(ctx)
		if gotErr != nil {
			t.Errorf("RunningInstances() error = %v, no error", gotErr)
		}
		opt := cmpopts.SortSlices(func(i, j *Instance) bool { return i.ID < j.ID })
		if !cmp.Equal(gotInsts, insts[1:], opt) {
			t.Errorf("RunningInstances() = %+v, %s; want %+v", gotInsts, gotErr, insts[1:])
		}
	})
}

func TestFakeAWSClientCreateInstance(t *testing.T) {
	t.Run("create-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		ud := &EC2UserData{}
		config := &EC2VMConfiguration{
			Description:    "desc",
			ImageID:        "id-44",
			Name:           "name-22",
			SSHKeyID:       "key-43",
			SecurityGroups: []string{"sg-1", "sg-2"},
			Tags: map[string]string{
				"key-1": "value-1",
			},
			Type:     "ami-44",
			UserData: ud.EncodedString(),
			Zone:     "zone-14",
		}
		gotInst, gotErr := f.CreateInstance(ctx, config)
		if gotErr != nil {
			t.Fatalf("CreateInstance(ctx, %+v) = %+v, %s; want no error", config, gotInst, gotErr)
		}
		// generated fields
		if gotInst.CPUCount <= 0 {
			t.Errorf("Instance. is not set")
		}
		if gotInst.ID == "" {
			t.Errorf("Instance.ID is not set")
		}
		if gotInst.IPAddressExternal == "" {
			t.Errorf("Instance.IPAddressExternal is not set")
		}
		if gotInst.IPAddressInternal == "" {
			t.Errorf("Instance.IPAddressInternal is not set")
		}
		if gotInst.State == "" {
			t.Errorf("Instance.State is not set")
		}
		// config fields
		if gotInst.Description != config.Description {
			t.Errorf("Instance.Description = %s, want %s", gotInst.Description, config.Description)
		}
		if gotInst.ImageID != config.ImageID {
			t.Errorf("Instance.ImageID = %s, want %s", gotInst.ImageID, config.ImageID)
		}
		if gotInst.Name != config.Name {
			t.Errorf("Instance.Name = %s, want %s", gotInst.Name, config.Name)
		}
		if gotInst.SSHKeyID != config.SSHKeyID {
			t.Errorf("Instance.SSHKeyID = %s, want %s", gotInst.SSHKeyID, config.SSHKeyID)
		}
		if !cmp.Equal(gotInst.SecurityGroups, config.SecurityGroups) {
			t.Errorf("Instance.SecurityGroups = %s, want %s", gotInst.SecurityGroups, config.SecurityGroups)
		}
		if !cmp.Equal(gotInst.Tags, config.Tags) {
			t.Errorf("Instance.Tags = %+v, want %+v", gotInst.Tags, config.Tags)
		}
		if gotInst.Type != config.Type {
			t.Errorf("Instance.Type = %s, want %s", gotInst.Type, config.Type)
		}
		if gotInst.Zone != config.Zone {
			t.Errorf("Instance.Zone = %s, want %s", gotInst.Zone, config.Zone)
		}
	})
}

func TestFakeAWSClientDestroyInstances(t *testing.T) {
	t.Run("invalid-params", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance: %s", gotErr)
		}
		if gotErr := f.DestroyInstances(nil, inst.ID); gotErr == nil {
			t.Errorf("DestroyInstances(nil, %s) = nil, want error", inst.ID)
		}
		if gotErr := f.DestroyInstances(ctx); gotErr == nil {
			t.Error("DestroyInstances(ctx) = nil, want error")
		}
	})
	t.Run("destroy-existing-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		if gotErr = f.DestroyInstances(ctx, inst.ID); gotErr != nil {
			t.Errorf("DestroyInstances(ctx, %s) = %s; want no error", inst.ID, gotErr)
		}
	})
	t.Run("destroy-existing-instances", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst1, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		inst2, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		if gotErr = f.DestroyInstances(ctx, inst1.ID, inst2.ID); gotErr != nil {
			t.Errorf("DestroyInstances(ctx, %s, %s) = %s; want no error", inst1.ID, inst2.ID, gotErr)
		}
	})
	t.Run("destroy-non-existing-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		instID := "instance-random"
		if gotErr := f.DestroyInstances(ctx, instID); gotErr == nil {
			t.Errorf("DestroyInstances(ctx, %s) = %s; want error", instID, gotErr)
		}
	})
}

func TestFakeAWSClientWaitUntilInstanceRunning(t *testing.T) {
	t.Run("invalid-params", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance: %s", gotErr)
		}
		if gotErr := f.WaitUntilInstanceRunning(nil, inst.ID); gotErr == nil {
			t.Errorf("WaitUntilInstanceRunning(nil, %s) = nil, want error", inst.ID)
		}
		if gotErr := f.WaitUntilInstanceRunning(ctx, ""); gotErr == nil {
			t.Errorf("WaitUntilInstanceRunning(ctx, %s) = nil, want error", "")
		}
	})
	t.Run("wait-for-existing-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		inst, gotErr := f.CreateInstance(ctx, generateVMConfig())
		if gotErr != nil {
			t.Fatalf("unable to create instance")
		}
		if gotErr = f.WaitUntilInstanceRunning(ctx, inst.ID); gotErr != nil {
			t.Errorf("WaitUntilInstanceRunning(ctx, %s) = %s; want no error", inst.ID, gotErr)
		}
	})
	t.Run("wait-for-non-existing-instance", func(t *testing.T) {
		ctx := context.Background()
		f := NewFakeAWSClient()
		instID := "instance-random"
		if gotErr := f.WaitUntilInstanceRunning(ctx, instID); gotErr == nil {
			t.Errorf("WaitUntilInstanceRunning(ctx, %s) = %s; want error", instID, gotErr)
		}
	})
}

func TestRandIPv4(t *testing.T) {
	got := randIPv4()
	gotIP := net.ParseIP(got)
	if gotIP == nil {
		t.Errorf("randIPv4() = %v, want conforment IPv4 address", got)
	}
}

func generateVMConfig() *EC2VMConfiguration {
	return &EC2VMConfiguration{
		ImageID:  fmt.Sprintf("ami-%s", randHex(4)),
		SSHKeyID: fmt.Sprintf("key-%s", randHex(4)),
		Type:     fmt.Sprintf("type-%s", randHex(4)),
		Zone:     fmt.Sprintf("zone-%s", randHex(4)),
	}
}
