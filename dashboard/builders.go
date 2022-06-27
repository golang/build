// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package dashboard contains shared configuration and logic used by various
// pieces of the Go continuous build system.
package dashboard

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/internal/gophers"
	"golang.org/x/build/maintner/maintnerd/maintapi/version"
	"golang.org/x/build/types"
)

// slowBotAliases maps short names from TRY= comments to which builder to run.
//
// TODO: we'll likely expand this, or move it, or change the matching
// syntax entirely. This is a first draft.
var slowBotAliases = map[string]string{
	// Known missing builders:
	"ios-amd64": "", // There is no builder for the iOS Simulator. See issues 42100 and 42177.

	"386":                  "linux-386",
	"aix":                  "aix-ppc64",
	"amd64":                "linux-amd64",
	"android":              "android-amd64-emu",
	"android-386":          "android-386-emu",
	"android-amd64":        "android-amd64-emu",
	"android-arm":          "android-arm-corellium",
	"android-arm64":        "android-arm64-corellium",
	"arm":                  "linux-arm-aws",
	"arm64":                "linux-arm64-aws",
	"darwin":               "darwin-amd64-12_0",
	"darwin-amd64":         "darwin-amd64-12_0",
	"darwin-arm64":         "darwin-arm64-12",
	"ios-arm64":            "ios-arm64-corellium",
	"dragonfly":            "dragonfly-amd64",
	"freebsd":              "freebsd-amd64-13_0",
	"freebsd-386":          "freebsd-386-13_0",
	"freebsd-amd64":        "freebsd-amd64-13_0",
	"freebsd-arm":          "freebsd-arm-paulzhol",
	"freebsd-arm64":        "freebsd-arm64-dmgk",
	"illumos":              "illumos-amd64",
	"ios":                  "ios-arm64-corellium",
	"js":                   "js-wasm",
	"linux":                "linux-amd64",
	"linux-arm":            "linux-arm-aws",
	"linux-arm64":          "linux-arm64-aws",
	"linux-loong64":        "linux-loong64-3a5000",
	"linux-mips":           "linux-mips-rtrk",
	"linux-mips64":         "linux-mips64-rtrk",
	"linux-mips64le":       "linux-mips64le-mengzhuo",
	"linux-mipsle":         "linux-mipsle-rtrk",
	"linux-ppc64":          "linux-ppc64-buildlet",
	"linux-ppc64le":        "linux-ppc64le-buildlet",
	"linux-ppc64le-power9": "linux-ppc64le-power9osu",
	"linux-riscv64":        "linux-riscv64-unmatched",
	"linux-s390x":          "linux-s390x-ibm",
	"longtest":             "linux-amd64-longtest",
	"loong64":              "linux-loong64-3a5000",
	"mac":                  "darwin-amd64-10_14",
	"macos":                "darwin-amd64-10_14",
	"mips":                 "linux-mips-rtrk",
	"mips64":               "linux-mips64-rtrk",
	"mips64le":             "linux-mips64le-mengzhuo",
	"mipsle":               "linux-mipsle-rtrk",
	"netbsd":               "netbsd-amd64-9_0",
	"netbsd-386":           "netbsd-386-9_0",
	"netbsd-amd64":         "netbsd-amd64-9_0",
	"netbsd-arm":           "netbsd-arm-bsiegert",
	"netbsd-arm64":         "netbsd-arm64-bsiegert",
	"nocgo":                "linux-amd64-nocgo",
	"openbsd":              "openbsd-amd64-68",
	"openbsd-386":          "openbsd-386-68",
	"openbsd-amd64":        "openbsd-amd64-68",
	"openbsd-arm":          "openbsd-arm-jsing",
	"openbsd-arm64":        "openbsd-arm64-jsing",
	"openbsd-mips64":       "openbsd-mips64-jsing",
	"plan9":                "plan9-arm",
	"plan9-386":            "plan9-386-0intro",
	"plan9-amd64":          "plan9-amd64-0intro",
	"ppc64":                "linux-ppc64-buildlet",
	"ppc64le":              "linux-ppc64le-buildlet",
	"ppc64lep9":            "linux-ppc64le-power9osu",
	"riscv64":              "linux-riscv64-unmatched",
	"s390x":                "linux-s390x-ibm",
	"solaris":              "solaris-amd64-oraclerel",
	"solaris-amd64":        "solaris-amd64-oraclerel",
	"wasm":                 "js-wasm",
	"windows":              "windows-amd64-2016",
	"windows-386":          "windows-386-2008",
	"windows-amd64":        "windows-amd64-2016",
	"windows-arm":          "windows-arm-zx2c4",
	"windows-arm64":        "windows-arm64-11",
}

// Builders are the different build configurations.
// The keys are like "darwin-amd64" or "linux-386-387".
// This map should not be modified by other packages.
// Initialization happens below, via calls to addBuilder.
var Builders = map[string]*BuildConfig{}

