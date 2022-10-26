// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package releasetargets

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"golang.org/x/build/maintner/maintnerd/maintapi/version"
)

type Target struct {
	Name            string
	GOOS, GOARCH    string
	SecondClass     bool
	Builder         string
	BuildOnly       bool
	LongTestBuilder string
	Race            bool
	ExtraEnv        []string // Extra environment variables set during toolchain build.
}

// ReleaseTargets maps a target name (usually but not always $GOOS-$GOARCH)
// to its target.
type ReleaseTargets map[string]*Target

type OSArch struct {
	OS, Arch string
}

func (rt ReleaseTargets) FirstClassPorts() map[OSArch]bool {
	result := map[OSArch]bool{}
	for _, target := range rt {
		if !target.SecondClass {
			result[OSArch{target.GOOS, target.GOARCH}] = true
		}
	}
	return result
}

// allReleases contains all the targets for all releases we're currently
// supporting. To reduce duplication, targets from earlier versions are
// propagated forward unless overridden. To remove a target in a later release,
// set it to nil explicitly.
// GOOS and GOARCH will be set automatically from the target name, but can be
// overridden if necessary. Name will also be set and should not be overridden.
var allReleases = map[int]ReleaseTargets{
	18: {
		"darwin-amd64": &Target{
			Builder:  "darwin-amd64-12_0",
			Race:     true,
			ExtraEnv: []string{"CGO_CFLAGS=-mmacosx-version-min=10.13"}, // Issues #36025 #35459
		},
		"darwin-arm64": &Target{
			Builder: "darwin-arm64-12",
			Race:    true,
		},
		"freebsd-386": &Target{
			SecondClass: true,
			Builder:     "freebsd-386-12_3",
		},
		"freebsd-amd64": &Target{
			SecondClass: true,
			Builder:     "freebsd-amd64-12_3",
			Race:        true,
		},
		"linux-386": &Target{
			Builder:         "linux-386-stretch",
			LongTestBuilder: "linux-386-longtest",
		},
		"linux-armv6l": &Target{
			GOARCH:  "arm",
			Builder: "linux-arm-aws",
		},
		"linux-arm64": &Target{
			Builder: "linux-arm64-aws",
		},
		"linux-amd64": &Target{
			Builder:         "linux-amd64-stretch",
			LongTestBuilder: "linux-amd64-longtest",
			Race:            true,
		},
		"linux-s390x": &Target{
			SecondClass: true,
			Builder:     "linux-s390x-crosscompile",
			BuildOnly:   true,
		},
		"linux-ppc64le": &Target{
			SecondClass: true,
			Builder:     "linux-ppc64le-buildlet",
			BuildOnly:   true,
		},
		"windows-386": &Target{
			Builder: "windows-386-2008",
		},
		"windows-amd64": &Target{
			Builder:         "windows-amd64-2008",
			LongTestBuilder: "windows-amd64-longtest",
			Race:            true,
		},
		"windows-arm64": &Target{
			SecondClass: true,
			Builder:     "windows-arm64-11",
		},
	},
	19: {
		"linux-386": &Target{
			Builder:         "linux-386-buster",
			LongTestBuilder: "linux-386-longtest",
		},
		"linux-amd64": &Target{
			Builder:         "linux-amd64-buster",
			LongTestBuilder: "linux-amd64-longtest",
			Race:            true,
		},
	},
	20: {
		"linux-386": &Target{
			Builder:         "linux-386-bullseye",
			LongTestBuilder: "linux-386-longtest",
		},
		"linux-amd64": &Target{
			Builder:         "linux-amd64-bullseye",
			LongTestBuilder: "linux-amd64-longtest",
			Race:            true,
		},
	},
}

func init() {
	for _, targets := range allReleases {
		for name, target := range targets {
			if target.Name != "" {
				panic(fmt.Sprintf("target.Name in %q should be left inferred", name))
			}
			target.Name = name
			parts := strings.SplitN(name, "-", 2)
			if target.GOOS == "" {
				target.GOOS = parts[0]
			}
			if target.GOARCH == "" {
				target.GOARCH = parts[1]
			}
		}
	}
}

func sortedReleases() []int {
	var releases []int
	for rel := range allReleases {
		releases = append(releases, rel)
	}
	sort.Ints(releases)
	return releases
}

// TargetsForGo1Point returns the ReleaseTargets that apply to the given
// version.
func TargetsForGo1Point(x int) ReleaseTargets {
	targets := ReleaseTargets{}
	for _, release := range sortedReleases() {
		if release > x {
			break
		}
		for osarch, target := range allReleases[release] {
			if target == nil {
				delete(targets, osarch)
			} else {
				copy := *target
				targets[osarch] = &copy
			}
		}
	}
	return targets
}

// TargetsForVersion returns the ReleaseTargets for a given Go version string,
// e.g. go1.18.1.
func TargetsForVersion(versionStr string) (ReleaseTargets, bool) {
	x, ok := version.Go1PointX(versionStr)
	if !ok {
		return nil, false
	}
	return TargetsForGo1Point(x), true
}

var latestFCPs map[OSArch]bool
var latestFCPsOnce sync.Once

// LatestFirstClassPorts returns the first class ports in the upcoming release.
func LatestFirstClassPorts() map[OSArch]bool {
	latestFCPsOnce.Do(func() {
		rels := sortedReleases()
		latest := rels[len(rels)-1]
		latestFCPs = TargetsForGo1Point(latest).FirstClassPorts()
	})
	return latestFCPs
}

// IsFirstClass reports whether the given port is first class in the upcoming
// release.
func IsFirstClass(os, arch string) bool {
	return LatestFirstClassPorts()[OSArch{os, arch}]
}
