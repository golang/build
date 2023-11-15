// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package macservice

import (
	"time"
)

// These are minimal definitions. Many fields have been omitted since we don't
// need them yet.

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
}

type Lease struct {
	LeaseID string `json:"leaseId"`

	Expires time.Time `json:"expires"`
}