// Hosts contains the names and configs of all the types of
// buildlets. They can be VMs, containers, or dedicated machines.
var Hosts = map[string]*HostConfig{
	"host-linux-bullseye": &HostConfig{
		Notes:           "Debian Bullseye",
		ContainerImage:  "linux-x86-bullseye:latest",
		buildletURLTmpl: "https://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-bullseye-morecpu": &HostConfig{
		Notes:           "Debian Bullseye, but on e2-highcpu-16",
		ContainerImage:  "linux-x86-bullseye:latest",
		machineType:     "e2-highcpu-16", // 16 vCPUs, 16 GB mem
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-buster": &HostConfig{
		Notes:           "Debian Buster",
		ContainerImage:  "linux-x86-buster:latest",
		buildletURLTmpl: "https://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-stretch": &HostConfig{
		Notes:           "Debian Stretch",
		ContainerImage:  "linux-x86-stretch:latest",
		machineType:     "e2-standard-4", // 4 vCPUs, 16 GB mem
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-stretch-vmx": &HostConfig{
		Notes:           "Debian Stretch w/ Nested Virtualization (VMX CPU bit) enabled, for testing",
		ContainerImage:  "linux-x86-stretch:latest",
		machineType:     "n2-highcpu-4", // e2 instances do not support MinCPUPlatform or NestedVirt.
		NestedVirt:      true,
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-armhf-cross": &HostConfig{
		Notes:           "Debian with armhf cross-compiler, built from env/crosscompile/linux-armhf",
		ContainerImage:  "linux-armhf-cross:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-armel-cross": &HostConfig{
		Notes:           "Debian with armel cross-compiler, from env/crosscompile/linux-armel",
		ContainerImage:  "linux-armel-cross:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-amd64-localdev": &HostConfig{
		IsReverse:   true,
		ExpectNum:   0,
		Notes:       "for localhost development of buildlets/gomote/coordinator only",
		SSHUsername: os.Getenv("USER"),
	},
	"host-js-wasm": &HostConfig{
		Notes:           "Container with node.js for testing js/wasm.",
		ContainerImage:  "js-wasm:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-s390x-cross": &HostConfig{
		Notes:           "Container with s390x cross-compiler.",
		ContainerImage:  "linux-s390x-cross:latest",
		buildletURLTmpl: "https://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-x86-alpine": &HostConfig{
		Notes:           "Alpine container",
		ContainerImage:  "linux-x86-alpine:latest",
		buildletURLTmpl: "https://storage.googleapis.com/$BUCKET/buildlet.linux-amd64-static",
		env:             []string{"GOROOT_BOOTSTRAP=/usr/lib/go"},
		SSHUsername:     "root",
	},
	"host-linux-clang": &HostConfig{
		Notes:           "Container with clang.",
		ContainerImage:  "linux-x86-clang:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-sid": &HostConfig{
		Notes:           "Debian sid, updated occasionally.",
		ContainerImage:  "linux-x86-sid:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-fedora": &HostConfig{
		Notes:           "Fedora 30",
		ContainerImage:  "linux-x86-fedora:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/goboot"},
		SSHUsername:     "root",
	},
	"host-linux-riscv64-joelsing": &HostConfig{
		Notes:     "SiFive HiFive Unleashed RISC-V board. 8 GB RAM, 4 cores.",
		IsReverse: true,
		ExpectNum: 1,
		Owners:    []*gophers.Person{gh("4a6f656c")},
		env:       []string{"GOROOT_BOOTSTRAP=/usr/local/goboot"},
	},
	"host-linux-riscv64-unmatched": &HostConfig{
		Notes:     "SiFive HiFive Unmatched RISC-V board. 16 GB RAM, 4 cores.",
		IsReverse: true,
		ExpectNum: 2,
		Owners:    []*gophers.Person{gh("mengzhuo")},
		env:       []string{"GOROOT_BOOTSTRAP=/usr/local/goboot"},
	},
	"host-openbsd-amd64-68": &HostConfig{
		VMImage:            "openbsd-amd64-68-v3", // v3 adds 009_exit syspatch; see golang.org/cl/278732.
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-amd64-go1_12.tar.gz",
		Notes:              "OpenBSD 6.8 (with 009_exit syspatch); GCE VM is built from script in build/env/openbsd-amd64",
		SSHUsername:        "gopher",
	},
	"host-openbsd-386-68": &HostConfig{
		VMImage:            "openbsd-386-68-v3", // v3 adds 009_exit syspatch; see golang.org/cl/278732.
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-386-go1_12.tar.gz",
		Notes:              "OpenBSD 6.8 (with 009_exit syspatch); GCE VM is built from script in build/env/openbsd-386",
		SSHUsername:        "gopher",
	},
	"host-openbsd-amd64-70": &HostConfig{
		VMImage:            "openbsd-amd64-70",
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-amd64-go1_12.tar.gz",
		Notes:              "OpenBSD 7.0; GCE VM is built from script in build/env/openbsd-amd64. n2-highcpu host.",
		SSHUsername:        "gopher",
	},
	"host-openbsd-386-70": &HostConfig{
		VMImage:            "openbsd-386-70",
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-386-go1_12.tar.gz",
		Notes:              "OpenBSD 7.0; GCE VM is built from script in build/env/openbsd-386. n2-highcpu host.",
		SSHUsername:        "gopher",
	},
	"host-openbsd-386-70-n2d": &HostConfig{
		// This host config is only for the runtime team to use investigating golang/go#49209.
		VMImage:            "openbsd-386-70",
		machineType:        "n2d-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-386-go1_12.tar.gz",
		Notes:              "OpenBSD 7.0; GCE VM is built from script in build/env/openbsd-386. n2d-highcpu host.",
		SSHUsername:        "gopher",
	},
	"host-openbsd-arm-joelsing": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		env:       []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		Owners:    []*gophers.Person{gh("4a6f656c")},
	},
	"host-openbsd-arm64-joelsing": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		env:       []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		Owners:    []*gophers.Person{gh("4a6f656c")},
	},
	"host-openbsd-mips64-joelsing": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		env:       []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		Owners:    []*gophers.Person{gh("4a6f656c")},
	},
	"host-freebsd-11_2": &HostConfig{
		VMImage:            "freebsd-amd64-112",
		Notes:              "FreeBSD 11.2; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-freebsd-11_4": &HostConfig{
		VMImage:            "freebsd-amd64-114",
		Notes:              "FreeBSD 11.4; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-freebsd-12_3": &HostConfig{
		VMImage:            "freebsd-amd64-123-stable-20211230",
		Notes:              "FreeBSD 12.3; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "e2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-freebsd-13_0": &HostConfig{
		VMImage:            "freebsd-amd64-130-stable-20211230",
		Notes:              "FreeBSD 13.0; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "e2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-freebsd-13_0-big": &HostConfig{
		VMImage:            "freebsd-amd64-130-stable-20211230",
		Notes:              "Same as host-freebsd-13_0, but on e2-standard-4",
		machineType:        "e2-standard-4", // 4 vCPUs, 16 GB mem
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-netbsd-amd64-9_0": &HostConfig{
		VMImage:            "netbsd-amd64-9-0-2019q4",
		Notes:              "NetBSD 9.0; GCE VM is built from script in build/env/netbsd-amd64. n2-highcpu host.",
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.netbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-netbsd-amd64-2da6b33.tar.gz",
		SSHUsername:        "root",
	},
	"host-netbsd-386-9_0": &HostConfig{
		VMImage:            "netbsd-i386-9-0-2019q4",
		Notes:              "NetBSD 9.0; GCE VM is built from script in build/env/netbsd-386. n2-highcpu host.",
		machineType:        "n2-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.netbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-netbsd-386-0b3b511.tar.gz",
		SSHUsername:        "root",
	},
	"host-netbsd-arm-bsiegert": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		env:       []string{"GOROOT_BOOTSTRAP=/usr/pkg/go112"},
		Owners:    []*gophers.Person{gh("bsiegert")},
	},
	"host-netbsd-arm64-bsiegert": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		env:       []string{"GOROOT_BOOTSTRAP=/usr/pkg/go114"},
		Owners:    []*gophers.Person{gh("bsiegert")},
	},
	"host-dragonfly-amd64-master": &HostConfig{
		IsReverse:   true,
		ExpectNum:   1,
		Notes:       "DragonFly BSD master, run by DragonFly team",
		env:         []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		SSHUsername: "root",
		Owners:      []*gophers.Person{gh("tuxillo")},
	},
	"host-freebsd-arm-paulzhol": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "Cubiboard2 1Gb RAM dual-core Cortex-A7 (Allwinner A20), FreeBSD 11.1-RELEASE",
		env:       []string{"GOROOT_BOOTSTRAP=/usr/home/paulzhol/go1.4"},
		Owners:    []*gophers.Person{gh("paulzhol")},
	},
	"host-freebsd-arm64-dmgk": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "AWS EC2 a1.large 2 vCPU 4GiB RAM, FreeBSD 12.1-STABLE",
		env:       []string{"GOROOT_BOOTSTRAP=/usr/home/builder/gobootstrap"},
		Owners:    []*gophers.Person{gh("dmgk")},
	},
	"host-plan9-arm-0intro": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "Raspberry Pi 3 Model B, Plan 9 from Bell Labs",
		Owners:    []*gophers.Person{gh("0intro")},
	},
	"host-plan9-amd64-0intro": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "QEMU VM, Plan 9 from Bell Labs, 9k kernel",
		Owners:    []*gophers.Person{gh("0intro")},
	},
	"host-plan9-386-0intro": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "QEMU VM, Plan 9 from Bell Labs",
		Owners:    []*gophers.Person{gh("0intro")},
	},
	"host-plan9-386-gce": &HostConfig{
		VMImage:            "plan9-386-v7",
		Notes:              "Plan 9 from 0intro; GCE VM is built from script in build/env/plan9-386",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.plan9-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-plan9-386.tar.gz",
		machineType:        "e2-highcpu-4",
		env:                []string{"GO_TEST_TIMEOUT_SCALE=3"},
	},
	"host-windows-amd64-2008": &HostConfig{
		VMImage:            "windows-amd64-server-2008r2-v7",
		machineType:        "e2-highcpu-4", // 4 vCPUs, 4 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2008-newcc": &HostConfig{
		VMImage:            "windows-amd64-server-2008r2-v8",
		machineType:        "e2-highcpu-4", // 4 vCPUs, 4 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2012": &HostConfig{
		VMImage:            "windows-amd64-server-2012r2-v7",
		machineType:        "e2-highcpu-4", // 4 vCPUs, 4 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2012-newcc": &HostConfig{
		VMImage:            "windows-amd64-server-2012r2-v8",
		machineType:        "e2-highcpu-4", // 4 vCPUs, 4 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2016": &HostConfig{
		VMImage:            "windows-amd64-server-2016-v7",
		machineType:        "e2-highcpu-4", // 4 vCPUs, 4 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2016-newcc": &HostConfig{
		VMImage:            "windows-amd64-server-2016-v8",
		machineType:        "e2-highcpu-4", // 4 vCPUs, 4 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2016-big": &HostConfig{
		Notes:              "Same as host-windows-amd64-2016, but on e2-highcpu-16",
		VMImage:            "windows-amd64-server-2016-v7",
		machineType:        "e2-highcpu-16", // 16 vCPUs, 16 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2016-big-newcc": &HostConfig{
		Notes:              "Same as host-windows-amd64-2016, but on e2-highcpu-16",
		VMImage:            "windows-amd64-server-2016-v8",
		machineType:        "e2-highcpu-16", // 16 vCPUs, 16 GB mem
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-arm64-zx2c4": &HostConfig{
		IsReverse: true,
		ExpectNum: 0,
		Owners:    []*gophers.Person{gh("zx2c4")},
		env:       []string{"GOROOT_BOOTSTRAP=C:\\Program Files (Arm)\\Go"},
	},
	"host-windows-arm64-mini": &HostConfig{
		Notes:              "macOS hosting Windows 10 in qemu with HVM acceleration.",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-arm64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-windows-arm64-f22ec5.tar.gz",
		IsReverse:          true,
		ExpectNum:          2,
	},
	"host-windows11-arm64-mini": &HostConfig{
		Notes:              "macOS hosting Windows 11 in qemu with HVM acceleration.",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-arm64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-windows-arm64-f22ec5.tar.gz",
		IsReverse:          true,
		ExpectNum:          5,
	},
	"host-darwin-10_12": &HostConfig{
		IsReverse: true,
		ExpectNum: 2,
		Notes:     "MacStadium OS X 10.12 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & let cmd/makemac recreate
	},
	"host-darwin-10_14": &HostConfig{
		IsReverse: true,
		ExpectNum: 2,
		Notes:     "MacStadium macOS Mojave (10.14) VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot", // Go 1.12.1
		},
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & let cmd/makemac recreate
	},
	"host-darwin-10_15": &HostConfig{
		IsReverse: true,
		ExpectNum: 3,
		Notes:     "MacStadium macOS Catalina (10.15) VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot", // Go 1.12.1
		},
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & let cmd/makemac recreate
	},
	"host-darwin-amd64-11_0": &HostConfig{
		IsReverse: true,
		ExpectNum: 4,
		Notes:     "MacStadium macOS Big Sur (11.0) VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot", // Go 1.13.4
		},
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & let cmd/makemac recreate
	},
	"host-darwin-amd64-12_0": &HostConfig{
		IsReverse: true,
		ExpectNum: 5,
		Notes:     "MacStadium macOS Monterey (12.0) VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot", // Go 1.17.3
		},
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & let cmd/makemac recreate
	},
	"host-darwin-arm64-11_0-toothrot": &HostConfig{
		IsReverse: true,
		Notes:     "macOS Big Sur (11) ARM64 (M1). Mac mini",
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot",
		},
		SSHUsername: "gopher",
	},
	"host-darwin-arm64-12_0-toothrot": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "macOS Big Sur (12) ARM64 (M1). Mac mini",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot",
		},
		SSHUsername: "gopher",
	},
	"host-darwin-arm64-11": &HostConfig{
		IsReverse: true,
		Notes:     "macOS Big Sur (11) ARM64 (M1). Mac mini",
		ExpectNum: 3,
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot",
		},
		SSHUsername: "gopher",
	},
	"host-darwin-arm64-12": &HostConfig{
		IsReverse: true,
		ExpectNum: 3,
		Notes:     "macOS Big Sur (12) ARM64 (M1). Mac mini",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot",
		},
		SSHUsername: "gopher",
	},

	"host-linux-s390x": &HostConfig{
		Notes:     "run by IBM",
		Owners:    []*gophers.Person{gh("jonathan-albrecht-ibm"), gophers.GetPerson("Cindy Lee")}, // See https://groups.google.com/g/golang-dev/c/obUDaYbaxXw/m/5sMgfDYVAAAJ.
		IsReverse: true,
		ExpectNum: 2, // See https://github.com/golang/go/issues/49557#issuecomment-969148789.
		env:       []string{"GOROOT_BOOTSTRAP=/var/buildlet/go-linux-s390x-bootstrap"},
	},
	"host-linux-ppc64-osu": &HostConfig{
		Notes:           "Debian jessie; run by Go team on osuosl.org",
		IsReverse:       true,
		ExpectNum:       5,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		SSHUsername:     "root",
		HermeticReverse: false, // TODO: run in chroots with overlayfs? https://github.com/golang/go/issues/34830#issuecomment-543386764
	},
	"host-linux-ppc64le-osu": &HostConfig{
		Notes:           "Debian Buster; run by Go team on osuosl.org; see x/build/env/linux-ppc64le/osuosl",
		IsReverse:       true,
		ExpectNum:       5,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		SSHUsername:     "root",
		HermeticReverse: true,
	},
	"host-linux-ppc64le-power9-osu": &HostConfig{
		Notes:           "Debian Buster; run by Go team on osuosl.org; see x/build/env/linux-ppc64le/osuosl",
		IsReverse:       true,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap", "GOPPC64=power9"},
		SSHUsername:     "root",
		HermeticReverse: true,
	},
	"host-linux-arm64-aws": &HostConfig{
		Notes:           "Debian Buster, EC2 arm64 instance. See x/build/env/linux-arm64/aws",
		VMImage:         "ami-03089323a1d38e652",
		ContainerImage:  "gobuilder-arm64-aws:latest",
		machineType:     "m6g.xlarge",
		isEC2:           true,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-arm64",
		SSHUsername:     "root",
	},
	"host-linux-arm-aws": &HostConfig{
		Notes:           "Debian Buster, EC2 arm instance. See x/build/env/linux-arm/aws",
		VMImage:         "ami-07409163bccd5ac4d",
		ContainerImage:  "gobuilder-arm-aws:latest",
		machineType:     "m6g.xlarge",
		isEC2:           true,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-arm",
		SSHUsername:     "root",
	},
	"host-illumos-amd64-jclulow": &HostConfig{
		Notes:       "SmartOS base64@19.1.0 zone",
		Owners:      []*gophers.Person{gh("jclulow")},
		IsReverse:   true,
		ExpectNum:   1,
		SSHUsername: "gobuild",
	},
	"host-solaris-oracle-amd64-oraclerel": &HostConfig{
		Notes:     "Oracle Solaris amd64 Release System",
		Owners:    []*gophers.Person{gh("rorth")}, // https://github.com/golang/go/issues/15581#issuecomment-550368581
		IsReverse: true,
		ExpectNum: 1,
		env:       []string{"GOROOT_BOOTSTRAP=/opt/golang/go-solaris-amd64-bootstrap"},
	},
	"host-linux-loong64-3a5000": &HostConfig{
		Notes:     "Loongson 3A5000 Box hosted by Loongson; loong64 is the short name of LoongArch 64 bit version",
		Owners:    []*gophers.Person{gh("XiaodongLoong")},
		IsReverse: true,
		ExpectNum: 3,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/lib/go-1.18",
		},
	},
	"host-linux-mipsle-mengzhuo": &HostConfig{
		Notes:     "Loongson 3A Box hosted by Meng Zhuo; actually MIPS64le despite the name",
		Owners:    []*gophers.Person{gh("mengzhuo")},
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/lib/golang",
			"GOMIPS64=hardfloat",
		},
	},
	"host-linux-mips64le-rtrk": &HostConfig{
		Notes:     "cavium,rhino_utm8 board hosted at RT-RK.com; quad-core cpu, 8GB of ram and 240GB ssd disks.",
		Owners:    []*gophers.Person{gh("bogojevic"), gh("milanknezevic")}, // See https://github.com/golang/go/issues/31217#issuecomment-547004892.
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap",
		},
	},
	"host-linux-mips64-rtrk": &HostConfig{
		Notes:     "cavium,rhino_utm8 board hosted at RT-RK.com; quad-core cpu, 8GB of ram and 240GB ssd disks.",
		Owners:    []*gophers.Person{gh("bogojevic"), gh("milanknezevic")}, // See https://github.com/golang/go/issues/31217#issuecomment-547004892.
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap",
		},
	},
	"host-ios-arm64-corellium-ios": &HostConfig{
		Notes:     "Virtual iOS devices hosted by Zenly on Corellium; see issues 31722 and 40523",
		Owners:    []*gophers.Person{gh("steeve"), gh("changkun")}, // See https://groups.google.com/g/golang-dev/c/oiuIE7qrWp0.
		IsReverse: true,
		ExpectNum: 3,
		env: []string{
			"GOROOT_BOOTSTRAP=/var/root/go-ios-arm64-bootstrap",
		},
	},
	"host-android-arm64-corellium-android": &HostConfig{
		Notes:     "Virtual Android devices hosted by Zenly on Corellium; see issues 31722 and 40523",
		Owners:    []*gophers.Person{gh("steeve"), gh("changkun")}, // See https://groups.google.com/g/golang-dev/c/oiuIE7qrWp0.
		IsReverse: true,
		ExpectNum: 3,
		env: []string{
			"GOROOT_BOOTSTRAP=/data/data/com.termux/files/home/go-android-arm64-bootstrap",
			// Only run one job at a time to avoid the OOM killer.
			// Issue 50084.
			"GOMAXPROCS=1",
		},
	},
	"host-aix-ppc64-osuosl": &HostConfig{
		Notes:     "AIX 7.2 VM on OSU; run by Tony Reix",
		Owners:    []*gophers.Person{gh("trex58")},
		IsReverse: true,
		ExpectNum: 1,
		env:       []string{"GOROOT_BOOTSTRAP=/opt/freeware/lib/golang"},
	},
	"host-android-amd64-emu": &HostConfig{
		Notes:           "Debian Buster w/ Android SDK + emulator (use nested virt)",
		ContainerImage:  "android-amd64-emu:bff27c0c9263",
		KonletVMImage:   "android-amd64-emu",
		NestedVirt:      true,
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-amd64-wsl": &HostConfig{
		Notes:     "Windows 10 WSL2 Ubuntu",
		Owners:    []*gophers.Person{gh("mengzhuo")},
		IsReverse: true,
		ExpectNum: 2,
		env:       []string{"GOROOT_BOOTSTRAP=/usr/lib/go"},
	},
	"host-linux-amd64-perf": &HostConfig{
		Notes:               "Cascade Lake performance testing machines",
		machineType:         "c2-standard-8", // C2 has precisely defined, consistent server architecture.
		ContainerImage:      "linux-x86-bullseye:latest",
		buildletURLTmpl:     "https://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:                 []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:         "root",
		CustomDeleteTimeout: 8 * time.Hour,
	},
}

