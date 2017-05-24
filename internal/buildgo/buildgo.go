// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package buildgo provides tools for pushing and building the Go
// distribution on buildlets.
package buildgo

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"golang.org/x/build/buildenv"
)

// BuilderRev is a build configuration type and a revision.
type BuilderRev struct {
	Name string // e.g. "linux-amd64-race"
	Rev  string // lowercase hex core repo git hash

	// optional sub-repository details (both must be present)
	SubName string // e.g. "net"
	SubRev  string // lowercase hex sub-repo git hash
}

func (br BuilderRev) IsSubrepo() bool {
	return br.SubName != ""
}

func (br BuilderRev) SubRevOrGoRev() string {
	if br.SubRev != "" {
		return br.SubRev
	}
	return br.Rev
}

func (br BuilderRev) RepoOrGo() string {
	if br.SubName == "" {
		return "go"
	}
	return br.SubName
}

// SnapshotObjectName is the cloud storage object name of the
// built Go tree for this builder and Go rev (not the sub-repo).
// The entries inside this tarball do not begin with "go/".
func (br *BuilderRev) SnapshotObjectName() string {
	return fmt.Sprintf("%v/%v/%v.tar.gz", "go", br.Name, br.Rev)
}

// SnapshotURL is the absolute URL of the snapshot object (see above).
func (br *BuilderRev) SnapshotURL(buildEnv *buildenv.Environment) string {
	return buildEnv.SnapshotURL(br.Name, br.Rev)
}

// snapshotExists reports whether the snapshot exists in storage.
// It returns potentially false negatives on network errors.
// Callers must not depend on this as more than an optimization.
func (br *BuilderRev) SnapshotExists(ctx context.Context, buildEnv *buildenv.Environment) bool {
	req, err := http.NewRequest("HEAD", br.SnapshotURL(buildEnv), nil)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		log.Printf("SnapshotExists check: %v", err)
		return false
	}
	return res.StatusCode == http.StatusOK
}
