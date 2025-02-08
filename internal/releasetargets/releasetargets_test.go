// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package releasetargets

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "controls whether to update releases.txt")

func TestReleaseTargets(t *testing.T) {
	releases := sortedReleases()
	if len(releases) < 3 {
		t.Errorf("sortedReleases returned %v (len %d); allFirstClass map and allports/go1.n.txt files are expected to cover a minimum of 3 releases (prev + curr + tip)", releases, len(releases))
	}
	var out bytes.Buffer
	for _, rel := range releases {
		printRelease(&out, rel, TargetsForGo1Point(rel))
	}
	if *update {
		if err := os.WriteFile("releases.txt", out.Bytes(), 0); err != nil {
			t.Fatalf("updating golden: %v", err)
		}
		return
	}

	golden, err := os.ReadFile("releases.txt")
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
		const builder = "(cross-compiled via distpack)"
		target := targets[name]
		var flags []string
		if !target.SecondClass {
			flags = append(flags, "First class port")
		}
		if target.MinMacOSVersion != "" {
			flags = append(flags, "Minimum macOS version is "+target.MinMacOSVersion)
		}
		fmt.Fprintf(w, "%-15v %-10v %-10v %v\n", name, target.GOOS, target.GOARCH, builder)
		if len(flags) != 0 {
			fmt.Fprintf(w, "\t%v\n", strings.Join(flags, ", "))
		}
		if len(target.ExtraEnv) != 0 {
			fmt.Fprintf(w, "\tExtra env: %q\n", target.ExtraEnv)
		}

		fmt.Fprintf(w, "\n")
	}
	fmt.Fprintf(w, "\n\n")
}