// CrossCompileConfig describes how to cross-compile a build on a
// faster host.
type CrossCompileConfig struct {
	// CompileHostType is the host type to use for compilation
	CompileHostType string

	// CCForTarget is the CC_FOR_TARGET environment variable.
	CCForTarget string

	// GOARM is any GOARM= environment variable.
	GOARM string

	// AlwaysCrossCompile controls whether this builder always
	// cross compiles. Otherwise it's only done for trybot runs.
	AlwaysCrossCompile bool
}

func gh(githubUsername string) *gophers.Person {
	p := gophers.GetPerson("@" + githubUsername)
	if p == nil {
		panic("person with GitHub username " + githubUsername + " does not exist in the golang.org/x/build/internal/gophers package")
	}
	return p
}

func init() {
	for key, c := range Hosts {
		if key == "" {
			panic("empty string key in Hosts")
		}
		if c.HostType == "" {
			c.HostType = key
		}
		if c.HostType != key {
			panic(fmt.Sprintf("HostType %q != key %q", c.HostType, key))
		}
		nSet := 0
		if c.VMImage != "" {
			nSet++
		}
		if c.ContainerImage != "" && !c.isEC2 {
			nSet++
		}
		if c.IsReverse {
			nSet++
		}
		if nSet != 1 {
			panic(fmt.Sprintf("exactly one of VMImage, ContainerImage, IsReverse must be set for host %q; got %v", key, nSet))
		}
		if c.buildletURLTmpl == "" && (c.VMImage != "" || c.ContainerImage != "") {
			panic(fmt.Sprintf("missing buildletURLTmpl for host type %q", key))
		}
	}
}

// A HostConfig describes the available ways to obtain buildlets of
// different types. Some host configs can serve multiple
// builders. For example, a host config of "host-linux-bullseye" can
// serve linux-amd64, linux-amd64-race, linux-386, linux-386-387, etc.
type HostConfig struct {
	// HostType is the unique name of this host config. It is also
	// the key in the Hosts map.
	HostType string

	// buildletURLTmpl is the URL "template" ($BUCKET is auto-expanded)
	// for the URL to the buildlet binary.
	// This field is required for VM and Container builders. It's not
	// needed for reverse buildlets because in that case, the buildlets
	// are already running and their stage0 should know how to update it
	// it automatically.
	buildletURLTmpl string

	// Exactly 1 of these must be set (with the exception of EC2 instances).
	// An EC2 instance may run a container inside a VM. In that case, a VMImage
	// and ContainerImage will both be set.
	VMImage        string // e.g. "openbsd-amd64-60"
	ContainerImage string // e.g. "linux-buildlet-std:latest" (suffix after "gcr.io/<PROJ>/")
	IsReverse      bool   // if true, only use the reverse buildlet pool

	// GCE options, if VMImage != "" || ContainerImage != ""
	machineType    string // optional GCE instance type
	RegularDisk    bool   // if true, use spinning disk instead of SSD
	MinCPUPlatform string // optional. e2 instances are not supported. see https://cloud.google.com/compute/docs/instances/specify-min-cpu-platform.

	// EC2 options
	isEC2 bool // if true, the instance is configured to run on EC2

	// GCE or EC2 options:
	//
	// CustomDeleteTimeout is an optional custom timeout after which the VM is forcibly destroyed and the build is retried.
	// Zero duration defers decision to internal/coordinator/pool package, which is enough for longtest post-submit builds.
	// (This is generally an internal implementation detail, currently left behind only for the -perf builder.)
	CustomDeleteTimeout time.Duration

	// Reverse options
	ExpectNum       int  // expected number of reverse buildlets of this type
	HermeticReverse bool // whether reverse buildlet has fresh env per conn

	// Container image options, if ContainerImage != "":
	NestedVirt    bool   // container requires VMX nested virtualization. e2 and n2d instances are not supported.
	KonletVMImage string // optional VM image (containing konlet) to use instead of default

	// Optional base env. GOROOT_BOOTSTRAP should go here if the buildlet
	// has Go 1.4+ baked in somewhere.
	env []string

	// These template URLs may contain $BUCKET which is expanded to the
	// relevant Cloud Storage bucket as specified by the build environment.
	goBootstrapURLTmpl string // optional URL to a built Go 1.4+ tar.gz

	Owners []*gophers.Person // owners; empty means golang-dev
	Notes  string            // notes for humans

	SSHUsername string // username to ssh as, empty means not supported
}

// A BuildConfig describes how to run a builder.
type BuildConfig struct {
	// Name is the unique name of the builder, in the form of
	// "GOOS-GOARCH" or "GOOS-GOARCH-suffix". For example,
	// "darwin-386", "linux-386-387", "linux-amd64-race". Some
	// suffixes are well-known and carry special meaning, such as
	// "-race".
	Name string

	// HostType is the required key into the Hosts map, describing
	// the type of host this build will run on.
	// For example, "host-linux-bullseye".
	HostType string

	// KnownIssues is a slice of non-zero golang.org/issue/nnn numbers for a
	// builder that may fail due to a known issue, such as because it is a new
	// builder still in development/testing, or because the feature
	// or port that it's meant to test hasn't been added yet, etc.
	//
	// A non-zero value here means that failures on this builder should not
	// be considered a serious regression and don't need investigation beyond
	// what is already in scope of the listed issues.
	KnownIssues []int

	Notes string // notes for humans

	// tryBot optionally specifies a policy func for whether trybots are enabled.
	// nil means off. Even if tryBot returns true, BuildConfig.BuildsRepo must also
	// return true. See the implementation of BuildConfig.BuildsRepoTryBot.
	// The proj is "go", "net", etc. The branch is proj's branch.
	// The goBranch is the same as branch for proj "go", else it's the go branch
	// ("master, "release-branch.go1.12", etc).
	tryBot  func(proj, branch, goBranch string) bool
	tryOnly bool // only used for trybots, and not regular builds

	CompileOnly bool // if true, compile tests, but don't run them
	FlakyNet    bool // network tests are flaky (try anyway, but ignore some failures)

	// buildsRepo optionally specifies whether this builder does
	// builds (of any type) for the given repo ("go", "net", etc.)
	// and its branch ("master", "release-branch.go1.12", "dev.link", etc.).
	// goBranch is the branch of "go" to build against. If repo == "go",
	// goBranch == branch.
	//
	// If nil, a default set of repos as reported by buildRepoByDefault
	// is built. See buildsRepoAtAll method for details.
	//
	// To implement a minor change to the default policy, create a
	// function that uses buildRepoByDefault. For example:
	//
	// 	buildsRepo: func(repo, branch, goBranch string) bool {
	// 		b := buildRepoByDefault(repo)
	// 		// ... modify b from the default value as needed ...
	// 		return b
	// 	}
	//
	buildsRepo func(repo, branch, goBranch string) bool

	// RunBench enables benchmarking of the toolchain using x/benchmarks.
	// This only applies when building x/benchmarks.
	RunBench bool

	// MinimumGoVersion optionally specifies the minimum Go version
	// this builder is allowed to use. It can be useful for skipping
	// builders that are too new and no longer support some supported
	// Go versions. It doesn't need to be set for builders that support
	// all supported Go versions.
	//
	// Note: This field currently has effect on trybot runs only.
	//
	// TODO: unexport this and make buildsRepoAtAll return false on too-old
	// of repos. The callers in coordinator will need updating.
	MinimumGoVersion types.MajorMinor

	// SkipSnapshot, if true, means to not fetch a tarball
	// snapshot of the world post-make.bash from the buildlet (and
	// thus to not write it to Google Cloud Storage). This is
	// incompatible with sharded tests, and should only be used
	// for very slow builders or networks, unable to transfer
	// the tarball in under ~5 minutes.
	SkipSnapshot bool

	// StopAfterMake causes the build to stop after the make
	// script completes, returning its result as the result of the
	// whole build. It does not run or compile any of the tests,
	// nor does it write a snapshot of the world to cloud
	// storage. This option is only supported for builders whose
	// BuildConfig.SplitMakeRun returns true.
	StopAfterMake bool

	// needsGoProxy is whether this builder should have GOPROXY set.
	// Currently this is only for the longtest builder, which needs
	// to run cmd/go tests fetching from the network.
	needsGoProxy bool

	// privateGoProxy for builder has it's own Go proxy instead of
	// proxy.golang.org, after setting this builder will respect
	// GOPROXY enviroment value.
	privateGoProxy bool

	// InstallRacePackages controls which packages to "go install
	// -race <pkgs>" after running make.bash (or equivalent).  If
	// the builder ends in "-race", the default if non-nil is just
	// "std".
	InstallRacePackages []string

	// GoDeps is a list of of git sha1 commits that must be in the
	// commit to be tested's history. If absent, this builder is
	// not run for that commit.
	GoDeps []string

	// CrossCompileConfig optionally specifies whether and how
	// this build is cross compiled.
	CrossCompileConfig *CrossCompileConfig

	// distTestAdjust optionally specifies a function that can
	// adjust the cmd/dist test policy for this builder.
	//
	// The BuildConfig.ShouldRunDistTest method implements the
	// default cmd/dist test policy, and then calls distTestAdjust
	// to adjust that decision further, if distTestAdjust is not nil.
	//
	// The initial value of the run parameter is what the default
	// policy said. The returned value from distTestAdjust is what
	// BuildConfig.ShouldRunDistTest reports to the caller.
	//
	// For example:
	//
	// 	distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
	// 		// ... modify run from the initial value as needed ...
	// 		return run
	// 	}
	//
	distTestAdjust func(run bool, distTest string, isNormalTry bool) bool

	// numTestHelpers is the number of _additional_ buildlets
	// past the first one to help out with sharded tests.
	// For TryBots and SlowBots, the numTryHelpers value is used,
	// unless it's zero, in which case numTestHelpers is used.
	numTestHelpers    int
	numTryTestHelpers int // For TryBots/SlowBots. If 0, numTestHelpers is used.

	env           []string // extra environment ("key=value") pairs
	allScriptArgs []string

	testHostConf *HostConfig // override HostConfig for testing, at least for now

	// isRestricted marks if a builder should be restricted to a subset of users.
	isRestricted bool
}

// Env returns the environment variables this builder should run with.
func (c *BuildConfig) Env() []string {
	env := []string{"GO_BUILDER_NAME=" + c.Name}
	if c.FlakyNet {
		env = append(env, "GO_BUILDER_FLAKY_NET=1")
	}
	if c.IsLongTest() {
		// Set a private hook in cmd/dist to run main Go repository tests
		// without the default -short flag. See golang.org/issue/12508.
		env = append(env, "GO_TEST_SHORT=0")
	}
	env = append(env, c.HostConfig().env...)
	return append(env, c.env...)
}

// ModulesEnv returns the extra module-specific environment variables
// to append to this builder as a function of the repo being built
// ("go", "oauth2", "net", etc).
func (c *BuildConfig) ModulesEnv(repo string) (env []string) {
	// EC2 and reverse builders should set the public module proxy
	// address instead of the internal proxy.
	if (c.HostConfig().isEC2 || c.IsReverse()) && repo != "go" && !c.PrivateGoProxy() {
		env = append(env, "GOPROXY=https://proxy.golang.org")
	}
	switch repo {
	case "go":
		if !c.OutboundNetworkAllowed() {
			env = append(env, "GOPROXY=off")
		}
	case "oauth2", "build", "perf", "website":
		env = append(env, "GO111MODULE=on")
	}
	return env
}

func (c *BuildConfig) IsReverse() bool { return c.HostConfig().IsReverse }

func (c *BuildConfig) IsContainer() bool { return c.HostConfig().IsContainer() }
func (c *HostConfig) IsContainer() bool  { return c.ContainerImage != "" }

func (c *BuildConfig) IsVM() bool { return c.HostConfig().IsVM() }

