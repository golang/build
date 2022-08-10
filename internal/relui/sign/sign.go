// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sign

import (
	"context"

	"golang.org/x/build/internal/relui/protos"
)

// Service interface for a release artifact signging service.
type Service interface {
	SignArtifact(ctx context.Context, workflowID, taskName string, retryCount int, bt BuildType, objectURI string) error
	ArtifactSigningStatus(ctx context.Context, workflowID, taskName string, retryCount int) (status Status, objectURI string, err error)
}

// Status of the signing request.
type Status int

const (
	StatusUnknown Status = iota
	StatusRunning
	StatusFailed
	StatusCompleted
	StatusNotFound
)

// String is the string representation for the signing request status.
func (bs Status) String() string {
	switch bs {
	case StatusRunning:
		return "Running"
	case StatusFailed:
		return "Failed"
	case StatusCompleted:
		return "Completed"
	case StatusNotFound:
		return "NotFound"
	}
	return "Unknown"
}

// BuildType is the type of build the signing request is for.
type BuildType int

const (
	BuildUnspecified BuildType = iota
	BuildMacosAMD
	BuildMacosARM
	BuildWindows
	BuildGPG
)

// proto is the corresponding protobuf definition for the signing request build type.
func (bt BuildType) proto() protos.SignArtifactRequest_BuildType {
	switch bt {
	case BuildUnspecified:
		return protos.SignArtifactRequest_BUILD_TYPE_UNSPECIFIED
	case BuildMacosAMD:
		return protos.SignArtifactRequest_BUILD_TYPE_MACOSAMD
	case BuildMacosARM:
		return protos.SignArtifactRequest_BUILD_TYPE_MACOSARM
	case BuildWindows:
		return protos.SignArtifactRequest_BUILD_TYPE_WINDOWS
	case BuildGPG:
		return protos.SignArtifactRequest_BUILD_TYPE_GPG
	}
	return protos.SignArtifactRequest_BUILD_TYPE_UNSPECIFIED
}
