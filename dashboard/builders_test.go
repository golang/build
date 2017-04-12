// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dashboard

import (
	"strings"
	"testing"
)

func TestOSARCHAccessors(t *testing.T) {
	valid := func(s string) bool { return s != "" && !strings.Contains(s, "-") }
	for _, conf := range Builders {
		os := conf.GOOS()
		arch := conf.GOARCH()
		osArch := os + "-" + arch
		if !valid(os) || !valid(arch) || !(conf.Name == osArch || strings.HasPrefix(conf.Name, osArch+"-")) {
			t.Errorf("OS+ARCH(%q) = %q, %q; invalid", conf.Name, os, arch)
		}
	}
}

func TestListTrybots(t *testing.T) {
	tryBots := TrybotBuilderNames()
	t.Logf("Builders:")
	for _, name := range tryBots {
		t.Logf("  - %s", name)
	}
}

func TestHostConfigsAllUsed(t *testing.T) {
	used := map[string]bool{}
	for _, conf := range Builders {
		used[conf.HostType] = true
	}
	for hostType := range Hosts {
		if !used[hostType] {
			// Currently host-linux-armhf-cross and host-linux-armel-cross aren't
			// referenced, but the coordinator hard-codes them, so don't make
			// this an error for now.
			t.Logf("warning: host type %q is not referenced from any build config", hostType)
		}
	}
}