// IsVM reports whether the instance running the job is ultimately a VM. Hosts where
// a VM is used only to initiate a container are considered a container, not a VM.
// EC2 instances may be configured to run in containers that are running
// on custom AMIs.
func (c *HostConfig) IsVM() bool {
	if c.isEC2 {
		return c.VMImage != "" && c.ContainerImage == ""
	}
	return c.VMImage != ""
}

func (c *BuildConfig) GOOS() string { return c.Name[:strings.Index(c.Name, "-")] }

func (c *BuildConfig) GOARCH() string {
	arch := c.Name[strings.Index(c.Name, "-")+1:]
	i := strings.Index(arch, "-")
	if i == -1 {
		return arch
	}
	return arch[:i]
}

// MatchesSlowBotTerm reports whether some provided term from a
// TRY=... comment on a Run-TryBot+1 vote on Gerrit should match this
// build config.
func (c *BuildConfig) MatchesSlowBotTerm(term string) bool {
	return term != "" && (term == c.Name || slowBotAliases[term] == c.Name)
}

// FilePathJoin is mostly like filepath.Join (without the cleaning) except
// it uses the path separator of c.GOOS instead of the host system's.
func (c *BuildConfig) FilePathJoin(x ...string) string {
	if c.GOOS() == "windows" {
		return strings.Join(x, "\\")
	}
	return strings.Join(x, "/")
}

func (c *BuildConfig) IsRestricted() bool {
	return c.isRestricted
}

// DistTestsExecTimeout returns how long the coordinator should wait
// for a cmd/dist test execution to run the provided dist test names.
func (c *BuildConfig) DistTestsExecTimeout(distTests []string) time.Duration {
	// TODO: consider using distTests? We never did before, but
	// now we have the TestStats in the coordinator. Pass in a
	// *buildstats.TestStats and use historical data times some
	// fudge factor? For now just use the old 20 minute limit
	// we've used since 2014, but scale it by the
	// GO_TEST_TIMEOUT_SCALE for the super slow builders which
	// struggle with, say, the cgo tests. (which should be broken
	// up into separate dist tests or shards, like the test/ dir
	// was)
	d := 20 * time.Minute
	d *= time.Duration(c.timeoutScale())
	return d
}

