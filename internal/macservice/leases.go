// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package macservice

import (
	"time"
)

// These are minimal definitions. Many fields have been omitted since we don't
// need them yet.

type LeaseRequest struct {
	VMResourceNamespace Namespace `json:"vmResourceNamespace"`

	InstanceSpecification InstanceSpecification `json:"instanceSpecification"`

	// Duration is ultimately a Duration protobuf message.
	//
	// https://pkg.go.dev/google.golang.org/protobuf@v1.31.0/types/known/durationpb#hdr-JSON_Mapping:
	// "In JSON format, the Duration type is encoded as a string rather
	// than an object, where the string ends in the suffix "s" (indicating
	// seconds) and is preceded by the number of seconds, with nanoseconds
	// expressed as fractional seconds."
	Duration string `json:"duration"`
}

type LeaseResponse struct {
	PendingLease Lease `json:"pendingLease"`
}

type RenewRequest struct {
	LeaseID string `json:"leaseId"`

	// Duration is ultimately a Duration protobuf message.
	//
	// https://pkg.go.dev/google.golang.org/protobuf@v1.31.0/types/known/durationpb#hdr-JSON_Mapping:
	// "In JSON format, the Duration type is encoded as a string rather
	// than an object, where the string ends in the suffix "s" (indicating
	// seconds) and is preceded by the number of seconds, with nanoseconds
	// expressed as fractional seconds."
	Duration string `json:"duration"`
}

type RenewResponse struct {
	Expires time.Time `json:"expires"`
}

type VacateRequest struct {
	LeaseID string `json:"leaseId"`
}

type FindRequest struct {
	VMResourceNamespace Namespace `json:"vmResourceNamespace"`
}

type FindResponse struct {
	Instances []Instance `json:"instances"`
}

type Namespace struct {
	CustomerName    string `json:"customerName"`
	ProjectName     string `json:"projectName"`
	SubCustomerName string `json:"subCustomerName"`
}

type Instance struct {
	Lease Lease `json:"lease"`

	InstanceSpecification InstanceSpecification `json:"instanceSpecification"`
}

type Lease struct {
	LeaseID string `json:"leaseId"`

	VMResourceNamespace Namespace `json:"vmResourceNamespace"`

	Expires time.Time `json:"expires"`
}

type InstanceSpecification struct {
	Profile     MachineProfile     `json:"profile"`
	AccessLevel NetworkAccessLevel `json:"accessLevel"`

	Metadata []MetadataEntry `json:"metadata"`

	DiskSelection DiskSelection `json:"diskSelection"`
}

type DiskSelection struct {
	ImageHashes ImageHashes `json:"imageHashes"`
}

type ImageHashes struct {
	BootSHA256 string `json:"bootSha256"`
}

type MachineProfile string

const (
	V1_MEDIUM_VM MachineProfile = "V1_MEDIUM_VM"
)

type NetworkAccessLevel string

const (
	GOLANG_OSS NetworkAccessLevel = "GOLANG_OSS"
)

type MetadataEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
