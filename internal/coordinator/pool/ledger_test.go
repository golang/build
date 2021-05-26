// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.13 && (linux || darwin)
// +build go1.13
// +build linux darwin

package pool

import (
	"context"
	"sort"
	"testing"
	"time"

	"golang.org/x/build/internal/cloud"
)

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestLedgerReserveResources(t *testing.T) {
	testCases := []struct {
		desc      string
		ctx       context.Context
		instName  string
		vmType    string
		instTypes []*cloud.InstanceType
		cpuLimit  int64
		cpuUsed   int64
		wantErr   bool
	}{
		{
			desc:     "success",
			ctx:      context.Background(),
			instName: "small-instance",
			vmType:   "aa.small",
			instTypes: []*cloud.InstanceType{
				&cloud.InstanceType{
					Type: "aa.small",
					CPU:  5,
				},
			},
			cpuLimit: 20,
			cpuUsed:  5,
			wantErr:  false,
		},
		{
			desc:     "cancelled-context",
			ctx:      canceledContext(),
			instName: "small-instance",
			vmType:   "aa.small",
			instTypes: []*cloud.InstanceType{
				&cloud.InstanceType{
					Type: "aa.small",
					CPU:  5,
				},
			},
			cpuLimit: 20,
			cpuUsed:  20,
			wantErr:  true,
		},
		{
			desc:      "unknown-instance-type",
			ctx:       context.Background(),
			instName:  "small-instance",
			vmType:    "aa.small",
			instTypes: []*cloud.InstanceType{},
			cpuLimit:  20,
			cpuUsed:   5,
			wantErr:   true,
		},
		{
			desc:     "instance-already-exists",
			ctx:      context.Background(),
			instName: "large-instance",
			vmType:   "aa.small",
			instTypes: []*cloud.InstanceType{
				&cloud.InstanceType{
					Type: "aa.small",
					CPU:  5,
				},
			},
			cpuLimit: 20,
			cpuUsed:  5,
			wantErr:  true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				cpuLimit: tc.cpuLimit,
				cpuUsed:  tc.cpuUsed,
				entries: map[string]*entry{
					"large-instance": &entry{},
				},
				types: make(map[string]*cloud.InstanceType),
			}
			l.UpdateInstanceTypes(tc.instTypes)
			gotErr := l.ReserveResources(tc.ctx, tc.instName, tc.vmType)
			if (gotErr != nil) != tc.wantErr {
				t.Errorf("ledger.reserveResources(%+v, %s, %s) = %s; want error %t", tc.ctx, tc.instName, tc.vmType, gotErr, tc.wantErr)
			}
		})
	}
}

func TestLedgerReleaseResources(t *testing.T) {
	testCases := []struct {
		desc        string
		instName    string
		entry       *entry
		cpuUsed     int64
		a1Used      int64
		wantCPUUsed int64
		wantA1Used  int64
		wantErr     bool
	}{
		{
			desc:     "success",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-x",
				vCPUCount:    10,
			},
			cpuUsed:     20,
			a1Used:      0,
			wantCPUUsed: 10,
			wantA1Used:  0,
			wantErr:     false,
		},
		{
			desc:     "entry-not-found",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-w",
				vCPUCount:    10,
			},
			cpuUsed:     20,
			a1Used:      0,
			wantCPUUsed: 20,
			wantA1Used:  0,
			wantErr:     true,
		},
		{
			desc:     "success-with-a1-instance",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-x",
				vCPUCount:    10,
				instanceType: a1MetalInstance,
			},
			cpuUsed:     20,
			a1Used:      1,
			wantCPUUsed: 10,
			wantA1Used:  0,
			wantErr:     false,
		},
		{
			desc:     "entry-not-found-with-a1-instance",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-w",
				vCPUCount:    10,
				instanceType: a1MetalInstance,
			},
			cpuUsed:     20,
			a1Used:      1,
			wantCPUUsed: 20,
			wantA1Used:  1,
			wantErr:     true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				cpuUsed:        tc.cpuUsed,
				instanceA1Used: tc.a1Used,
				entries: map[string]*entry{
					tc.entry.instanceName: tc.entry,
				},
			}
			gotErr := l.releaseResources(tc.instName)
			if (gotErr != nil) != tc.wantErr {
				t.Errorf("ledger.releaseResources(%s) = %s; want error %t", tc.instName, gotErr, tc.wantErr)
			}
			if l.cpuUsed != tc.wantCPUUsed {
				t.Errorf("ledger.cpuUsed = %d; wanted %d", l.cpuUsed, tc.wantCPUUsed)
			}
			if l.instanceA1Used != tc.wantA1Used {
				t.Errorf("ledger.instanceA1Used = %d; wanted %d", l.instanceA1Used, tc.wantA1Used)
			}
		})
	}
}