// timeoutScale returns this builder's GO_TEST_TIMEOUT_SCALE value, or 1.
func (c *BuildConfig) timeoutScale() int {
	const pfx = "GO_TEST_TIMEOUT_SCALE="
	for _, env := range [][]string{c.env, c.HostConfig().env} {
		for _, kv := range env {
			if strings.HasPrefix(kv, pfx) {
				if n, err := strconv.Atoi(kv[len(pfx):]); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 1
}

// HostConfig returns the host configuration of c.
func (c *BuildConfig) HostConfig() *HostConfig {
	if c.testHostConf != nil {
		return c.testHostConf
	}
	if c, ok := Hosts[c.HostType]; ok {
		return c
	}
	panic(fmt.Sprintf("missing buildlet config for buildlet %q", c.Name))
}

// GoBootstrapURL returns the URL of a built Go 1.4+ tar.gz for the
// build configuration type c, or empty string if there isn't one.
func (c *BuildConfig) GoBootstrapURL(e *buildenv.Environment) string {
	return strings.Replace(c.HostConfig().goBootstrapURLTmpl, "$BUCKET", e.BuildletBucket, 1)
}

// BuildletBinaryURL returns the public URL of this builder's buildlet.
func (c *HostConfig) BuildletBinaryURL(e *buildenv.Environment) string {
	tmpl := c.buildletURLTmpl
	return strings.Replace(tmpl, "$BUCKET", e.BuildletBucket, 1)
}

func (c *BuildConfig) IsRace() bool {
	return strings.HasSuffix(c.Name, "-race")
}

// IsLongTest reports whether this is a longtest builder.
// A longtest builder runs tests without the -short flag.
//
// A builder is considered to be a longtest builder
// if and only if its name ends with "-longtest".
func (c *BuildConfig) IsLongTest() bool {
	return strings.HasSuffix(c.Name, "-longtest")
}

// OutboundNetworkAllowed reports whether this builder should be
// allowed to make outbound network requests. This is only enforced
// on some builders. (Currently most Linux ones)
func (c *BuildConfig) OutboundNetworkAllowed() bool {
	return c.IsLongTest()
}

func (c *BuildConfig) GoInstallRacePackages() []string {
	if c.InstallRacePackages != nil {
		return append([]string(nil), c.InstallRacePackages...)
	}
	if c.IsRace() {
		return []string{"std"}
	}
	return nil
}

// AllScript returns the relative path to the operating system's script to
// do the build and run its standard set of tests.
// Example values are "src/all.bash", "src/all.bat", "src/all.rc".
func (c *BuildConfig) AllScript() string {
	if c.Name == "" {
		panic("bogus BuildConfig")
	}
	if c.IsRace() {
		if strings.HasPrefix(c.Name, "windows-") {
			return "src/race.bat"
		}
		return "src/race.bash"
	}
	if strings.HasPrefix(c.Name, "windows-") {
		return "src/all.bat"
	}
	if strings.HasPrefix(c.Name, "plan9-") {
		return "src/all.rc"
	}
	if strings.HasPrefix(c.Name, "misc-compile") {
		return "src/buildall.bash"
	}
	return "src/all.bash"
}

// SplitMakeRun reports whether the coordinator should first compile
// (using c.MakeScript), then snapshot, then run the tests (ideally
// sharded) using cmd/dist test.
// Eventually this function should always return true (and then be deleted)
// but for now we've only set up the scripts and verified that the main
// configurations work.
func (c *BuildConfig) SplitMakeRun() bool {
	switch c.AllScript() {
	case "src/all.bash", "src/all.bat",
		"src/race.bash", "src/race.bat",
		"src/all.rc":
		// These we've verified to work.
		return true
	}
	// TODO(bradfitz): buildall.bash should really just be N small container
	// jobs instead of a "buildall.bash". Then we can delete this whole method.
	return false
}

func (c *BuildConfig) IsTryOnly() bool { return c.tryOnly }

func (c *BuildConfig) NeedsGoProxy() bool { return c.needsGoProxy }

// PrivateGoProxy for builder has it's own Go proxy instead of proxy.golang.org
func (c *BuildConfig) PrivateGoProxy() bool { return c.privateGoProxy }

// BuildsRepoPostSubmit reports whether the build configuration type c
// should build the given repo ("go", "net", etc) and branch
// ("master", "release-branch.go1.12") as a post-submit build
// that shows up on https://build.golang.org/.
func (c *BuildConfig) BuildsRepoPostSubmit(repo, branch, goBranch string) bool {
	if c.tryOnly {
		return false
	}
	return c.buildsRepoAtAll(repo, branch, goBranch)
}

// BuildsRepoTryBot reports whether the build configuration type c
// should build the given repo ("go", "net", etc) and branch
// ("master", "release-branch.go1.12") as a trybot.
func (c *BuildConfig) BuildsRepoTryBot(repo, branch, goBranch string) bool {
	return c.tryBot != nil && c.tryBot(repo, branch, goBranch) && c.buildsRepoAtAll(repo, branch, goBranch)
}

// ShouldRunDistTest reports whether the named cmd/dist test should be
// run for this build config. The isNormalTry parameter is whether this
// is for a normal TryBot (non-SlowBot) run.
//
// In general, this returns true. When in normal trybot mode,
// some slow portable tests are only run on the fastest builder.
//
// Individual builders can adjust this policy to fit their needs.
func (c *BuildConfig) ShouldRunDistTest(distTest string, isNormalTry bool) bool {
	run := true

	// This section implements the default cmd/dist test policy.
	// Any changes here will affect test coverage on all builders.
	if isNormalTry {
		slowPortableTest := distTest == "api" // Whether a test is slow and has the same behavior everywhere.
		fastestBuilder := c.Name == "linux-amd64"
		if slowPortableTest && !fastestBuilder {
			// Don't run the test on this builder.
			run = false
		}
	}

	// Let individual builders adjust the cmd/dist test policy.
	if c.distTestAdjust != nil {
		run = c.distTestAdjust(run, distTest, isNormalTry)
	}

	return run
}

// buildsRepoAtAll reports whether we should do builds of the provided
// repo ("go", "sys", "net", etc). This applies to both post-submit
// and trybot builds. Use BuildsRepoPostSubmit for only post-submit
// or BuildsRepoTryBot for trybots.
//
// The branch is the branch of repo ("master",
// "release-branch.go1.12", etc); it is required. The goBranch is the
// branch of Go itself. It's required if repo != "go". When repo ==
// "go", the goBranch defaults to the value of branch.
func (c *BuildConfig) buildsRepoAtAll(repo, branch, goBranch string) bool {
	if goBranch == "" {
		if repo == "go" {
			goBranch = branch
		} else {
			panic("missing goBranch")
		}
	}
	if branch == "" {
		panic("missing branch")
	}
	if repo == "" {
		panic("missing repo")
	}
	// Don't build old branches.
	const minGo1x = 11
	for _, b := range []string{branch, goBranch} {
		if bmaj, bmin, ok := version.ParseReleaseBranch(b); ok {
			if bmaj != 1 || bmin < minGo1x {
				return false
			}
			bmm := types.MajorMinor{bmaj, bmin}
			if bmm.Less(c.MinimumGoVersion) {
				return false
			}
			if repo == "exp" {
				// Don't test exp against release branches; it's experimental.
				return false
			}
		}
	}

	// Build dev.boringcrypto branches only on linux/amd64 and windows/386 (see golang.org/issue/26791).
	if repo == "go" && (branch == "dev.boringcrypto" || strings.HasPrefix(branch, "dev.boringcrypto.")) {
		if c.Name != "linux-amd64" && c.Name != "windows-386-2008" {
			return false
		}
	}
	if repo != "go" && !c.SplitMakeRun() {
		return false
	}
	if p := c.buildsRepo; p != nil {
		return p(repo, branch, goBranch)
	}
	return buildRepoByDefault(repo)
}

// buildRepoByDefault reports whether builders should do builds
// for the given repo ("go", "net", etc.) by default.
//
// It's used directly by BuildConfig.buildsRepoAtAll method for
// builders with a nil BuildConfig.buildsRepo value.
// It's also used by many builders that provide a custom build
// repo policy (a non-nil BuildConfig.buildsRepo value) as part
// of making the decision of whether to build a given repo.
//
// As a result, it effectively implements the default build repo policy.
// Any changes here will affect repo coverage of many builders.
func buildRepoByDefault(repo string) bool {
	switch repo {
	case "go":
		// Build the main Go repository by default.
		return true
	case "mobile", "exp", "build", "vulndb":
		// Don't build x/mobile, x/exp, x/build, x/vulndb by default.
		//
		// Builders need to explicitly opt-in to build these repos.
		return false
	default:
		// Build all other golang.org/x repositories by default.
		return true
	}
}

var (
	defaultPlusExp            = defaultPlus("exp")
	defaultPlusExpBuild       = defaultPlus("exp", "build")
	defaultPlusExpBuildVulnDB = defaultPlus("exp", "build", "vulndb")
)

// defaultPlus returns a buildsRepo policy function that returns true for all
// all the repos listed, plus the default repos.
func defaultPlus(repos ...string) func(repo, branch, goBranch string) bool {
	return func(repo, _, _ string) bool {
		for _, r := range repos {
			if r == repo {
				return true
			}
		}
		return buildRepoByDefault(repo)
	}
}

// AllScriptArgs returns the set of arguments that should be passed to the
// all.bash-equivalent script. Usually empty.
func (c *BuildConfig) AllScriptArgs() []string {
	return append([]string(nil), c.allScriptArgs...)
}

// MakeScript returns the relative path to the operating system's script to
// do the build.
// Example values are "src/make.bash", "src/make.bat", "src/make.rc".
func (c *BuildConfig) MakeScript() string {
	if strings.HasPrefix(c.Name, "windows-") {
		return "src/make.bat"
	}
	if strings.HasPrefix(c.Name, "plan9-") {
		return "src/make.rc"
	}
	return "src/make.bash"
}

// MakeScriptArgs returns the set of arguments that should be passed to the
// make.bash-equivalent script. Usually empty.
func (c *BuildConfig) MakeScriptArgs() []string {
	return c.AllScriptArgs()
}

// GorootFinal returns the default install location for
// releases for this platform.
func (c *BuildConfig) GorootFinal() string {
	if strings.HasPrefix(c.Name, "windows-") {
		return "c:\\go"
	}
	return "/usr/local/go"
}

// MachineType returns the GCE machine type to use for this builder.
func (c *HostConfig) MachineType() string {
	if v := c.machineType; v != "" {
		return v
	}
	if c.NestedVirt {
		// e2 instances do not support nested virtualization, but n2
		// instances do.
		return "n2-standard-4" // 4 vCPUs, 16 GB mem
	}
	if c.IsContainer() {
		// Set a higher default machine size for containers,
		// so their /workdir tmpfs can be larger. The COS
		// image has no swap, so we want to make sure the
		// /workdir fits completely in memory.
		return "e2-standard-16" // 16 vCPUs, 64 GB mem
	}
	return "e2-standard-8" // 8 vCPUs, 32 GB mem
}

// IsEC2 returns true if the machine type is an EC2 arm64 type.
func (c *HostConfig) IsEC2() bool {
	return c.isEC2
}

// PoolName returns a short summary of the builder's host type for the
// https://farmer.golang.org/builders page.
func (c *HostConfig) PoolName() string {
	switch {
	case c.IsReverse:
		return "Reverse (dedicated machine/VM)"
	case c.IsEC2():
		return "EC2 VM Container"
	case c.IsVM():
		return "GCE VM"
	case c.IsContainer():
		return "Container"
	}
	panic("unknown builder type")
}

// ContainerVMImage returns the base VM name (not the fully qualified
// URL resource name of the VM) that starts the konlet program that
// pulls & runs a container.
// The empty string means that no particular VM image is required
// and the caller can run this container in any host.
//
// This method is only applicable when c.IsContainer() is true.
func (c *HostConfig) ContainerVMImage() string {
	if c.KonletVMImage != "" {
		return c.KonletVMImage
	}
	if c.NestedVirt {
		return "debian-stretch-vmx"
	}
	if c.isEC2 && c.ContainerImage != "" {
		return fmt.Sprintf("gcr.io/%s/%s", buildenv.Production.ProjectName, c.ContainerImage)
	}
	return ""
}

// IsHermetic reports whether this host config gets a fresh
// environment (including /usr, /var, etc) for each execution. This is
// true for VMs, GKE, and reverse buildlets running their containers
// running in Docker, but false on some reverse buildlets.
func (c *HostConfig) IsHermetic() bool {
	switch {
	case c.IsReverse:
		return c.HermeticReverse
	case c.IsEC2():
		return true
	case c.IsVM():
		return true
	case c.IsContainer():
		return true
	}
	panic("unknown builder type")
}

// GCENumCPU reports the number of GCE CPUs this buildlet requires.
func (c *HostConfig) GCENumCPU() int {
	t := c.MachineType()
	n, _ := strconv.Atoi(t[strings.LastIndex(t, "-")+1:])
	return n
}

// NumTestHelpers reports how many additional buildlets
// past the first one to help out with sharded tests.
//
// isTry specifies whether it's for a pre-submit test
// run (a TryBot or SlowBot) where speed matters more.
func (c *BuildConfig) NumTestHelpers(isTry bool) int {
	if isTry && c.numTryTestHelpers != 0 {
		return c.numTryTestHelpers
	}
	return c.numTestHelpers
}

// defaultTrySet returns a trybot policy function that reports whether
// a project should use trybots. All the default projects are included,
// plus any given in extraProj.
func defaultTrySet(extraProj ...string) func(proj, branch, goBranch string) bool {
	return func(proj, branch, goBranch string) bool {
		if proj == "go" {
			return true
		}
		for _, p := range extraProj {
			if proj == p {
				return true
			}
		}
		switch proj {
		case "grpc-review":
			return false
		}
		return true
	}
}

// explicitTrySet returns a trybot policy function that reports
// whether a project should use trybots. Only the provided projects in
// projs are enabled.
func explicitTrySet(projs ...string) func(proj, branch, goBranch string) bool {
	return func(proj, branch, goBranch string) bool {
		for _, p := range projs {
			if proj == p {
				return true
			}
		}
		return false
	}
}

func init() {
	addBuilder(BuildConfig{
		Name:              "freebsd-amd64-11_2",
		HostType:          "host-freebsd-11_2",
		tryBot:            explicitTrySet("sys"),
		distTestAdjust:    fasterTrybots,
		numTryTestHelpers: 4,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder is still used by Go 1.16 and 1.15,
			// so keep it around a bit longer. See golang.org/issue/45727.
			// Test relevant Go versions so that we're better informed.
			return atMostGo1(goBranch, 16) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-amd64-11_4",
		HostType:          "host-freebsd-11_4",
		tryBot:            explicitTrySet("sys"),
		distTestAdjust:    fasterTrybots,
		numTryTestHelpers: 4,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder is still used by Go 1.17 and 1.16,
			// keep it around a bit longer. See go.dev/issue/49491.
			return atMostGo1(goBranch, 17) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-12_3",
		HostType: "host-freebsd-12_3",
		tryBot:   defaultTrySet("sys"),

		distTestAdjust:    fasterTrybots, // If changing this policy, update TestShouldRunDistTest accordingly.
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-386-12_3",
		HostType:          "host-freebsd-12_3",
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		distTestAdjust:    fasterTrybots,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-race",
		HostType: "host-freebsd-13_0-big",
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-amd64-13_0",
		HostType:          "host-freebsd-13_0",
		tryBot:            explicitTrySet("sys"),
		distTestAdjust:    fasterTrybots, // If changing this policy, update TestShouldRunDistTest accordingly.
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-386-13_0",
		HostType:          "host-freebsd-13_0",
		tryBot:            explicitTrySet("sys"),
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		distTestAdjust:    fasterTrybots,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:           "freebsd-386-11_2",
		HostType:       "host-freebsd-11_2",
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         explicitTrySet("sys"),
		env:            []string{"GOARCH=386", "GOHOSTARCH=386"},
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder is still used by Go 1.16 and 1.15,
			// so keep it around a bit longer. See golang.org/issue/45727.
			// Test relevant Go versions so that we're better informed.
			return atMostGo1(goBranch, 16) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:           "freebsd-386-11_4",
		HostType:       "host-freebsd-11_4",
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         explicitTrySet("sys"),
		env:            []string{"GOARCH=386", "GOHOSTARCH=386"},
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder is still used by Go 1.17 and 1.16,
			// keep it around a bit longer. See go.dev/issue/49491.
			return atMostGo1(goBranch, 17) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:           "linux-386",
		HostType:       "host-linux-bullseye",
		distTestAdjust: fasterTrybots,
		tryBot:         defaultTrySet(),
		Notes:          "Debian stable (currently Debian bullseye).",
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		numTestHelpers:    1,
		numTryTestHelpers: 3,
	})
	addBuilder(BuildConfig{
		Name:  "linux-386-softfloat",
		Notes: "GO386=softfloat",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" || repo == "crypto"
		},
		HostType: "host-linux-stretch",
		env:      []string{"GOARCH=386", "GOHOSTARCH=386", "GO386=softfloat"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64",
		HostType: "host-linux-bullseye",
		tryBot:   defaultTrySet(),
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "vulndb" && atMostGo1(goBranch, 17) {
				// The vulndb repo is for use only by the Go team,
				// so it doesn't need to work on older Go versions.
				return false
			}
			return defaultPlusExpBuildVulnDB(repo, branch, goBranch)
		},
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		numTestHelpers:    1,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-boringcrypto",
		HostType: "host-linux-bullseye",
		Notes:    "GOEXPERIMENT=boringcrypto",
		tryBot:   defaultTrySet(),
		env: []string{
			"GOEXPERIMENT=boringcrypto",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		numTestHelpers:    1,
		numTryTestHelpers: 4,
		GoDeps: []string{
			"5c4ed73f1c3f2052d8f60ce5ed45d9d4f9686331", // CL 397895, "internal/goexperiment: add GOEXPERIMENT=boringcrypto"
		},
	})
	addBuilder(BuildConfig{
		Name:       "linux-amd64-vmx",
		HostType:   "host-linux-stretch-vmx",
		buildsRepo: disabledBuilder,
	})

	const testAlpine = false // Issue 22689 (hide all red builders), Issue 19938 (get Alpine passing)
	if testAlpine {
		addBuilder(BuildConfig{
			Name:     "linux-amd64-alpine",
			HostType: "host-linux-x86-alpine",
		})
	}

	// addMiscCompileGo1 adds a misc-compile TryBot that
	// runs buildall.bash on the specified target(s), up to 3 max.
	// The targets are matched against the "go tool dist list" name,
	// but with hyphens instead of forward slashes ("linux-amd64", etc).
	// If min is non-zero, it specifies the minimum Go 1.x version.
	addMiscCompileGo1 := func(min int, suffix string, targets ...string) {
		if len(targets) > 3 {
			// This limit will do until we have better visibility
			// into holistic TryBot completion times via metrics.
			panic("at most 3 targets may be specified to avoid making TryBots slow; see issues 32632 and 17104")
		}
		var v types.MajorMinor
		var alsoNote string
		if min != 0 {
			v = types.MajorMinor{1, min}
			alsoNote = fmt.Sprintf(" Applies to Go 1.%d and newer.", min)
		}
		addBuilder(BuildConfig{
			Name:     "misc-compile" + suffix,
			HostType: "host-linux-stretch",
			tryBot:   defaultTrySet(),
			env: []string{
				"GO_DISABLE_OUTBOUND_NETWORK=1",
			},
			tryOnly:          true,
			MinimumGoVersion: v,
			CompileOnly:      true,
			Notes:            "Runs buildall.bash to cross-compile & vet std+cmd packages for " + strings.Join(targets, " & ") + ", but doesn't run any tests." + alsoNote,
			allScriptArgs: []string{
				// Filtering pattern to buildall.bash:
				"^(" + strings.Join(targets, "|") + ")$",
			},
		})
	}
	// addMiscCompile adds a misc-compile TryBot
	// for all supported Go versions.
	addMiscCompile := func(suffix string, targets ...string) { addMiscCompileGo1(0, suffix, targets...) }

	// Arrange so that no more than 3 ports are tested sequentially in each misc-compile
	// TryBot to avoid any individual misc-compile TryBot from becoming a bottleneck for
	// overall TryBot completion time (currently 10 minutes; see golang.org/issue/17104).
	//
	// The TestTryBotsCompileAllPorts test is used to detect any gaps in TryBot coverage
	// when new ports are added, and the misc-compile pairs below can be re-arranged.
	//
	// (In the past, we used flexible regexp patterns that matched all architectures
	// for a given GOOS value. However, over time as new architectures were added,
	// some misc-compile TryBot could become much slower than others.)
	//
	// See golang.org/issue/32632.
	addMiscCompile("-windows-arm", "windows-arm", "windows-arm64")
	addMiscCompile("-darwin", "darwin-amd64", "darwin-arm64")
	addMiscCompile("-mips", "linux-mips", "linux-mips64")
	addMiscCompile("-mipsle", "linux-mipsle", "linux-mips64le")
	addMiscCompile("-ppc", "linux-ppc64", "linux-ppc64le", "aix-ppc64")
	addMiscCompile("-freebsd", "freebsd-386", "freebsd-arm", "freebsd-arm64")
	addMiscCompile("-netbsd", "netbsd-386", "netbsd-amd64")
	addMiscCompile("-netbsd-arm", "netbsd-arm", "netbsd-arm64")
	addMiscCompile("-openbsd", "openbsd-386", "openbsd-mips64")
	addMiscCompile("-openbsd-arm", "openbsd-arm", "openbsd-arm64")
	addMiscCompile("-plan9", "plan9-386", "plan9-amd64", "plan9-arm")
	addMiscCompile("-solaris", "solaris-amd64", "illumos-amd64")
	addMiscCompile("-other-1", "dragonfly-amd64", "linux-loong64")
	addMiscCompile("-other-2", "linux-riscv64", "linux-s390x", "linux-arm-arm5") // 'linux-arm-arm5' is linux/arm with GOARM=5.

	// TODO: Issue 25963, get the misc-compile trybots for Android/iOS.
	// Then consider subrepos too, so "mobile" can at least be included
	// as a misc-compile for ^android- and ^ios-.

	addBuilder(BuildConfig{
		Name:     "linux-amd64-nocgo",
		HostType: "host-linux-bullseye",
		Notes:    "cgo disabled",
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := buildRepoByDefault(repo)
			switch repo {
			case "perf":
				// Requires sqlite, which requires cgo.
				b = false
			case "exp":
				b = true
			}
			return b
		},
		env: []string{
			"CGO_ENABLED=0",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			// This USER=root was required for Docker-based builds but probably isn't required
			// in the VM anymore, since the buildlet probably already has this in its environment.
			// (It was required because without cgo, it couldn't find the username)
			"USER=root",
		},
	})
	addBuilder(BuildConfig{
		Name:       "linux-amd64-noopt",
		Notes:      "optimizations and inlining disabled",
		HostType:   "host-linux-bullseye",
		buildsRepo: onlyGo,
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GO_GCFLAGS=-N -l",
		},
	})
	addBuilder(BuildConfig{
		Name:        "linux-amd64-ssacheck",
		HostType:    "host-linux-bullseye",
		buildsRepo:  onlyGo,
		tryBot:      nil, // TODO: add a func to conditionally run this trybot if compiler dirs are touched
		CompileOnly: true,
		Notes:       "SSA internal checks enabled",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GO_GCFLAGS=-d=ssa/check/on,dclstack",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-staticlockranking",
		HostType: "host-linux-stretch",
		Notes:    "builder with GOEXPERIMENT=staticlockranking, see golang.org/issue/37937",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go"
		},
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GOEXPERIMENT=staticlockranking",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-unified",
		HostType: "host-linux-buster",
		Notes:    "builder with GOEXPERIMENT=unified, see golang.org/issue/46786",
		tryBot: func(repo, branch, goBranch string) bool {
			// TODO(go.dev/issue/52150): Restore testing against tools repo.
			return (repo == "go" /*|| repo == "tools"*/) && (goBranch == "master" || goBranch == "dev.unified")
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return (repo == "go" || repo == "tools") && (goBranch == "master" || goBranch == "dev.unified")
		},
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GOEXPERIMENT=unified",
		},
		GoDeps: []string{
			"804ecc2581caf33ae347d6a1ce67436d1f74e93b", // CL 328215, which added GOEXPERIMENT=unified on dev.typeparams
		},
		numTestHelpers:    1,
		numTryTestHelpers: 4,
		KnownIssues:       []int{52150},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-nounified",
		HostType: "host-linux-buster",
		Notes:    "builder with GOEXPERIMENT=nounified, see golang.org/issue/51397",
		tryBot: func(repo, branch, goBranch string) bool {
			return (repo == "go" || repo == "tools") && (goBranch == "master" || goBranch == "dev.unified")
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return (repo == "go" || repo == "tools") && (goBranch == "master" || goBranch == "dev.unified")
		},
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GOEXPERIMENT=nounified",
		},
		GoDeps: []string{
			"804ecc2581caf33ae347d6a1ce67436d1f74e93b", // CL 328215, which added GOEXPERIMENT=unified on dev.typeparams
		},
		numTestHelpers:    1,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-goamd64v3",
		HostType: "host-linux-bullseye",
		Notes:    "builder with GOAMD64=v3, see proposal 45453 and issue 48505",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// GOAMD64 is added in Go 1.18.
			return atLeastGo1(goBranch, 18) && buildRepoByDefault(repo)
		},
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GOAMD64=v3",
		},
	})
	addBuilder(BuildConfig{
		Name:                "linux-amd64-racecompile",
		HostType:            "host-linux-bullseye",
		tryBot:              nil, // TODO: add a func to conditionally run this trybot if compiler dirs are touched
		CompileOnly:         true,
		SkipSnapshot:        true,
		StopAfterMake:       true,
		InstallRacePackages: []string{"cmd/compile", "cmd/link"},
		Notes:               "race-enabled cmd/compile and cmd/link",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:              "linux-amd64-race",
		HostType:          "host-linux-bullseye",
		tryBot:            defaultTrySet(),
		buildsRepo:        defaultPlusExpBuild,
		distTestAdjust:    fasterTrybots,
		numTestHelpers:    1,
		numTryTestHelpers: 5,
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-clang",
		HostType: "host-linux-clang",
		Notes:    "Debian Buster + clang 7.0 instead of gcc",
		env:      []string{"CC=/usr/bin/clang", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-clang",
		HostType: "host-linux-clang",
		Notes:    "Debian Buster + clang 7.0 instead of gcc",
		env:      []string{"CC=/usr/bin/clang"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-sid",
		HostType: "host-linux-sid",
		Notes:    "Debian sid (unstable)",
		env:      []string{"GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-sid",
		HostType: "host-linux-sid",
		Notes:    "Debian sid (unstable)",
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-fedora",
		HostType: "host-linux-fedora",
		Notes:    "Fedora",
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-androidemu",
		HostType: "host-android-amd64-emu",
		env: []string{
			"GOARCH=amd64",
			"GOOS=linux",
			"CGO_ENABLED=1",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		tryBot: func(repo, branch, goBranch string) bool {
			// Only for mobile repo for now, not "go":
			return repo == "mobile" && branch == "master" && goBranch == "master"
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "mobile" && branch == "master" && goBranch == "master"
		},
		Notes: "Runs GOOS=linux but with the Android emulator attached, for running x/mobile host tests.",
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-stretch",
		HostType: "host-linux-stretch",
		Notes:    "Debian Stretch.",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-bullseye",
		HostType: "host-linux-bullseye",
		Notes:    "Debian Bullseye.",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-buster",
		HostType: "host-linux-buster",
		Notes:    "Debian Buster.",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-stretch",
		HostType: "host-linux-stretch",
		Notes:    "Debian Stretch, 32-bit builder.",
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-buster",
		HostType: "host-linux-buster",
		Notes:    "Debian Buster, 32-bit builder.",
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-longtest",
		HostType: "host-linux-bullseye-morecpu",
		Notes:    "Debian Bullseye with go test -short=false",
		tryBot: func(repo, branch, goBranch string) bool {
			onReleaseBranch := strings.HasPrefix(branch, "release-branch.")
			return repo == "go" && onReleaseBranch // See issue 37827.
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			// Test all repos, ignoring buildRepoByDefault.
			// For golang.org/x repos, don't test non-latest versions.
			return repo == "go" || (branch == "master" && goBranch == "master")
		},
		needsGoProxy: true, // for cmd/go module tests
		env: []string{
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
		numTryTestHelpers: 4, // Target time is < 15 min for golang.org/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-longtest",
		HostType: "host-linux-bullseye-morecpu",
		Notes:    "Debian Bullseye with go test -short=false; to get 32-bit coverage",
		tryBot: func(repo, branch, goBranch string) bool {
			onReleaseBranch := strings.HasPrefix(branch, "release-branch.")
			return repo == "go" && onReleaseBranch // See issue 37827.
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := buildRepoByDefault(repo)
			if repo != "go" && !(branch == "master" && goBranch == "master") {
				// For golang.org/x repos, don't test non-latest versions.
				b = false
			}
			return b
		},
		needsGoProxy: true, // for cmd/go module tests
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
		numTryTestHelpers: 4, // Target time is < 15 min for golang.org/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "js-wasm",
		HostType: "host-js-wasm",
		tryBot:   explicitTrySet("go"),
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := buildRepoByDefault(repo)
			switch repo {
			case "benchmarks", "debug", "perf", "talks", "tools", "tour", "website":
				// Don't test these golang.org/x repos.
				b = false
			}
			if repo != "go" && !(branch == "master" && goBranch == "master") {
				// For golang.org/x repos, don't test non-latest versions.
				b = false
			}
			return b
		},

		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			if isNormalTry {
				if strings.Contains(distTest, "/internal/") ||
					strings.Contains(distTest, "vendor/golang.org/x/arch") {
					return false
				}
				switch distTest {
				case "cmd/go", "nolibgcc:crypto/x509", "reboot":
					return false
				}
			}
			return run
		},
		numTryTestHelpers: 5,
		env: []string{
			"GOOS=js", "GOARCH=wasm", "GOHOSTOS=linux", "GOHOSTARCH=amd64",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/workdir/go/misc/wasm",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-amd64-68",
		HostType:          "host-openbsd-amd64-68",
		tryBot:            defaultTrySet(),
		distTestAdjust:    noTestDirAndNoReboot,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "openbsd-386-68",
		HostType: "host-openbsd-386-68",
		tryBot:   explicitTrySet("sys"),
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "review" {
				// https://golang.org/issue/49529: git seems to be too slow on this
				// platform.
				return false
			}
			return buildRepoByDefault(repo)
		},
		distTestAdjust:    noTestDirAndNoReboot,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:           "openbsd-amd64-70",
		HostType:       "host-openbsd-amd64-70",
		tryBot:         defaultTrySet(),
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// https://github.com/golang/go/issues/48977#issuecomment-971763553:
			// 1.16 seems to be incompatible with 7.0.
			return atLeastGo1(goBranch, 17) && buildRepoByDefault(repo)
		},
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "openbsd-386-70",
		HostType: "host-openbsd-386-70",
		tryBot:   explicitTrySet("sys"),
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "review" {
				// https://golang.org/issue/49529: git seems to be too slow on this
				// platform.
				return false
			}
			// https://github.com/golang/go/issues/48977#issuecomment-971763553:
			// 1.16 seems to be incompatible with 7.0.
			return atLeastGo1(goBranch, 17) && buildRepoByDefault(repo)
		},
		distTestAdjust:    noTestDirAndNoReboot,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		// This builder is only for the runtime team to use investigating golang/go#49209.
		Name:           "openbsd-386-70-n2d",
		HostType:       "host-openbsd-386-70-n2d",
		buildsRepo:     disabledBuilder,
		distTestAdjust: noTestDirAndNoReboot,
	})
	addBuilder(BuildConfig{
		Name:         "openbsd-arm-jsing",
		HostType:     "host-openbsd-arm-joelsing",
		SkipSnapshot: true,
		FlakyNet:     true,
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "go", "net", "sys":
				return branch == "master" && goBranch == "master"
			default:
				return false
			}
		},
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         nil,
		env: []string{
			// The machine is slow.
			"GO_TEST_TIMEOUT_SCALE=5",
		},
	})
	addBuilder(BuildConfig{
		Name:         "openbsd-arm64-jsing",
		HostType:     "host-openbsd-arm64-joelsing",
		SkipSnapshot: true,
		FlakyNet:     true,
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "go", "net", "sys":
				return branch == "master" && goBranch == "master"
			default:
				return false
			}
		},
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         nil,
		env: []string{
			// The machine is slow.
			"GO_TEST_TIMEOUT_SCALE=5",
		},
	})
	addBuilder(BuildConfig{
		Name:         "openbsd-mips64-jsing",
		HostType:     "host-openbsd-mips64-joelsing",
		SkipSnapshot: true,
		FlakyNet:     true,
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "go", "net", "sys":
				return branch == "master" && goBranch == "master"
			default:
				return false
			}
		},
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         nil,
		env: []string{
			// The machine is slow.
			"GO_TEST_TIMEOUT_SCALE=5",
		},
	})
	addBuilder(BuildConfig{
		Name:           "netbsd-amd64-9_0",
		HostType:       "host-netbsd-amd64-9_0",
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         explicitTrySet("sys"),
		KnownIssues:    []int{50138},
	})
	addBuilder(BuildConfig{
		Name:           "netbsd-386-9_0",
		HostType:       "host-netbsd-386-9_0",
		distTestAdjust: noTestDirAndNoReboot,
		KnownIssues:    []int{50138},
	})
	addBuilder(BuildConfig{
		Name:     "netbsd-arm-bsiegert",
		HostType: "host-netbsd-arm-bsiegert",
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "review" {
				// https://golang.org/issue/49530: This test seems to be too slow even
				// with a long scale factor.
				return false
			}
			return buildRepoByDefault(repo)
		},
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         nil,
		env: []string{
			// The machine is slow.
			"GO_TEST_TIMEOUT_SCALE=10",
		},
		FlakyNet:    true,
		KnownIssues: []int{50138},
	})
	addBuilder(BuildConfig{
		Name:           "netbsd-arm64-bsiegert",
		HostType:       "host-netbsd-arm64-bsiegert",
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         nil,
		env: []string{
			// The machine is slow.
			"GO_TEST_TIMEOUT_SCALE=10",
		},
		FlakyNet:    true,
		KnownIssues: []int{50138},
	})
	addBuilder(BuildConfig{
		Name:           "plan9-386",
		HostType:       "host-plan9-386-gce",
		numTestHelpers: 1,
		tryOnly:        true, // disable it for now; Issue 31261, Issue 29801
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			switch distTest {
			case "api",
				"go_test:cmd/go": // takes over 20 minutes without working SMP
				return false
			}
			return run
		},
		buildsRepo:  plan9Default,
		KnownIssues: []int{29801},
	})
	addBuilder(BuildConfig{
		Name:           "windows-amd64-2008",
		HostType:       "host-windows-amd64-2008",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo:     onlyGo,
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
	})
	addBuilder(BuildConfig{
		Name:           "windows-amd64-2008-newcc",
		HostType:       "host-windows-amd64-2008-newcc",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo:     onlyGo,
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
		KnownIssues: []int{35006},
	})
	addBuilder(BuildConfig{
		Name:              "windows-386-2008",
		HostType:          "host-windows-amd64-2008",
		buildsRepo:        defaultPlusExpBuild,
		distTestAdjust:    fasterTrybots,
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:        "windows-386-2008-newcc",
		HostType:    "host-windows-amd64-2008-newcc",
		buildsRepo:  defaultPlusExpBuild,
		env:         []string{"GOARCH=386", "GOHOSTARCH=386"},
		KnownIssues: []int{35006},
	})
	addBuilder(BuildConfig{
		Name:              "windows-386-2012",
		HostType:          "host-windows-amd64-2012",
		distTestAdjust:    fasterTrybots,
		buildsRepo:        onlyGo,
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:        "windows-386-2012-newcc",
		HostType:    "host-windows-amd64-2012-newcc",
		buildsRepo:  onlyGo,
		env:         []string{"GOARCH=386", "GOHOSTARCH=386"},
		KnownIssues: []int{35006},
	})
	addBuilder(BuildConfig{
		Name:           "windows-amd64-2012",
		HostType:       "host-windows-amd64-2012",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo:     onlyGo,
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
	})
	addBuilder(BuildConfig{
		Name:           "windows-amd64-2012-newcc",
		HostType:       "host-windows-amd64-2012-newcc",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo:     onlyGo,
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
		KnownIssues: []int{35006},
	})
	addBuilder(BuildConfig{
		Name:           "windows-amd64-2016",
		HostType:       "host-windows-amd64-2016",
		buildsRepo:     defaultPlusExpBuild,
		distTestAdjust: fasterTrybots,
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:       "windows-amd64-2016-newcc",
		HostType:   "host-windows-amd64-2016-newcc",
		buildsRepo: defaultPlusExpBuild,
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
		KnownIssues: []int{35006},
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-longtest",
		HostType: "host-windows-amd64-2016-big",
		Notes:    "Windows Server 2016 with go test -short=false",
		tryBot: func(repo, branch, goBranch string) bool {
			onReleaseBranch := strings.HasPrefix(branch, "release-branch.")
			return repo == "go" && onReleaseBranch // See issue 37827.
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := defaultPlusExpBuild(repo, branch, goBranch)
			if repo != "go" && !(branch == "master" && goBranch == "master") {
				// For golang.org/x repos, don't test non-latest versions.
				b = false
			}
			return b
		},
		needsGoProxy: true, // for cmd/go module tests
		env: []string{
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
		numTryTestHelpers: 4, // Target time is < 15 min for golang.org/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-longtest-newcc",
		HostType: "host-windows-amd64-2016-big-newcc",
		Notes:    "Windows Server 2016 with go test -short=false",
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := defaultPlusExpBuild(repo, branch, goBranch)
			if repo != "go" && !(branch == "master" && goBranch == "master") {
				// For golang.org/x repos, don't test non-latest versions.
				b = false
			}
			return b
		},
		needsGoProxy: true, // for cmd/go module tests
		env: []string{
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
		KnownIssues: []int{35006},
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-race",
		HostType: "host-windows-amd64-2016-big",
		Notes:    "Only runs -race tests (./race.bat)",
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2"},
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-race-newcc",
		HostType: "host-windows-amd64-2016-big-newcc",
		Notes:    "Only runs -race tests (./race.bat)",
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2"},
		KnownIssues: []int{35006},
	})
	addBuilder(BuildConfig{
		Name:     "windows-arm-zx2c4",
		HostType: "host-windows-arm64-zx2c4",
		env: []string{
			"GOARM=7",
			"GO_TEST_TIMEOUT_SCALE=3"},
	})
	addBuilder(BuildConfig{
		Name:              "windows-arm64-10",
		HostType:          "host-windows-arm64-mini",
		numTryTestHelpers: 1,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atLeastGo1(goBranch, 17) && buildRepoByDefault(repo)
		},
		env: []string{
			"GOARCH=arm64",
		},
	})
	addBuilder(BuildConfig{
		Name:              "windows-arm64-11",
		HostType:          "host-windows11-arm64-mini",
		numTryTestHelpers: 1,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atLeastGo1(goBranch, 18) && buildRepoByDefault(repo)
		},
		env: []string{
			"GOARCH=arm64",
			"GOMAXPROCS=4", // OOM problems, see go.dev/issue/51019
		},
		KnownIssues: []int{51019},
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-10_12",
		HostType:       "host-darwin-10_12",
		distTestAdjust: macTestPolicy,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// macOS 10.12 not supported after Go 1.16
			return atMostGo1(goBranch, 16) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-10_14",
		HostType:       "host-darwin-10_14",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExp,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-10_15",
		HostType:       "host-darwin-10_15",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-11_0",
		HostType:       "host-darwin-amd64-11_0",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-12_0",
		HostType:       "host-darwin-amd64-12_0",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-nocgo",
		HostType:       "host-darwin-amd64-12_0",
		distTestAdjust: noTestDirAndNoReboot,
		env:            []string{"CGO_ENABLED=0"},
	})
	addBuilder(BuildConfig{
		Name:           "darwin-arm64-11_0-toothrot",
		HostType:       "host-darwin-arm64-11_0-toothrot",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-arm64-12_0-toothrot",
		HostType:       "host-darwin-arm64-12_0-toothrot",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-arm64-11",
		HostType:       "host-darwin-arm64-11",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-arm64-12",
		HostType:       "host-darwin-arm64-12",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-race",
		HostType:       "host-darwin-amd64-12_0",
		distTestAdjust: macTestPolicy,
		buildsRepo:     onlyGo,
	})
	addBuilder(BuildConfig{
		Name:     "ios-arm64-corellium",
		HostType: "host-ios-arm64-corellium-ios",
		Notes:    "Virtual iPhone SE running on Corellium; owned by zenly (github.com/znly)",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && branch == "master" && goBranch == "master"
		},
	})
	addBuilder(BuildConfig{
		Name:     "android-arm64-corellium",
		HostType: "host-android-arm64-corellium-android",
		Notes:    "Virtual Android running on Corellium; owned by zenly (github.com/znly)",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && branch == "master" && goBranch == "master"
		},
	})
	addBuilder(BuildConfig{
		Name:     "android-arm-corellium",
		HostType: "host-android-arm64-corellium-android",
		Notes:    "Virtual Android running on Corellium; owned by zenly (github.com/znly)",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && branch == "master" && goBranch == "master"
		},
		env: []string{
			"CGO_ENABLED=1",
			"GOARCH=arm",
		},
	})
	addBuilder(BuildConfig{
		Name:     "android-386-emu",
		HostType: "host-android-amd64-emu", // same amd64 host is used for 386 builder
		Notes:    "Android emulator on GCE (GOOS=android GOARCH=386)",
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := buildRepoByDefault(repo)
			switch repo {
			case "mobile":
				b = true
			case "build", "blog", "talks", "review", "tour", "website":
				b = false
			}
			return b
		},
		env: []string{
			"GOARCH=386",
			"GOOS=android",
			"GOHOSTARCH=amd64",
			"GOHOSTOS=linux",
			"CGO_ENABLED=1",
		},
		KnownIssues: []int{42212, 51001, 52724},
	})
	addBuilder(BuildConfig{
		Name:              "android-amd64-emu",
		HostType:          "host-android-amd64-emu",
		Notes:             "Android emulator on GCE (GOOS=android GOARCH=amd64)",
		numTryTestHelpers: 3,
		tryBot: func(repo, branch, goBranch string) bool {
			// See discussion in go.dev/issue/53377.
			switch repo {
			case "mobile":
				return true
			}
			return false
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := buildRepoByDefault(repo)
			switch repo {
			case "mobile":
				b = true
			case "build", "blog", "talks", "review", "tour", "website":
				b = false
			}
			return b
		},
		env: []string{
			"GOARCH=amd64",
			"GOOS=android",
			"GOHOSTARCH=amd64",
			"GOHOSTOS=linux",
			"CGO_ENABLED=1",
		},
		KnownIssues: []int{42212, 51001, 52724},
	})
	addBuilder(BuildConfig{
		Name:     "illumos-amd64",
		HostType: "host-illumos-amd64-jclulow",
	})
	addBuilder(BuildConfig{
		Name:        "solaris-amd64-oraclerel",
		HostType:    "host-solaris-oracle-amd64-oraclerel",
		Notes:       "Oracle Solaris release version",
		FlakyNet:    true,
		KnownIssues: []int{51443},
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64-buildlet",
		HostType:       "host-linux-ppc64-osu",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see golang.org/issues/44422
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64le-buildlet",
		HostType:       "host-linux-ppc64le-osu",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see golang.org/issues/44422
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64le-power9osu",
		HostType:       "host-linux-ppc64le-power9-osu",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see golang.org/issues/44422
	})
	addBuilder(BuildConfig{
		Name:              "linux-arm64-aws",
		HostType:          "host-linux-arm64-aws",
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 1,
	})
	addBuilder(BuildConfig{
		Name:              "linux-arm-aws",
		HostType:          "host-linux-arm-aws",
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 1,
		env: []string{
			"GOARCH=arm",
			"GOARM=6",
			"GOHOSTARCH=arm",
			"CGO_CFLAGS=-march=armv6",
			"CGO_LDFLAGS=-march=armv6",
		},
	})
	addBuilder(BuildConfig{
		FlakyNet:       true,
		HostType:       "host-linux-loong64-3a5000",
		Name:           "linux-loong64-3a5000",
		SkipSnapshot:   true,
		distTestAdjust: loong64DistTestPolicy,
		buildsRepo:     loong64BuildsRepoPolicy,
		privateGoProxy: true, // this builder is behind firewall
		env: []string{
			"GOARCH=loong64",
			"GOHOSTARCH=loong64",
		},
		KnownIssues: []int{53116, 53093},
	})
	addBuilder(BuildConfig{
		FlakyNet:       true,
		HostType:       "host-linux-mipsle-mengzhuo",
		Name:           "linux-mips64le-mengzhuo",
		buildsRepo:     onlyMasterDefault,
		distTestAdjust: mipsDistTestPolicy,
		privateGoProxy: true, // this builder is behind firewall
	})
	addBuilder(BuildConfig{
		FlakyNet:       true,
		HostType:       "host-linux-mips64le-rtrk",
		Name:           "linux-mips64le-rtrk",
		SkipSnapshot:   true,
		distTestAdjust: mipsDistTestPolicy,
		buildsRepo:     mipsBuildsRepoPolicy,
		env: []string{
			"GOARCH=mips64le",
			"GOHOSTARCH=mips64le",
		},
	})
	addBuilder(BuildConfig{
		FlakyNet:       true,
		HostType:       "host-linux-mips64le-rtrk",
		Name:           "linux-mipsle-rtrk",
		SkipSnapshot:   true,
		distTestAdjust: mipsDistTestPolicy,
		buildsRepo:     mipsBuildsRepoPolicy,
		env: []string{
			"GOARCH=mipsle",
			"GOHOSTARCH=mipsle",
		},
	})
	addBuilder(BuildConfig{
		FlakyNet:       true,
		HostType:       "host-linux-mips64-rtrk",
		Name:           "linux-mips64-rtrk",
		SkipSnapshot:   true,
		distTestAdjust: mipsDistTestPolicy,
		buildsRepo:     mipsBuildsRepoPolicy,
		env: []string{
			"GOARCH=mips64",
			"GOHOSTARCH=mips64",
		},
	})
	addBuilder(BuildConfig{
		FlakyNet:       true,
		HostType:       "host-linux-mips64-rtrk",
		Name:           "linux-mips-rtrk",
		SkipSnapshot:   true,
		distTestAdjust: mipsDistTestPolicy,
		buildsRepo:     mipsBuildsRepoPolicy,
		env: []string{
			"GOARCH=mips",
			"GOHOSTARCH=mips",
		},
	})
	addBuilder(BuildConfig{
		HostType:       "host-linux-riscv64-joelsing",
		Name:           "linux-riscv64-jsing",
		SkipSnapshot:   true,
		FlakyNet:       true,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=4"},
		distTestAdjust: riscvDistTestPolicy,
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "go", "net", "sys":
				return branch == "master" && goBranch == "master"
			default:
				return false
			}
		},
	})
	addBuilder(BuildConfig{
		HostType:       "host-linux-riscv64-unmatched",
		Name:           "linux-riscv64-unmatched",
		env:            []string{"GO_TEST_TIMEOUT_SCALE=4"},
		FlakyNet:       true,
		buildsRepo:     onlyMasterDefault,
		distTestAdjust: riscvDistTestPolicy,
		privateGoProxy: true, // this builder is behind firewall
		KnownIssues:    []int{53379},
	})
	addBuilder(BuildConfig{
		Name:           "linux-s390x-ibm",
		HostType:       "host-linux-s390x",
		numTestHelpers: 0,
		FlakyNet:       true,
	})
	addBuilder(BuildConfig{
		Name:        "linux-s390x-crosscompile",
		HostType:    "host-s390x-cross",
		Notes:       "s390x cross-compile builder for releases; doesn't run tests",
		CompileOnly: true,
		tryOnly:     true, // but not in trybot set for now
		env: []string{
			"CGO_ENABLED=1",
			"GOARCH=s390x",
			"GOHOSTARCH=amd64",
			"CC_FOR_TARGET=s390x-linux-gnu-gcc",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-localdev",
		HostType: "host-linux-amd64-localdev",
		Notes:    "for localhost development only",
		tryOnly:  true,
	})
	addBuilder(BuildConfig{
		Name:           "dragonfly-amd64",
		HostType:       "host-dragonfly-amd64-master",
		Notes:          "DragonFly BSD master, run by DragonFly team",
		distTestAdjust: noTestDirAndNoReboot,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see golang.org/issue/45216
		SkipSnapshot:   true,
		KnownIssues:    []int{53577},
	})
	addBuilder(BuildConfig{
		Name:           "freebsd-arm-paulzhol",
		HostType:       "host-freebsd-arm-paulzhol",
		distTestAdjust: noTestDirAndNoReboot,
		SkipSnapshot:   true,
		FlakyNet:       true,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This was a fragile little machine with limited memory.
			// Only run a few of the core subrepos for now while
			// we figure out what's killing it.
			switch repo {
			case "go", "sys", "net":
				return true
			}
			return false
		},
		env: []string{
			"GOARM=7",
			"CGO_ENABLED=1",
		},
		KnownIssues: []int{52679},
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-arm64-dmgk",
		HostType: "host-freebsd-arm64-dmgk",
	})
	addBuilder(BuildConfig{
		Name:           "plan9-arm",
		HostType:       "host-plan9-arm-0intro",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo:     plan9Default,
		KnownIssues:    []int{49338},
	})
	addBuilder(BuildConfig{
		Name:     "plan9-amd64-0intro",
		HostType: "host-plan9-amd64-0intro",
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			run = noTestDirAndNoReboot(run, distTest, isNormalTry)
			switch distTest {
			case "api",
				"go_test:cmd/go": // takes over 20 minutes without working SMP
				return false
			}
			return run
		},
		buildsRepo:  plan9Default,
		KnownIssues: []int{49756, 49327},
	})
	addBuilder(BuildConfig{
		Name:     "plan9-386-0intro",
		HostType: "host-plan9-386-0intro",
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			run = noTestDirAndNoReboot(run, distTest, isNormalTry)
			switch distTest {
			case "api",
				"go_test:cmd/go": // takes over 20 minutes without working SMP
				return false
			}
			return run
		},
		buildsRepo:  plan9Default,
		KnownIssues: []int{50137, 50878},
	})
	addBuilder(BuildConfig{
		Name:     "aix-ppc64",
		HostType: "host-aix-ppc64-osuosl",
		env: []string{
			"PATH=/opt/freeware/bin:/usr/bin:/etc:/usr/sbin:/usr/ucb:/usr/bin/X11:/sbin:/usr/java7_64/jre/bin:/usr/java7_64/bin",
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "vulndb", "vuln":
				// vulndb currently uses a dependency which does not build cleanly
				// on aix-ppc64. Until that issue is resolved, skip vulndb on
				// this builder.
				// (https://golang.org/issue/49218).
				return false
			}
			return buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:           "linux-amd64-wsl",
		HostType:       "host-linux-amd64-wsl",
		Notes:          "Windows 10 WSL2 Ubuntu",
		FlakyNet:       true,
		SkipSnapshot:   true, // The builder has a slow uplink bandwidth.
		privateGoProxy: true, // this builder is behind firewall
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-perf",
		HostType: "host-linux-amd64-perf",
		Notes:    "Performance testing for linux-amd64",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "benchmarks"
		},
		RunBench:     true,
		SkipSnapshot: true,
	})
}

