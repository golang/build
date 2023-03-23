// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin
// +build linux darwin

package releasetargets

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"sort"
	"strings"
	"testing"

	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/coordinator/pool"
)

var update = flag.Bool("update", false, "controls whether to update releases.txt")

func TestReleaseTargets(t *testing.T) {
	out := &bytes.Buffer{}
	for _, release := range sortedReleases() {
		printRelease(out, release, TargetsForGo1Point(release))
	}
	if *update {
		if err := ioutil.WriteFile("releases.txt", out.Bytes(), 0); err != nil {
			t.Fatalf("updating golden: %v", err)
		}
		return
	}

	golden, err := ioutil.ReadFile("releases.txt")
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	if !bytes.Equal(golden, out.Bytes()) {
		t.Error("Goldens need updating. Rerun with -update.")
	}
}

func printRelease(w io.Writer, release int, targets ReleaseTargets) {
	fmt.Fprintf(w, "Targets for release 1.%v\n%s\n", release, strings.Repeat("=", 80))
	var targetNames []string
	for name := range targets {
		targetNames = append(targetNames, name)
	}
	sort.Strings(targetNames)
	for _, name := range targetNames {
		target := targets[name]
		var flags []string
		builder := target.Builder
		if target.BuildOnly {
			if builder == "" {
				builder = "(cross-compiled, no tests)"
			} else {
				flags = append(flags, "Build only")
			}

		}
		if target.Race {
			flags = append(flags, "Race enabled")
		}
		if target.LongTestBuilder != "" {
			flags = append(flags, "Long tests on "+target.LongTestBuilder)
		}
		fmt.Fprintf(w, "%-15v %-10v %-10v %v\n", name, target.GOOS, target.GOARCH, builder)
		if len(flags) != 0 {
			fmt.Fprintf(w, "\t%v\n", strings.Join(flags, ", "))
		}
		if len(target.ExtraEnv) != 0 {
			fmt.Fprintf(w, "\tExtra env: %q\n", target.ExtraEnv)
		}
		if bc, ok := dashboard.Builders[target.Builder]; ok {
			var runningOn string
			switch pool.ForHost(bc.HostConfig()).(type) {
			case *pool.EC2Buildlet:
				runningOn = "AWS"
			case *pool.GCEBuildlet:
				runningOn = "GCP"
			case *pool.ReverseBuildletPool:
				runningOn = fmt.Sprintf("reverse builder: %v", bc.HostConfig().Notes)
			default:
				runningOn = "unknown"
			}
			fmt.Fprintf(w, "\tRunning on %v\n", runningOn)
		}

		fmt.Fprintf(w, "\n")
	}
	fmt.Fprintf(w, "\n\n")
}

func TestBuildersExist(t *testing.T) {
	for _, rel := range allReleases {
		for _, target := range rel {
			if target == nil || target.Builder == "" {
				continue
			}
			_, ok := dashboard.Builders[target.Builder]
			if !ok {
				t.Errorf("missing builder: %q", target.Builder)
			}
			if _, ok := dashboard.Builders[target.LongTestBuilder]; target.LongTestBuilder != "" && !ok {
				t.Errorf("missing longtest builder: %q", target.LongTestBuilder)
			}
		}
	}
}