func TestLedgerAllocateResources(t *testing.T) {
	testCases := []struct {
		desc        string
		numCPU      int64
		cpuLimit    int64
		cpuUsed     int64
		a1Used      int64
		a1Limit     int64
		instName    string
		instType    string
		wantReserve bool
		wantCPUUsed int64
		wantA1Used  int64
	}{
		{
			desc:        "reservation-success",
			numCPU:      10,
			cpuLimit:    10,
			cpuUsed:     0,
			a1Used:      0,
			a1Limit:     1,
			instName:    "chacha",
			instType:    "x.type",
			wantReserve: true,
			wantCPUUsed: 10,
			wantA1Used:  0,
		},
		{
			desc:        "failed-to-reserve",
			a1Used:      0,
			a1Limit:     1,
			numCPU:      10,
			cpuLimit:    5,
			cpuUsed:     0,
			instName:    "pasa",
			instType:    "x.type",
			wantReserve: false,
			wantCPUUsed: 0,
			wantA1Used:  0,
		},
		{
			desc:        "invalid-cpu-count",
			a1Used:      0,
			a1Limit:     1,
			numCPU:      0,
			cpuLimit:    50,
			cpuUsed:     20,
			instName:    "double",
			instType:    "x.type",
			wantReserve: false,
			wantCPUUsed: 20,
			wantA1Used:  0,
		},
		{
			desc:        "reservation-success with a1.metal instance",
			numCPU:      10,
			cpuLimit:    10,
			cpuUsed:     0,
			a1Used:      0,
			a1Limit:     1,
			instName:    "chacha",
			instType:    a1MetalInstance,
			wantReserve: true,
			wantCPUUsed: 10,
			wantA1Used:  1,
		},
		{
			desc:        "failed-to-reserve with a1.metal instance",
			a1Used:      0,
			a1Limit:     1,
			numCPU:      10,
			cpuLimit:    5,
			cpuUsed:     0,
			instName:    "pasa",
			instType:    a1MetalInstance,
			wantReserve: false,
			wantCPUUsed: 0,
			wantA1Used:  0,
		},
		{
			desc:        "invalid-cpu-count with a1.metal instance",
			a1Used:      0,
			a1Limit:     10,
			numCPU:      0,
			cpuLimit:    50,
			cpuUsed:     20,
			instName:    "double",
			instType:    a1MetalInstance,
			wantReserve: false,
			wantCPUUsed: 20,
			wantA1Used:  0,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				entries:         make(map[string]*entry),
				cpuLimit:        tc.cpuLimit,
				cpuUsed:         tc.cpuUsed,
				instanceA1Limit: tc.a1Limit,
				instanceA1Used:  tc.a1Used,
			}
			gotReserve := l.allocateResources(tc.numCPU, tc.instName, tc.instType)
			if gotReserve != tc.wantReserve {
				t.Errorf("ledger.allocateResources(%d) = %v, want %v", tc.numCPU, gotReserve, tc.wantReserve)
			}
			if l.cpuUsed != tc.wantCPUUsed {
				t.Errorf("ledger.cpuUsed = %d; want %d", l.cpuUsed, tc.wantCPUUsed)
			}
			if l.instanceA1Used != tc.wantA1Used {
				t.Errorf("ledger.instanceA1Used = %d; want %d", l.instanceA1Used, tc.wantA1Used)
			}
			if _, ok := l.entries[tc.instName]; tc.wantReserve && !ok {
				t.Fatalf("ledger.entries[%s] = nil; want it to exist", tc.instName)
			}
			if e, _ := l.entries[tc.instName]; tc.wantReserve && e.vCPUCount != tc.numCPU {
				t.Fatalf("ledger.entries[%s].vCPUCount = %d; want %d", tc.instName, e.vCPUCount, tc.numCPU)
			}
		})
	}
}