// addBuilder adds c to the Builders map after doing some checks.
func addBuilder(c BuildConfig) {
	if c.Name == "" {
		panic("empty name")
	}
	if c.HostType == "" {
		panic(fmt.Sprintf("missing HostType for builder %q", c.Name))
	}
	if _, dup := Builders[c.Name]; dup {
		panic("dup name " + c.Name)
	}
	if _, ok := Hosts[c.HostType]; !ok {
		panic(fmt.Sprintf("undefined HostType %q for builder %q", c.HostType, c.Name))
	}
	if c.SkipSnapshot && (c.numTestHelpers > 0 || c.numTryTestHelpers > 0) {
		panic(fmt.Sprintf("config %q's SkipSnapshot is not compatible with sharded test helpers", c.Name))
	}
	for i, issue := range c.KnownIssues {
		if issue == 0 {
			panic(fmt.Errorf("config %q's KnownIssues slice has a zero issue at index %d", c.Name, i))
		}
	}

	types := 0
	for _, fn := range []func() bool{c.IsReverse, c.IsContainer, c.IsVM} {
		if fn() {
			types++
		}
	}
	if types != 1 {
		panic(fmt.Sprintf("build config %q host type inconsistent (must be Reverse, Image, or VM)", c.Name))
	}

	Builders[c.Name] = &c
}

