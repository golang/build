// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package releasetargets

import (
	"embed"
	"fmt"
	"io/fs"
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

	// For Darwin targets, the minimum targeted version, e.g. 10.13 or 13.
	MinMacOSVersion string
}

// ReleaseTargets maps a target name (usually but not always $GOOS-$GOARCH)
// to its target.
type ReleaseTargets map[string]*Target

type OSArch struct {
	OS, Arch string
}

func (o OSArch) String() string {
	if o.OS == "linux" && o.Arch == "arm" {
		return "linux-armv6l"
	}
	return o.OS + "-" + o.Arch
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
// propagated forward unless overridden. To stop configuring a target in a
// later release, set it to nil explicitly.
// GOOS and GOARCH will be set automatically from the target name, but can be
// overridden if necessary. Name will also be set and should not be overridden.
var allReleases = map[int]ReleaseTargets{
	19: {
		"darwin-amd64": &Target{
			Builder:         "darwin-amd64-12_0",
			Race:            true,
			MinMacOSVersion: "10.13",                                           // Issues #36025 #35459
			ExtraEnv:        []string{"CGO_CFLAGS=-mmacosx-version-min=10.13"}, // Issues #36025 #35459
		},
		"darwin-arm64": &Target{
			Builder:         "darwin-arm64-12",
			Race:            true,
			MinMacOSVersion: "11", // Big Sur was the first release with M1 support.
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
			Builder:         "linux-386-buster",
			LongTestBuilder: "linux-386-longtest",
		},
		"linux-armv6l": &Target{
			GOARCH:  "arm",
			Builder: "linux-arm-aws",
		},
		"linux-arm64": &Target{
			Builder: "linux-arm64",
		},
		"linux-amd64": &Target{
			Builder:         "linux-amd64-buster",
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
		// *-oldcc amd64/386 builders needed for 1.18, 1.19
		"windows-386": &Target{
			Builder: "windows-386-2008-oldcc",
		},
		"windows-amd64": &Target{
			Builder:         "windows-amd64-2008-oldcc",
			LongTestBuilder: "windows-amd64-longtest-oldcc",
			Race:            true,
		},
		"windows-arm64": &Target{
			SecondClass: true,
			Builder:     "windows-arm64-11",
		},
	},
	20: {
		// 1.20 drops Race .as from the distribution.
		"darwin-amd64": &Target{
			Builder:         "darwin-amd64-13",
			MinMacOSVersion: "10.13",                                           // Issues #36025 #35459
			ExtraEnv:        []string{"CGO_CFLAGS=-mmacosx-version-min=10.13"}, // Issues #36025 #35459
		},
		"darwin-arm64": &Target{
			Builder:         "darwin-arm64-12",
			MinMacOSVersion: "11", // Big Sur was the first release with M1 support.
		},
		"freebsd-386": &Target{
			SecondClass: true,
			Builder:     "freebsd-386-13_0",
		},
		"freebsd-amd64": &Target{
			SecondClass: true,
			Builder:     "freebsd-amd64-13_0",
		},
		"linux-386": &Target{
			Builder:         "linux-386-bullseye",
			LongTestBuilder: "linux-386-longtest",
		},
		"linux-amd64": &Target{
			Builder:         "linux-amd64-bullseye",
			LongTestBuilder: "linux-amd64-longtest",
		},
		"windows-386": &Target{
			Builder: "windows-386-2008",
		},
		"windows-amd64": &Target{
			Builder:         "windows-amd64-2008",
			LongTestBuilder: "windows-amd64-longtest",
		},
	},
	21: {
		"darwin-amd64": &Target{
			Builder:         "darwin-amd64-13",
			MinMacOSVersion: "10.15", // go.dev/issue/57125
		},
		"linux-s390x":   nil,
		"linux-ppc64le": nil,
		"windows-386": &Target{
			Builder: "windows-386-2016",
		},
		"windows-amd64": &Target{
			Builder:         "windows-amd64-2016",
			LongTestBuilder: "windows-amd64-longtest",
		},
		"windows-arm": &Target{
			Builder:     "windows-arm64-11", // Windows builds need a builder to create their MSIs.
			SecondClass: true,
			BuildOnly:   true,
		},
	},
}

//go:generate ./genlatestports.bash
//go:embed allports/*.txt
var allPortsFS embed.FS
var allPorts = map[int][]OSArch{}

func init() {
	for _, targets := range allReleases {
		for name, target := range targets {
			if target == nil {
				continue
			}
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

			if (target.MinMacOSVersion != "") != (target.GOOS == "darwin") {
				panic("must set MinMacOSVersion in target " + target.Name)
			}
		}
	}
	files, err := allPortsFS.ReadDir("allports")
	if err != nil {
		panic(err)
	}
	for _, f := range files {
		major := 0
		if n, err := fmt.Sscanf(f.Name(), "go1.%d.txt", &major); err != nil || n == 0 {
			panic("failed to parse filename " + f.Name())
		}
		body, err := fs.ReadFile(allPortsFS, "allports/"+f.Name())
		if err != nil {
			panic(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			os, arch, _ := strings.Cut(line, "/")
			allPorts[major] = append(allPorts[major], OSArch{os, arch})
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

var unbuildableOSs = map[string]bool{
	"android": true,
	"ios":     true,
	"js":      true,
	"wasip1":  true,
}

// TargetsForGo1Point returns the ReleaseTargets that apply to the given
// version.
func TargetsForGo1Point(x int) ReleaseTargets {
	targets := ReleaseTargets{}
	var ports []OSArch
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
		if p, ok := allPorts[release]; ok {
			ports = p
		}
	}
	for _, osarch := range ports {
		_, unbuildable := unbuildableOSs[osarch.OS]
		_, exists := targets[osarch.String()]
		if unbuildable || exists {
			continue
		}
		targets[osarch.String()] = &Target{
			Name:        osarch.String(),
			GOOS:        osarch.OS,
			GOARCH:      osarch.Arch,
			SecondClass: true,
			BuildOnly:   true,
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