func TestLedgerDeallocateCPU(t *testing.T) {
	testCases := []struct {
		desc        string
		numCPU      int64
		cpuUsed     int64
		wantCPUUsed int64
	}{
		{
			desc:        "release-success",
			numCPU:      10,
			cpuUsed:     10,
			wantCPUUsed: 0,
		},
		{
			desc:        "failed-to-release",
			numCPU:      10,
			cpuUsed:     5,
			wantCPUUsed: 5,
		},
		{
			desc:        "invalid-cpu-count",
			numCPU:      0,
			cpuUsed:     0,
			wantCPUUsed: 0,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				cpuUsed: tc.cpuUsed,
			}
			l.deallocateCPU(tc.numCPU)
			if l.cpuUsed != tc.wantCPUUsed {
				t.Errorf("ledger.cpuUsed = %d; want %d", l.cpuUsed, tc.wantCPUUsed)
			}
		})
	}
}

func TestLedgerUpdateReservation(t *testing.T) {
	testCases := []struct {
		desc     string
		instName string
		instID   string
		entry    *entry
		wantErr  bool
	}{
		{
			desc:     "success",
			instName: "inst-x",
			instID:   "id-foo-x",
			entry: &entry{
				instanceName: "inst-x",
			},
			wantErr: false,
		},
		{
			desc:     "success",
			instName: "inst-x",
			instID:   "id-foo-x",
			entry: &entry{
				instanceName: "inst-w",
			},
			wantErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				entries: map[string]*entry{
					tc.entry.instanceName: tc.entry,
				},
			}
			if gotErr := l.UpdateReservation(tc.instName, tc.instID); (gotErr != nil) != tc.wantErr {
				t.Errorf("ledger.updateReservation(%s, %s) = %s; want error %t", tc.instName, tc.instID, gotErr, tc.wantErr)
			}
			e, ok := l.entries[tc.instName]
			if !tc.wantErr && !ok {
				t.Fatalf("ledger.entries[%s] does not exist", tc.instName)
			}
			if !tc.wantErr && e.createdAt.IsZero() {
				t.Errorf("ledger.entries[%s].createdAt = %s; time not set", tc.instName, e.createdAt)
			}
		})
	}
}

func TestLedgerRemove(t *testing.T) {
	testCases := []struct {
		desc        string
		instName    string
		entry       *entry
		cpuUsed     int64
		wantCPUUsed int64
		wantErr     bool
	}{
		{
			desc:     "success",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-x",
				vCPUCount:    10,
			},
			cpuUsed:     100,
			wantCPUUsed: 90,
			wantErr:     false,
		},
		{
			desc:     "entry-does-not-exist",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-w",
				vCPUCount:    10,
			},
			cpuUsed:     100,
			wantCPUUsed: 100,
			wantErr:     true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				cpuUsed: tc.cpuUsed,
				entries: map[string]*entry{
					tc.entry.instanceName: tc.entry,
				},
			}
			if gotErr := l.Remove(tc.instName); (gotErr != nil) != tc.wantErr {
				t.Errorf("ledger.remove(%s) = %s; want error %t", tc.instName, gotErr, tc.wantErr)
			}
			if gotE, ok := l.entries[tc.instName]; ok {
				t.Errorf("ledger.entries[%s] = %+v; want it not to exist", tc.instName, gotE)
			}
			if l.cpuUsed != tc.wantCPUUsed {
				t.Errorf("ledger.cpuUsed = %d; want %d", l.cpuUsed, tc.wantCPUUsed)
			}
		})
	}
}

func TestInstanceID(t *testing.T) {
	testCases := []struct {
		desc        string
		instName    string
		entry       *entry
		cpuUsed     int64
		wantCPUUsed int64
		wantErr     bool
	}{
		{
			desc:     "success",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-x",
				vCPUCount:    10,
			},
			cpuUsed:     100,
			wantCPUUsed: 90,
			wantErr:     false,
		},
		{
			desc:     "entry-does-not-exist",
			instName: "inst-x",
			entry: &entry{
				instanceName: "inst-w",
				vCPUCount:    10,
			},
			cpuUsed:     100,
			wantCPUUsed: 100,
			wantErr:     true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				cpuUsed: tc.cpuUsed,
				entries: map[string]*entry{
					tc.entry.instanceName: tc.entry,
				},
			}
			if gotErr := l.Remove(tc.instName); (gotErr != nil) != tc.wantErr {
				t.Errorf("ledger.remove(%s) = %s; want error %t", tc.instName, gotErr, tc.wantErr)
			}
			if gotE, ok := l.entries[tc.instName]; ok {
				t.Errorf("ledger.entries[%s] = %+v; want it not to exist", tc.instName, gotE)
			}
			if l.cpuUsed != tc.wantCPUUsed {
				t.Errorf("ledger.cpuUsed = %d; want %d", l.cpuUsed, tc.wantCPUUsed)
			}
		})
	}
}