// tryNewMiscCompile is an intermediate step towards adding a real addMiscCompile TryBot.
// It adds a post-submit-only builder with KnownIssue, GoDeps set to the provided values,
// and runs on a limited set of branches to get test results without potential disruption
// for contributors. It can be modified as needed when onboarding a misc-compile builder.
func tryNewMiscCompile(suffix, rx string, knownIssue int, goDeps []string) {
	if knownIssue == 0 {
		panic("tryNewMiscCompile: knownIssue parameter must be non-zero")
	}
	addBuilder(BuildConfig{
		Name:        "misc-compile" + suffix,
		HostType:    "host-linux-bullseye",
		buildsRepo:  func(repo, branch, goBranch string) bool { return repo == "go" && branch == "master" },
		KnownIssues: []int{knownIssue},
		GoDeps:      goDeps,
		env:         []string{"GO_DISABLE_OUTBOUND_NETWORK=1"},
		CompileOnly: true,
		Notes:       fmt.Sprintf("Tries buildall.bash to cross-compile & vet std+cmd packages for "+rx+", but doesn't run any tests. See golang.org/issue/%d.", knownIssue),
		allScriptArgs: []string{
			// Filtering pattern to buildall.bash:
			rx,
		},
	})
}

// fasterTrybots is a distTestAdjust policy function.
// It skips (returns false) the test/ directory and reboot tests for trybots.
func fasterTrybots(run bool, distTest string, isNormalTry bool) bool {
	if isNormalTry {
		if strings.HasPrefix(distTest, "test:") || distTest == "reboot" {
			return false // skip test
		}
	}
	return run
}

