// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sign

import (
	"context"

	"golang.org/x/build/internal/relui/protos"
)

// Service is an interface for a release artifact signing service.
//
// Each call blocks until either the request has been acknowledged or the passed in context has been canceled.
// Setting a timeout on the context is recommended.
type Service interface {
	// SignArtifact creates a request to sign a release artifact.
	// The object URI must be URIs for file(s) on the service private GCS.
	SignArtifact(ctx context.Context, bt BuildType, objectURI []string) (jobID string, _ error)
	// ArtifactSigningStatus retrieves the status of an existing signing request,
	// or an error indicating that the status couldn't be determined.
	// If the status is completed, objectURI will be populated with the URIs for signed files in GCS.
	ArtifactSigningStatus(ctx context.Context, jobID string) (_ Status, description string, objectURI []string, _ error)
	// CancelSigning marks a previous signing request as no longer needed,
	// possibly allowing resources to be freed sooner than otherwise.
	CancelSigning(ctx context.Context, jobID string) error
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
	default:
		return "Unknown"
	}
}

// BuildType is the type of build the signing request is for.
type BuildType int

const (
	BuildUnspecified BuildType = iota

	BuildMacOS
	BuildWindows
	BuildGPG

	BuildMacOSConstructInstallerOnly
	BuildWindowsConstructInstallerOnly

	BuildMacOSBinary
	BuildWindowsBinary
)

// proto is the corresponding protobuf definition for the signing request build type.
func (bt BuildType) proto() protos.SignArtifactRequest_BuildType {
	switch bt {
	case BuildMacOS:
		return protos.SignArtifactRequest_BUILD_TYPE_MACOS
	case BuildWindows:
		return protos.SignArtifactRequest_BUILD_TYPE_WINDOWS
	case BuildGPG:
		return protos.SignArtifactRequest_BUILD_TYPE_GPG
	case BuildMacOSConstructInstallerOnly:
		return protos.SignArtifactRequest_BUILD_TYPE_MACOS_CONSTRUCT_INSTALLER_ONLY
	case BuildWindowsConstructInstallerOnly:
		return protos.SignArtifactRequest_BUILD_TYPE_WINDOWS_CONSTRUCT_INSTALLER_ONLY
	case BuildMacOSBinary:
		return protos.SignArtifactRequest_BUILD_TYPE_MACOS_BINARY
	case BuildWindowsBinary:
		return protos.SignArtifactRequest_BUILD_TYPE_WINDOWS_BINARY
	default:
		return protos.SignArtifactRequest_BUILD_TYPE_UNSPECIFIED
	}
}

func (bt BuildType) String() string {
	switch bt {
	case BuildMacOS:
		return "macOS"
	case BuildWindows:
		return "Windows"
	case BuildGPG:
		return "GPG"
	case BuildMacOSConstructInstallerOnly:
		return "macOS (construct installer only)"
	case BuildWindowsConstructInstallerOnly:
		return "Windows (construct installer only)"
	case BuildMacOSBinary:
		return "macOS (binary)"
	case BuildWindowsBinary:
		return "Windows (binary)"
	default:
		return "unspecified"
	}
}