func TestLedgerSetCPULimit(t *testing.T) {
	l := &ledger{}
	var want int64 = 300
	l.SetCPULimit(300)
	if l.cpuLimit != want {
		t.Errorf("ledger.cpuLimit = %d; want %d", l.cpuLimit, want)
	}
}

func TestLedgerUpdateInstanceTypes(t *testing.T) {
	testCases := []struct {
		desc  string
		types []*cloud.InstanceType
	}{
		{"no-type", []*cloud.InstanceType{}},
		{"single-type", []*cloud.InstanceType{&cloud.InstanceType{"x", 15}}},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := newLedger()
			l.UpdateInstanceTypes(tc.types)
			for _, it := range tc.types {
				if gotV, ok := l.types[it.Type]; !ok || gotV != it {
					t.Errorf("ledger.types[%s] = %v; want %v", it.Type, gotV, it)
				}
			}
			if len(l.types) != len(tc.types) {
				t.Errorf("len(ledger.types) = %d; want %d", len(l.types), len(tc.types))
			}
		})
	}
}

func TestLedgerResources(t *testing.T) {
	testCases := []struct {
		desc          string
		entries       map[string]*entry
		cpuCount      int64
		cpuLimit      int64
		wantInstCount int64
	}{
		{"no-instances", map[string]*entry{}, 2, 3, 0},
		{"single-instance", map[string]*entry{"x": &entry{}}, 2, 3, 1},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				entries:  tc.entries,
				cpuLimit: tc.cpuLimit,
				cpuUsed:  tc.cpuCount,
			}
			gotR := l.Resources()
			if gotR.InstCount != tc.wantInstCount {
				t.Errorf("ledger.instCount = %d; want %d", gotR.InstCount, tc.wantInstCount)
			}
			if gotR.CPUUsed != tc.cpuCount {
				t.Errorf("ledger.cpuCount = %d; want %d", gotR.CPUUsed, tc.cpuCount)
			}
			if gotR.CPULimit != tc.cpuLimit {
				t.Errorf("ledger.cpuLimit = %d; want %d", gotR.CPULimit, tc.cpuLimit)
			}
		})
	}
}

func TestLedgerResourceTime(t *testing.T) {
	ct := time.Now()

	testCases := []struct {
		desc    string
		entries map[string]*entry
	}{
		{"no-instances", map[string]*entry{}},
		{"single-instance", map[string]*entry{
			"inst-x": &entry{
				createdAt:    ct,
				instanceID:   "id-x",
				instanceName: "inst-x",
				vCPUCount:    1,
			},
		}},
		{"multiple-instances", map[string]*entry{
			"inst-z": &entry{
				createdAt:    ct.Add(2 * time.Second),
				instanceID:   "id-z",
				instanceName: "inst-z",
				vCPUCount:    1,
			},
			"inst-y": &entry{
				createdAt:    ct.Add(time.Second),
				instanceID:   "id-y",
				instanceName: "inst-y",
				vCPUCount:    1,
			},
			"inst-x": &entry{
				createdAt:    ct,
				instanceID:   "id-x",
				instanceName: "inst-x",
				vCPUCount:    1,
			},
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			l := &ledger{
				entries: tc.entries,
			}
			gotRT := l.ResourceTime()
			if !sort.SliceIsSorted(gotRT, func(i, j int) bool { return gotRT[i].Creation.Before(gotRT[j].Creation) }) {
				t.Errorf("resource time is not sorted")
			}
			if len(l.entries) != len(gotRT) {
				t.Errorf("mismatch in items returned")
			}
			for _, rt := range gotRT {
				delete(l.entries, rt.Name)
			}
			if len(l.entries) != 0 {
				t.Errorf("mismatch")
			}
		})
	}
}