// noTestDirAndNoReboot is a distTestAdjust policy function.
// It skips (returns false) the test/ directory and reboot tests for all builds.
func noTestDirAndNoReboot(run bool, distTest string, isNormalTry bool) bool {
	if strings.HasPrefix(distTest, "test:") || distTest == "reboot" {
		return false // skip test
	}
	return run
}

// ppc64DistTestPolicy is a distTestAdjust policy function
// that's shared by linux-ppc64le, -ppc64le-power9osu, and -ppc64.
func ppc64DistTestPolicy(run bool, distTest string, isNormalTry bool) bool {
	if distTest == "reboot" {
		// Skip test. It seems to use a lot of memory?
		// See https://golang.org/issue/35233.
		return false
	}
	return run
}

// mipsDistTestPolicy is a distTestAdjust policy function
// that's shared by the slow mips builders.
func mipsDistTestPolicy(run bool, distTest string, isNormalTry bool) bool {
	switch distTest {
	case "api", "reboot":
		return false
	}
	return run
}

// mipsBuildsRepoPolicy is a buildsRepo policy function
// that's shared by the slow mips builders.
func mipsBuildsRepoPolicy(repo, branch, goBranch string) bool {
	switch repo {
	case "go", "net", "sys":
		return branch == "master" && goBranch == "master"
	default:
		return false
	}
}

// riscvDistTestPolicy is same as mipsDistTestPolicy for now.
var riscvDistTestPolicy = mipsDistTestPolicy

// loong64DistTestPolicy is a distTestAdjust policy function
func loong64DistTestPolicy(run bool, distTest string, isNormalTry bool) bool {
	switch distTest {
	case "api", "reboot":
		return false
	}
	return run
}

// loong64BuildsRepoPolicy is a buildsRepo policy function
func loong64BuildsRepoPolicy(repo, branch, goBranch string) bool {
	switch repo {
	case "go", "net", "sys":
		return branch == "master" && goBranch == "master"
	default:
		return false
	}
}

// TryBuildersForProject returns the builders that should run as part of
// a TryBot set for the given project.
// The project argument is of the form "go", "net", "sys", etc.
// The branch is the branch of that project ("master", "release-branch.go1.12", etc)
// The goBranch is the branch of Go to use. If proj == "go", then branch == goBranch.
func TryBuildersForProject(proj, branch, goBranch string) []*BuildConfig {
	return buildersForProject(proj, branch, goBranch, (*BuildConfig).BuildsRepoTryBot)
}

// isBuilderFunc is the type of functions that report whether a builder
// should be run given a project, branch and goBranch.
type isBuilderFunc func(conf *BuildConfig, proj, branch, goBranch string) bool

// buildersForProject returns the builders that should be run for the given project,
// using isBuilder to test each builder.
// See TryBuildersForProject for the valid forms of proj, branch and goBranch.
func buildersForProject(proj, branch, goBranch string, isBuilder isBuilderFunc) []*BuildConfig {
	var confs []*BuildConfig
	for _, conf := range Builders {
		if isBuilder(conf, proj, branch, goBranch) {
			confs = append(confs, conf)
		}
	}
	sort.Slice(confs, func(i, j int) bool {
		return confs[i].Name < confs[j].Name
	})
	return confs
}

// atLeastGo1 reports whether branch is "release-branch.go1.N" where N >= min.
// It assumes "master" and "dev.*" branches are already greater than min, and
// always includes them.
func atLeastGo1(branch string, min int) bool {
	if branch == "master" {
		return true
	}
	if strings.HasPrefix(branch, "dev.") {
		// Treat dev branches current.
		// If a dev branch is active, it will be current.
		// If it is not active, it doesn't matter anyway.
		// TODO: dev.boringcrypto.go1.N branches may be the
		// exception. Currently we only build boringcrypto
		// on linux/amd64 and windows/386, which support all
		// versions of Go, so it doesn't actually matter.
		return true
	}
	major, minor, ok := version.ParseReleaseBranch(branch)
	return ok && major == 1 && minor >= min
}

// atMostGo1 reports whether branch is "release-branch.go1.N" where N <= max.
// It assumes "master" branch is already greater than max, and doesn't include it.
func atMostGo1(branch string, max int) bool {
	major, minor, ok := version.ParseReleaseBranch(branch)
	return ok && major == 1 && minor <= max
}

// onlyGo is a common buildsRepo policy value that only builds the main "go" repo.
func onlyGo(repo, branch, goBranch string) bool { return repo == "go" }

// onlyMasterDefault is a common buildsRepo policy value that only builds
// default repos on the master branch.
func onlyMasterDefault(repo, branch, goBranch string) bool {
	return branch == "master" && goBranch == "master" && buildRepoByDefault(repo)
}

// plan9Default is like onlyMasterDefault, but also omits repos that are
// both filesystem-intensive and unlikely to be relevant to plan9 users.
func plan9Default(repo, branch, goBranch string) bool {
	switch repo {
	case "review":
		// The x/review repo tests a Git hook, but the plan9 "git" doesn't have the
		// same command-line API as "git" everywhere else.
		return false
	case "website":
		// The x/website tests read and check the website code snippets,
		// which require many filesystem walk and read operations.
		return false
	case "vulndb", "vuln":
		// vulncheck can't read plan9 binaries.
		return false
	default:
		return onlyMasterDefault(repo, branch, goBranch)
	}
}

// disabledBuilder is a buildsRepo policy function that always return false.
func disabledBuilder(repo, branch, goBranch string) bool { return false }

// macTestPolicy is the test policy for Macs.
//
// We have limited Mac resources. It's not worth wasting time testing
// portable things on them. That is, if there's a slow test that will
// still fail slowly on another builder where we have more resources
// (like linux-amd64), then there's no point testing it redundantly on
// the Macs.
func macTestPolicy(run bool, distTest string, isNormalTry bool) bool {
	if strings.HasPrefix(distTest, "test:") {
		return false
	}
	switch distTest {
	case "reboot", "api", "doc_progs",
		"wiki", "bench_go1", "codewalk":
		return false
	}
	if isNormalTry {
		switch distTest {
		case "runtime:cpu124", "race", "moved_goroot":
			return false
		}
		// TODO: more. Look at bigquery results once we have more data.
	}
	return run
}
