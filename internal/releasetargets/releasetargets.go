// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package releasetargets

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"sync"

	"golang.org/x/build/maintner/maintnerd/maintapi/version"
)

type Target struct {
	Name         string
	GOOS, GOARCH string
	SecondClass  bool     // A port that is not a first class port. See go.dev/wiki/PortingPolicy#first-class-ports.
	ExtraEnv     []string // Extra environment variables set during toolchain build.

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
//
// TODO(dmitshur): Rename allReleases at some point. It currently tracks only
// first class ports, everything else is generated from 'go tool dist list' output
// via allPorts. The allReleases name was its original name, back when it really
// was all releases.
var allReleases = map[int]ReleaseTargets{
	22: {
		"darwin-amd64": &Target{
			MinMacOSVersion: "10.15", // go.dev/issue/57125
		},
		"darwin-arm64": &Target{
			MinMacOSVersion: "11", // Big Sur was the first release with M1 support.
		},
		"linux-386": &Target{},
		"linux-armv6l": &Target{
			GOARCH:   "arm",
			ExtraEnv: []string{"GOARM=6"},
		},
		"linux-amd64":   &Target{},
		"linux-arm64":   &Target{},
		"windows-386":   &Target{},
		"windows-amd64": &Target{},
		"windows-arm": &Target{
			SecondClass: true,
		},
		"windows-arm64": &Target{
			SecondClass: true,
		},
	},
	23: {
		"darwin-amd64": &Target{
			MinMacOSVersion: "11", // go.dev/issue/64207
		},
	},
	24: {
		"windows-arm":   nil, // not first-class, leave it to allPorts
		"windows-arm64": nil, // not first-class, leave it to allPorts
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
	for rel := range allPorts {
		releases = append(releases, rel)
	}
	sort.Ints(releases)
	return slices.Compact(releases)
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
