// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestHostTypeToVersion(t *testing.T) {
	testCases := []struct {
		desc        string
		hostType    string
		wantVersion *Version
	}{
		{
			desc:     "valid original darwin host type",
			hostType: "host-darwin-10_14",
			wantVersion: &Version{
				Major: 10,
				Minor: 14,
				Arch:  "amd64",
			},
		},
		{
			desc:     "valid original darwin host type",
			hostType: "host-darwin-10_15",
			wantVersion: &Version{
				Major: 10,
				Minor: 15,
				Arch:  "amd64",
			},
		},
		{
			desc:     "valid newer darwin host ARM64",
			hostType: "host-darwin-arm64-11_0",
			wantVersion: &Version{
				Major: 11,
				Minor: 0,
				Arch:  "arm64",
			},
		},
		{
			desc:     "valid newer darwin host AMD64",
			hostType: "host-darwin-amd64-11_1",
			wantVersion: &Version{
				Major: 11,
				Minor: 1,
				Arch:  "amd64",
			},
		},
		{
			desc:     "valid newer darwin host AMD64",
			hostType: "host-darwin-amd64-13",
			wantVersion: &Version{
				Major: 13,
				Minor: 0,
				Arch:  "amd64",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			got, err := hostTypeToVersion(tc.hostType)
			if err != nil {
				t.Fatalf("hostTypeToVersion(%q) = %v, %s; want no error", tc.hostType, got, err)
			}
			if diff := cmp.Diff(tc.wantVersion, got); diff != "" {
				t.Errorf("hostTypeToVersion(%q) = (-want +got):\n%s", tc.hostType, diff)
			}
		})
	}
}

func TestHostTypeToVersionError(t *testing.T) {
	testCases := []struct {
		desc     string
		hostType string
		wantErr  error
	}{
		{
			desc:     "empty string",
			hostType: "",
		},
		{
			desc:     "invalid newer darwin host ARM64",
			hostType: "host-darwin-amd64-11_1-toothrot",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got, gotErr := hostTypeToVersion(tc.hostType); got != nil || gotErr == nil {
				t.Errorf("hostTypeToVersion(%q) = %+v, %s; want nil, error", tc.hostType, got, gotErr)
			}
		})
	}
}

func TestHostOnMacStadium(t *testing.T) {
	testCases := []struct {
		desc     string
		hostType string
		want     bool
	}{
		{
			desc:     "empty string",
			hostType: "",
			want:     false,
		},
		{
			desc:     "valid original darwin host",
			hostType: "host-darwin-10_14",
			want:     true,
		},
		{
			desc:     "valid newer darwin host",
			hostType: "host-darwin-amd64-11_0",
			want:     true,
		},
		{
			desc:     "invalid newer darwin host type",
			hostType: "host-darwin-arm64-11_0-toothrot",
			want:     false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := hostOnMacStadium(tc.hostType); got != tc.want {
				t.Errorf("hostOnMacStadium(%q) = %t; want %t", tc.hostType, got, tc.want)
			}
		})
	}
}

func TestVMNameReg(t *testing.T) {
	testCases := []struct {
		desc   string
		vmName string
		want   bool
	}{
		{
			desc:   "empty string",
			vmName: "",
			want:   false,
		},
		{
			desc:   "invalid original darwin host",
			vmName: "mac_10_11_host01b",
			want:   false,
		},
		{
			desc:   "valid newer darwin host",
			vmName: "mac_11_12_amd64_host01b",
			want:   true,
		},
		{
			desc:   "valid newer darwin host",
			vmName: "mac_11_20_amd64_host05a",
			want:   true,
		},
		{
			desc:   "invalid newer darwin host type",
			vmName: "host-darwin-arm64-11_0-toothrot",
			want:   false,
		},
		{
			desc:   "invalid bastion host",
			vmName: "dns_server",
			want:   false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := vmNameReg.MatchString(tc.vmName); got != tc.want {
				t.Errorf("vmNameReg.MatchString(%q) = %t; want %t", tc.vmName, got, tc.want)
			}
		})
	}
}
