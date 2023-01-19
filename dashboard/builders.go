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

	"386":                   "linux-386",
	"aix":                   "aix-ppc64",
	"amd64":                 "linux-amd64",
	"android":               "android-amd64-emu",
	"android-386":           "android-386-emu",
	"android-amd64":         "android-amd64-emu",
	"android-arm":           "android-arm-corellium",
	"android-arm64":         "android-arm64-corellium",
	"arm":                   "linux-arm-aws",
	"arm64":                 "linux-arm64",
	"darwin":                "darwin-amd64-12_0",
	"darwin-amd64":          "darwin-amd64-12_0",
	"darwin-arm64":          "darwin-arm64-12",
	"ios-arm64":             "ios-arm64-corellium",
	"dragonfly":             "dragonfly-amd64-622",
	"dragonfly-amd64":       "dragonfly-amd64-622",
	"freebsd":               "freebsd-amd64-13_0",
	"freebsd-386":           "freebsd-386-13_0",
	"freebsd-amd64":         "freebsd-amd64-13_0",
	"freebsd-arm":           "freebsd-arm-paulzhol",
	"freebsd-arm64":         "freebsd-arm64-dmgk",
	"freebsd-riscv64":       "freebsd-riscv64-unmatched",
	"illumos":               "illumos-amd64",
	"ios":                   "ios-arm64-corellium",
	"js":                    "js-wasm",
	"linux":                 "linux-amd64",
	"linux-arm":             "linux-arm-aws",
	"linux-loong64":         "linux-loong64-3a5000",
	"linux-mips":            "linux-mips-rtrk",
	"linux-mips64":          "linux-mips64-rtrk",
	"linux-mips64le":        "linux-mips64le-rtrk",
	"linux-mipsle":          "linux-mipsle-rtrk",
	"linux-ppc64":           "linux-ppc64-sid-buildlet",
	"linux-ppc64le":         "linux-ppc64le-buildlet",
	"linux-ppc64le-power9":  "linux-ppc64le-power9osu",
	"linux-ppc64le-power10": "linux-ppc64le-power10osu",
	"linux-riscv64":         "linux-riscv64-unmatched",
	"linux-s390x":           "linux-s390x-ibm",
	"longtest":              "linux-amd64-longtest",
	"loong64":               "linux-loong64-3a5000",
	"mips":                  "linux-mips-rtrk",
	"mips64":                "linux-mips64-rtrk",
	"mips64le":              "linux-mips64le-rtrk",
	"mipsle":                "linux-mipsle-rtrk",
	"netbsd":                "netbsd-amd64-9_3",
	"netbsd-386":            "netbsd-386-9_3",
	"netbsd-amd64":          "netbsd-amd64-9_3",
	"netbsd-arm":            "netbsd-arm-bsiegert",
	"netbsd-arm64":          "netbsd-arm64-bsiegert",
	"nocgo":                 "linux-amd64-nocgo",
	"openbsd":               "openbsd-amd64-72",
	"openbsd-386":           "openbsd-386-72",
	"openbsd-amd64":         "openbsd-amd64-72",
	"openbsd-arm":           "openbsd-arm-jsing",
	"openbsd-arm64":         "openbsd-arm64-jsing",
	"openbsd-mips64":        "openbsd-mips64-jsing",
	"plan9":                 "plan9-arm",
	"plan9-386":             "plan9-386-0intro",
	"plan9-amd64":           "plan9-amd64-0intro",
	"ppc64":                 "linux-ppc64-sid-buildlet",
	"ppc64le":               "linux-ppc64le-buildlet",
	"ppc64lep9":             "linux-ppc64le-power9osu",
	"ppc64lep10":            "linux-ppc64le-power10osu",
	"riscv64":               "linux-riscv64-unmatched",
	"s390x":                 "linux-s390x-ibm",
	"solaris":               "solaris-amd64-oraclerel",
	"solaris-amd64":         "solaris-amd64-oraclerel",
	"wasm":                  "js-wasm",
	"windows":               "windows-amd64-2016",
	"windows-386":           "windows-386-2008",
	"windows-amd64":         "windows-amd64-2016",
	"windows-arm":           "windows-arm-zx2c4",
	"windows-arm64":         "windows-arm64-11",
}

// Builders are the different build configurations.
// The keys are like "darwin-amd64" or "linux-386-387".
// This map should not be modified by other packages.
// Initialization happens below, via calls to addBuilder.
var Builders = map[string]*BuildConfig{}

// GoBootstrap is the bootstrap Go version.
// Bootstrap Go builds with this name must be in the bucket,
// usually uploaded by 'genbootstrap -upload all'.
const GoBootstrap = "go1.17.13"

// Hosts contains the names and configs of all the types of
// buildlets. They can be VMs, containers, or dedicated machines.
//
// Please keep table sorted by map key.
var Hosts = map[string]*HostConfig{
	"host-aix-ppc64-osuosl": {
		Notes:     "AIX 7.2 VM on OSU; run by Tony Reix",
		Owners:    []*gophers.Person{gh("trex58")},
		IsReverse: true,
		ExpectNum: 1,
	},
	"host-android-arm64-corellium-android": {
		Notes:       "Virtual Android devices hosted by Zenly on Corellium; see issues 31722 and 40523",
		Owners:      []*gophers.Person{gh("steeve"), gh("changkun")}, // See https://groups.google.com/g/golang-dev/c/oiuIE7qrWp0.
		IsReverse:   true,
		ExpectNum:   3,
		GoBootstrap: "none", // image has Go 1.15.3 (go.dev/issue/54246); cannot access storage.googleapis.com
		env: []string{
			"GOROOT_BOOTSTRAP=/data/data/com.termux/files/home/go-android-arm64-bootstrap",
			// Only run one job at a time to avoid the OOM killer.
			// Issue 50084.
			"GOMAXPROCS=1",
		},
	},
	"host-darwin-amd64-10_14-aws": {
		IsReverse:       true,
		ExpectNum:       2,
		Notes:           "AWS macOS Mojave (10.14) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-amd64-10_15-aws": {
		IsReverse:       true,
		ExpectNum:       2,
		Notes:           "AWS macOS Catalina (10.15) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-amd64-11-aws": {
		IsReverse:       true,
		ExpectNum:       2,
		Notes:           "AWS macOS Big Sur (11) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-amd64-12-aws": {
		IsReverse:       true,
		ExpectNum:       6,
		Notes:           "AWS macOS Monterey (12) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-amd64-13-aws": {
		IsReverse:       true,
		ExpectNum:       2,
		Notes:           "AWS macOS Ventura (13) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-arm64-11": {
		IsReverse:     true,
		Notes:         "macOS Big Sur (11) ARM64 (M1) on Mac minis in a Google office",
		ExpectNum:     3,
		SSHUsername:   "gopher",
		GoogleReverse: true,
	},
	"host-darwin-arm64-12": {
		IsReverse:     true,
		ExpectNum:     3,
		Notes:         "macOS Monterey (12) ARM64 (M1) on Mac minis in a Google office",
		SSHUsername:   "gopher",
		GoogleReverse: true,
	},
	"host-dragonfly-amd64-622": {
		Notes:       "DragonFly BSD 6.2.2 on GCE, built from build/env/dragonfly-amd64",
		VMImage:     "dragonfly-amd64-622",
		SSHUsername: "root",
	},
	"host-freebsd-amd64-12_3": {
		VMImage:     "freebsd-amd64-123-stable-20211230",
		Notes:       "FreeBSD 12.3; GCE VM, built from build/env/freebsd-amd64",
		SSHUsername: "gopher",
	},
	"host-freebsd-amd64-13_0": {
		VMImage:     "freebsd-amd64-130-stable-20211230",
		Notes:       "FreeBSD 13.0; GCE VM, built from build/env/freebsd-amd64",
		SSHUsername: "gopher",
	},
	"host-freebsd-arm-paulzhol": {
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "Raspberry Pi 3 Model B, FreeBSD 13.1-RELEASE with SCHED_4BSD",
		Owners:    []*gophers.Person{gh("paulzhol")},
	},
	"host-freebsd-arm64-dmgk": {
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "AWS EC2 a1.large 2 vCPU 4GiB RAM, FreeBSD 12.1-STABLE",
		Owners:    []*gophers.Person{gh("dmgk")},
	},
	"host-freebsd-riscv64-unmatched": {
		IsReverse:   true,
		ExpectNum:   1,
		Notes:       "SiFive HiFive Unmatched RISC-V board. 16 GB RAM. FreeBSD 13.1-RELEASE",
		Owners:      []*gophers.Person{gh("mengzhuo")},
		GoBootstrap: "none",
		env: []string{
			"GOROOT_BOOTSTRAP=/home/gopher/go-freebsd-riscv64-bootstrap",
		},
	},
	"host-illumos-amd64-jclulow": {
		Notes:       "SmartOS base64@19.1.0 zone",
		Owners:      []*gophers.Person{gh("jclulow")},
		IsReverse:   true,
		ExpectNum:   1,
		SSHUsername: "gobuild",
	},
	"host-ios-arm64-corellium-ios": {
		Notes:       "Virtual iOS devices hosted by Zenly on Corellium; see issues 31722 and 40523",
		Owners:      []*gophers.Person{gh("steeve"), gh("changkun")}, // See https://groups.google.com/g/golang-dev/c/oiuIE7qrWp0.
		IsReverse:   true,
		ExpectNum:   3,
		GoBootstrap: "none", // image has devel d6c4583ad4 (pre-Go 1.18) Dec 2021 (go.dev/issue/54246); cannot access storage.googleapis.com
		env: []string{
			"GOROOT_BOOTSTRAP=/var/root/go-ios-arm64-bootstrap",
		},
	},
	"host-linux-amd64-alpine": {
		Notes:          "Alpine container",
		ContainerImage: "linux-x86-alpine:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-androidemu": {
		Notes:          "Debian Bullseye w/ Android SDK + emulator (use nested virt)",
		ContainerImage: "android-amd64-emu:bff27c0c9263",
		KonletVMImage:  "android-amd64-emu-bullseye",
		NestedVirt:     true,
		SSHUsername:    "root",
	},
	"host-linux-amd64-bullseye": {
		Notes:          "Debian Bullseye",
		ContainerImage: "linux-x86-bullseye:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-bullseye-vmx": {
		Notes:          "Debian Bullseye w/ Nested Virtualization (VMX CPU bit) enabled",
		ContainerImage: "linux-x86-bullseye:latest",
		NestedVirt:     true,
		SSHUsername:    "root",
	},
	"host-linux-amd64-buster": {
		Notes:          "Debian Buster",
		ContainerImage: "linux-x86-buster:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-clang": {
		Notes:          "Container with clang.",
		ContainerImage: "linux-x86-clang:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-fedora": {
		Notes:          "Fedora 30",
		ContainerImage: "linux-x86-fedora:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-js-wasm": {
		Notes:          "Container with Node.js 14 for testing js/wasm.",
		ContainerImage: "js-wasm:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-js-wasm-node18": {
		Notes:          "Container with Node.js 18 for testing js/wasm.",
		ContainerImage: "js-wasm-node18:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-localdev": {
		IsReverse:   true,
		ExpectNum:   0,
		Notes:       "for localhost development of buildlets/gomote/coordinator only",
		SSHUsername: os.Getenv("USER"),
	},
	"host-linux-amd64-perf": {
		Notes:               "Cascade Lake performance testing machines",
		machineType:         "c2-standard-8", // C2 has precisely defined, consistent server architecture.
		ContainerImage:      "linux-x86-bullseye:latest",
		SSHUsername:         "root",
		CustomDeleteTimeout: 8 * time.Hour,
	},
	"host-linux-amd64-s390x-cross": {
		Notes:          "Container with s390x cross-compiler.",
		ContainerImage: "linux-s390x-cross:latest",
	},
	"host-linux-amd64-sid": {
		Notes:          "Debian sid, updated occasionally.",
		ContainerImage: "linux-x86-sid:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-stretch": {
		Notes:          "Debian Stretch",
		ContainerImage: "linux-x86-stretch:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-wsl": {
		Notes:     "Windows 10 WSL2 Ubuntu",
		Owners:    []*gophers.Person{gh("mengzhuo")},
		IsReverse: true,
		ExpectNum: 2,
	},
	"host-linux-arm-aws": {
		Notes:          "Debian Buster, EC2 arm instance. See x/build/env/linux-arm/aws",
		VMImage:        "ami-07409163bccd5ac4d",
		ContainerImage: "gobuilder-arm-aws:latest",
		machineType:    "m6g.xlarge",
		isEC2:          true,
		SSHUsername:    "root",
	},
	"host-linux-arm64-bullseye": {
		Notes:           "Debian Bullseye",
		ContainerImage:  "linux-arm64-bullseye:latest",
		machineType:     "t2a",
		SSHUsername:     "root",
		cosArchitecture: CosArchARM64,
	},
	"host-linux-arm64-bullseye-high-disk": {
		Notes:           "Debian Bullseye, larger boot disk size",
		ContainerImage:  "linux-arm64-bullseye:latest",
		machineType:     "t2a",
		SSHUsername:     "root",
		cosArchitecture: CosArchARM64,
		RootDriveSizeGB: 20,
	},
	"host-linux-loong64-3a5000": {
		Notes:       "Loongson 3A5000 Box hosted by Loongson; loong64 is the short name of LoongArch 64 bit version",
		Owners:      []*gophers.Person{gh("XiaodongLoong"), gh("abner-chenc")},
		IsReverse:   true,
		ExpectNum:   5,
		GoBootstrap: "none",
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/lib/go-linux-loong64-bootstrap",
		},
	},
	"host-linux-mips64-rtrk": {
		Notes:     "cavium,rhino_utm8 board hosted at RT-RK.com; quad-core cpu, 8GB of ram and 240GB ssd disks.",
		Owners:    []*gophers.Person{gh("draganmladjenovic")}, // See https://github.com/golang/go/issues/53574#issuecomment-1169891255.
		IsReverse: true,
		ExpectNum: 1,
	},
	"host-linux-mips64le-rtrk": {
		Notes:     "cavium,rhino_utm8 board hosted at RT-RK.com; quad-core cpu, 8GB of ram and 240GB ssd disks.",
		Owners:    []*gophers.Person{gh("draganmladjenovic")}, // See https://github.com/golang/go/issues/53574#issuecomment-1169891255.
		IsReverse: true,
		ExpectNum: 1,
	},
	"host-linux-ppc64-sid": {
		Notes:           "Debian sid; run by Go team on osuosl.org",
		Owners:          []*gophers.Person{gh("pmur")},
		IsReverse:       true,
		ExpectNum:       5,
		SSHUsername:     "root",
		HermeticReverse: true,
	},
	"host-linux-ppc64le-osu": {
		Notes:           "Ubuntu 20.04; run by Go team on osuosl.org; see x/build/env/linux-ppc64le/osuosl",
		Owners:          []*gophers.Person{gh("pmur")},
		IsReverse:       true,
		ExpectNum:       5,
		SSHUsername:     "root",
		HermeticReverse: true,
	},
	"host-linux-ppc64le-power10-osu": {
		Notes:     "Ubuntu 20.04; run by Go team on osuosl.org; see x/build/env/linux-ppc64le/osuosl",
		Owners:    []*gophers.Person{gh("pmur")},
		IsReverse: true,
		// GOPPC64=power10 is only supported in go1.20 and later. The container provides a patched boostrap compiler.
		env: []string{
			"GOPPC64=power10",
			"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		SSHUsername:     "root",
		HermeticReverse: true,
	},
	"host-linux-ppc64le-power9-osu": {
		Notes:           "Ubuntu 20.04; run by Go team on osuosl.org; see x/build/env/linux-ppc64le/osuosl",
		Owners:          []*gophers.Person{gh("pmur")},
		IsReverse:       true,
		env:             []string{"GOPPC64=power9"},
		SSHUsername:     "root",
		HermeticReverse: true,
	},
	"host-linux-riscv64-joelsing": {
		Notes:     "SiFive HiFive Unleashed RISC-V board. 8 GB RAM, 4 cores.",
		IsReverse: true,
		ExpectNum: 1,
		Owners:    []*gophers.Person{gh("4a6f656c")},
	},
	"host-linux-riscv64-unmatched": {
		Notes:     "SiFive HiFive Unmatched RISC-V board. 16 GB RAM, 4 cores.",
		IsReverse: true,
		ExpectNum: 2,
		Owners:    []*gophers.Person{gh("mengzhuo")},
	},
	"host-linux-s390x": {
		Notes:     "run by IBM",
		Owners:    []*gophers.Person{gh("Vishwanatha-HD"), gh("srinivas-pokala")},
		IsReverse: true,
		ExpectNum: 2, // See https://github.com/golang/go/issues/49557#issuecomment-969148789.
	},
	"host-netbsd-386-9_3": {
		VMImage:     "netbsd-i386-9-3-202211120320",
		Notes:       "NetBSD 9.3; GCE VM is built from script in build/env/netbsd-386",
		machineType: "n2", // force Intel; see go.dev/issue/49209
		SSHUsername: "root",
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-netbsd-amd64-9_3": {
		VMImage:     "netbsd-amd64-9-3-202211120320v2",
		Notes:       "NetBSD 9.3; GCE VM is built from script in build/env/netbsd-amd64",
		machineType: "n2", // force Intel; see go.dev/issue/49209
		SSHUsername: "root",
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-netbsd-arm-bsiegert": {
		IsReverse:   true,
		ExpectNum:   1,
		Owners:      []*gophers.Person{gh("bsiegert")},
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-netbsd-arm64-bsiegert": {
		IsReverse:   true,
		ExpectNum:   1,
		Owners:      []*gophers.Person{gh("bsiegert")},
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-openbsd-386-72": {
		VMImage:     "openbsd-386-72",
		machineType: "n2", // force Intel; see go.dev/issue/49209
		Notes:       "OpenBSD 7.2; GCE VM, built from build/env/openbsd-386",
		SSHUsername: "gopher",
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-openbsd-amd64-72": {
		VMImage:     "openbsd-amd64-72",
		machineType: "n2", // force Intel; see go.dev/issue/49209
		Notes:       "OpenBSD 7.2; GCE VM, built from build/env/openbsd-amd64",
		SSHUsername: "gopher",
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-openbsd-arm-joelsing": {
		IsReverse:   true,
		ExpectNum:   1,
		Owners:      []*gophers.Person{gh("4a6f656c")},
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-openbsd-arm64-joelsing": {
		IsReverse:   true,
		ExpectNum:   1,
		Owners:      []*gophers.Person{gh("4a6f656c")},
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-openbsd-mips64-joelsing": {
		IsReverse:   true,
		ExpectNum:   1,
		Owners:      []*gophers.Person{gh("4a6f656c")},
		GoBootstrap: "go1.19.2", // Go 1.17 is too old; see go.dev/issue/42422
	},
	"host-plan9-386-0intro": {
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "QEMU VM, Plan 9 from Bell Labs",
		Owners:    []*gophers.Person{gh("0intro")},
	},
	"host-plan9-386-gce": {
		VMImage: "plan9-386-v7",
		Notes:   "Plan 9 from 0intro; GCE VM, built from build/env/plan9-386",
		env:     []string{"GO_TEST_TIMEOUT_SCALE=3"},
	},
	"host-plan9-amd64-0intro": {
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "QEMU VM, Plan 9 from Bell Labs, 9k kernel",
		Owners:    []*gophers.Person{gh("0intro")},
	},
	"host-plan9-arm-0intro": {
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "Raspberry Pi 3 Model B, Plan 9 from Bell Labs",
		Owners:    []*gophers.Person{gh("0intro")},
	},
	"host-solaris-oracle-amd64-oraclerel": {
		Notes:     "Oracle Solaris amd64 Release System",
		HostArch:  "solaris-amd64",
		Owners:    []*gophers.Person{gh("rorth")}, // https://github.com/golang/go/issues/15581#issuecomment-550368581
		IsReverse: true,
		ExpectNum: 1,
	},
	"host-windows-amd64-2008": {
		VMImage:     "windows-amd64-server-2008r2-v8",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2008-oldcc": {
		VMImage:     "windows-amd64-server-2008r2-v7",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2012": {
		VMImage:     "windows-amd64-server-2012r2-v8",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2012-oldcc": {
		VMImage:     "windows-amd64-server-2012r2-v7",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2016": {
		VMImage:     "windows-amd64-server-2016-v8",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2016-big": {
		VMImage:     "windows-amd64-server-2016-v8",
		machineType: "e2-standard-16",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2016-big-oldcc": {
		VMImage:     "windows-amd64-server-2016-v7",
		machineType: "e2-standard-16",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2016-oldcc": {
		VMImage:     "windows-amd64-server-2016-v7",
		SSHUsername: "gopher",
	},
	"host-windows-arm64-zx2c4": {
		IsReverse: true,
		ExpectNum: 0,
		Owners:    []*gophers.Person{gh("zx2c4")},
	},
	"host-windows11-arm64-azure": { // host name known to cmd/buildlet/stage0, cannot change
		Notes:     "Azure windows 11 arm64 VMs",
		HostArch:  "windows-arm64",
		IsReverse: true,
		ExpectNum: 5,
	},
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
		if c.HostArch == "" {
			f := strings.Split(c.HostType, "-")
			if len(f) < 3 {
				panic(fmt.Sprintf("invalid HostType %q", c.HostType))
			}
			c.HostArch = f[1] + "-" + f[2] // "linux-amd64"
			if f[2] == "arm" {
				c.HostArch += "-7" // assume newer ARM
			}
		}
		if c.GoBootstrap == "" {
			c.GoBootstrap = GoBootstrap
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
	}
}

// CosArch defines the diffrent COS images types used.
type CosArch string

const (
	CosArchAMD64 CosArch = "cos-stable"       // COS image for AMD64 architecture
	CosArchARM64         = "cos-arm64-stable" // COS image for ARM64 architecture
)

// A HostConfig describes the available ways to obtain buildlets of
// different types. Some host configs can serve multiple
// builders. For example, a host config of "host-linux-amd64-bullseye" can
// serve linux-amd64, linux-amd64-race, linux-386, linux-386-387, etc.
type HostConfig struct {
	// HostType is the unique name of this host config. It is also
	// the key in the Hosts map.
	HostType string

	// HostArch is a string identifying the host architecture, to decide which binaries to run on it.
	// If unset, it is derived in func init from the HostType
	// (for example, host-linux-amd64-foo has HostArch "linux-amd64").
	// For clarity, the HostArch for 32-bit arm always ends in -5 or -7
	// to specify the GOARM value; implicit HostArch always use -7.
	// (This specificity is necessary because there is no one value that
	// works on all 32-bit ARM chips on non-Linux operating systems.)
	//
	// If set explicitly, HostArch should have the form "GOOS-GOARCH-suffix",
	// where suffix is the GO$GOARCH setting (that is, GOARM for GOARCH=arm).
	// For example "openbsd-arm-5" for GOOS=openbsd GOARCH=arm GOARM=5.
	//
	// (See the BuildletBinaryURL and GoBootstrapURL methods.)
	HostArch string

	// GoBootstrap is the version of Go to use for bootstrap.
	// A bootstrap toolchain built with that version must be in the bucket.
	// If unset, it is set in func init to GoBootstrap (the global constant).
	//
	// If GoBootstrap is set to "none", it means this buildlet is not given a new bootstrap
	// toolchain for each build, typically because it cannot download from
	// storage.googleapis.com.
	//
	// (See the GoBootstrapURL method.)
	GoBootstrap string

	// Exactly 1 of these must be set (with the exception of EC2 instances).
	// An EC2 instance may run a container inside a VM. In that case, a VMImage
	// and ContainerImage will both be set.
	VMImage        string // e.g. "openbsd-amd64-60"
	ContainerImage string // e.g. "linux-buildlet-std:latest" (suffix after "gcr.io/<PROJ>/")
	IsReverse      bool   // if true, only use the reverse buildlet pool

	// GCE options, if VMImage != "" || ContainerImage != ""
	machineType     string  // optional GCE instance type ("n2-standard-4") or instance class ("n2")
	RegularDisk     bool    // if true, use spinning disk instead of SSD
	MinCPUPlatform  string  // optional. e2 instances are not supported. see https://cloud.google.com/compute/docs/instances/specify-min-cpu-platform.
	cosArchitecture CosArch // optional. GCE instances which use COS need the architecture set. Default: CosArchAMD64

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
	GoogleReverse   bool // whether this reverse builder is owned by Google

	// Container image options, if ContainerImage != "":
	NestedVirt    bool   // container requires VMX nested virtualization. e2 and n2d instances are not supported.
	KonletVMImage string // optional VM image (containing konlet) to use instead of default

	// Optional base env.
	env []string

	Owners []*gophers.Person // owners; empty means golang-dev
	Notes  string            // notes for humans

	SSHUsername string // username to ssh as, empty means not supported

	RootDriveSizeGB int64 // optional, GCE instance root size in base-2 GB. Default: default size for instance type.
}

// CosArchitecture returns the COS architecture to use with the host. The default is CosArchAMD64.
func (hc *HostConfig) CosArchitecture() CosArch {
	if hc.cosArchitecture == CosArch("") {
		return CosArchAMD64
	}
	return hc.cosArchitecture
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
	// For example, "host-linux-amd64-bullseye".
	HostType string

	// KnownIssues is a slice of non-zero go.dev/issue/nnn numbers for a
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
	// GOPROXY environment value.
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

	env            []string // extra environment ("key=value") pairs
	makeScriptArgs []string // extra args to pass to the make.bash-equivalent script
	allScriptArgs  []string // extra args to pass to the all.bash-equivalent script

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
		// without the default -short flag. See go.dev/issue/12508.
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
	d *= time.Duration(c.GoTestTimeoutScale())
	return d
}

// GoTestTimeoutScale returns this builder's GO_TEST_TIMEOUT_SCALE value, or 1.
func (c *BuildConfig) GoTestTimeoutScale() int {
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
	hc := c.HostConfig()
	if hc.GoBootstrap == "none" {
		return ""
	}
	return "https://storage.googleapis.com/" + e.BuildletBucket +
		"/gobootstrap-" + hc.HostArch + "-" + hc.GoBootstrap + ".tar.gz"
}

// BuildletBinaryURL returns the public URL of this builder's buildlet.
func (c *HostConfig) BuildletBinaryURL(e *buildenv.Environment) string {
	return "https://storage.googleapis.com/" + e.BuildletBucket + "/buildlet." + c.HostArch
}

// IsRace reports whether this is a race builder.
// A race builder runs tests without the -short flag.
//
// A builder is considered to be a race builder
// if and only if it one of the components of the builder
// name is "race" (components are delimited by "-").
func (c *BuildConfig) IsRace() bool {
	for _, s := range strings.Split(c.Name, "-") {
		if s == "race" {
			return true
		}
	}
	return false
}

// IsLongTest reports whether this is a longtest builder.
// A longtest builder runs tests without the -short flag.
//
// A builder is considered to be a longtest builder
// if and only if it one of the components of the builder
// name is "longtest" (components are delimited by "-").
func (c *BuildConfig) IsLongTest() bool {
	for _, s := range strings.Split(c.Name, "-") {
		if s == "longtest" {
			return true
		}
	}
	return false
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
			bmm := types.MajorMinor{Major: bmaj, Minor: bmin}
			if bmm.Less(c.MinimumGoVersion) {
				return false
			}
			if repo == "exp" {
				// Don't test exp against release branches; it's experimental.
				return false
			}
		}
	}

	// Build dev.boringcrypto branches only on linux/amd64 and windows/386 (see go.dev/issue/26791).
	if repo == "go" && (branch == "dev.boringcrypto" || strings.HasPrefix(branch, "dev.boringcrypto.")) {
		if c.Name != "linux-amd64" && !strings.HasPrefix(c.Name, "windows-386") {
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
	return append([]string(nil), c.makeScriptArgs...)
}

// GorootFinal returns the default install location for
// releases for this platform.
func (c *BuildConfig) GorootFinal() string {
	if strings.HasPrefix(c.Name, "windows-") {
		return "c:\\go"
	}
	return "/usr/local/go"
}

// MachineType returns the AWS or GCE machine type to use for this builder.
func (c *HostConfig) MachineType() string {
	if c.IsEC2() {
		return c.machineType
	}
	typ := c.machineType
	if typ == "" {
		if c.NestedVirt || c.MinCPUPlatform != "" {
			// e2 instances do not support nested virtualization, but n2 instances do.
			// Same for MinCPUPlatform: https://cloud.google.com/compute/docs/instances/specify-min-cpu-platform#limitations
			typ = "n2"
		} else {
			typ = "e2"
		}
	}
	if strings.Contains(typ, "-") { // full type like "n2-standard-8"
		return typ
	}
	if c.IsContainer() {
		// Set a higher default machine size for containers,
		// so their /workdir tmpfs can be larger. The COS
		// image has no swap, so we want to make sure the
		// /workdir fits completely in memory.
		return typ + "-standard-16" // 16 vCPUs, 64 GB mem
	}
	return typ + "-standard-8" // 8 vCPUs, 32 GB mem
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
		return "debian-bullseye-vmx"
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

// IsGoogle reports whether this host is operated by Google,
// implying that we trust it to be secure and available.
func (c *HostConfig) IsGoogle() bool {
	if c.IsContainer() || c.IsVM() {
		return true
	}
	if c.IsReverse && c.GoogleReverse {
		return true
	}
	return false
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
		Name:     "freebsd-amd64-12_3",
		HostType: "host-freebsd-amd64-12_3",
		tryBot:   defaultTrySet("sys"),

		distTestAdjust:    fasterTrybots, // If changing this policy, update TestShouldRunDistTest accordingly.
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-386-12_3",
		HostType:          "host-freebsd-amd64-12_3",
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		distTestAdjust:    fasterTrybots,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-race",
		HostType: "host-freebsd-amd64-13_0",
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-amd64-13_0",
		HostType:          "host-freebsd-amd64-13_0",
		tryBot:            explicitTrySet("sys"),
		distTestAdjust:    fasterTrybots, // If changing this policy, update TestShouldRunDistTest accordingly.
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-386-13_0",
		HostType:          "host-freebsd-amd64-13_0",
		tryBot:            explicitTrySet("sys"),
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		distTestAdjust:    fasterTrybots,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:           "linux-386",
		HostType:       "host-linux-amd64-bullseye",
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
		HostType: "host-linux-amd64-bullseye",
		env:      []string{"GOARCH=386", "GOHOSTARCH=386", "GO386=softfloat"},
	})
	addBuilder(BuildConfig{
		Name:       "linux-amd64",
		HostType:   "host-linux-amd64-bullseye",
		tryBot:     defaultTrySet(),
		buildsRepo: defaultPlusExpBuildVulnDB,
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		numTestHelpers:    1,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-boringcrypto",
		HostType: "host-linux-amd64-bullseye",
		Notes:    "GOEXPERIMENT=boringcrypto",
		tryBot:   defaultTrySet(),
		env: []string{
			"GOEXPERIMENT=boringcrypto",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		numTestHelpers:    1,
		numTryTestHelpers: 4,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atLeastGo1(goBranch, 19) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:       "linux-amd64-vmx",
		HostType:   "host-linux-amd64-bullseye-vmx",
		buildsRepo: disabledBuilder,
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-alpine",
		HostType: "host-linux-amd64-alpine",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atLeastGo1(goBranch, 20) && buildRepoByDefault(repo)
		},
	})

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
			v = types.MajorMinor{Major: 1, Minor: min}
			alsoNote = fmt.Sprintf(" Applies to Go 1.%d and newer.", min)
		}
		addBuilder(BuildConfig{
			Name:     "misc-compile" + suffix,
			HostType: "host-linux-amd64-bullseye",
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
	// overall TryBot completion time (currently 10 minutes; see go.dev/issue/17104).
	//
	// The TestTryBotsCompileAllPorts test is used to detect any gaps in TryBot coverage
	// when new ports are added, and the misc-compile pairs below can be re-arranged.
	//
	// (In the past, we used flexible regexp patterns that matched all architectures
	// for a given GOOS value. However, over time as new architectures were added,
	// some misc-compile TryBot could become much slower than others.)
	//
	// See go.dev/issue/32632.
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
	addMiscCompileGo1(20, "-go1.20", "freebsd-riscv64")

	// TODO: Issue 25963, get the misc-compile trybots for Android/iOS.
	// Then consider subrepos too, so "mobile" can at least be included
	// as a misc-compile for ^android- and ^ios-.

	addBuilder(BuildConfig{
		Name:     "linux-amd64-nocgo",
		HostType: "host-linux-amd64-bullseye",
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
		HostType:   "host-linux-amd64-bullseye",
		buildsRepo: onlyGo,
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GO_GCFLAGS=-N -l",
		},
	})
	addBuilder(BuildConfig{
		Name:        "linux-amd64-ssacheck",
		HostType:    "host-linux-amd64-bullseye",
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
		HostType: "host-linux-amd64-bullseye",
		Notes:    "builder with GOEXPERIMENT=staticlockranking, see go.dev/issue/37937",
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
		HostType: "host-linux-amd64-buster",
		Notes:    "builder with GOEXPERIMENT=unified, see go.dev/issue/46786",
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
		numTestHelpers:    1,
		numTryTestHelpers: 4,
		KnownIssues:       []int{52150},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-nounified",
		HostType: "host-linux-amd64-buster",
		Notes:    "builder with GOEXPERIMENT=nounified, see go.dev/issue/51397",
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
		numTestHelpers:    1,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-goamd64v3",
		HostType: "host-linux-amd64-bullseye",
		Notes:    "builder with GOAMD64=v3, see proposal 45453 and issue 48505",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GOAMD64=v3",
		},
	})
	addBuilder(BuildConfig{
		Name:                "linux-amd64-racecompile",
		HostType:            "host-linux-amd64-bullseye",
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
		HostType:          "host-linux-amd64-bullseye",
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
		HostType: "host-linux-amd64-clang",
		Notes:    "Debian Buster + clang 7.0 instead of gcc",
		env:      []string{"CC=/usr/bin/clang", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-clang",
		HostType: "host-linux-amd64-clang",
		Notes:    "Debian Buster + clang 7.0 instead of gcc",
		env:      []string{"CC=/usr/bin/clang"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-sid",
		HostType: "host-linux-amd64-sid",
		Notes:    "Debian sid (unstable)",
		env:      []string{"GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-sid",
		HostType: "host-linux-amd64-sid",
		Notes:    "Debian sid (unstable)",
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-fedora",
		HostType: "host-linux-amd64-fedora",
		Notes:    "Fedora",
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-androidemu",
		HostType: "host-linux-amd64-androidemu",
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
		HostType: "host-linux-amd64-stretch",
		Notes:    "Debian Stretch.",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atMostGo1(goBranch, 19) && buildRepoByDefault(repo) // Stretch was EOL at the start of the 1.20 cycle.
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-bullseye",
		HostType: "host-linux-amd64-bullseye",
		Notes:    "Debian Bullseye.",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-buster",
		HostType: "host-linux-amd64-buster",
		Notes:    "Debian Buster.",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-stretch",
		HostType: "host-linux-amd64-stretch",
		Notes:    "Debian Stretch, 32-bit builder.",
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atMostGo1(goBranch, 19) && buildRepoByDefault(repo) // Stretch was EOL at the start of the 1.20 cycle.
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-buster",
		HostType: "host-linux-amd64-buster",
		Notes:    "Debian Buster, 32-bit builder.",
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-bullseye",
		HostType: "host-linux-amd64-bullseye",
		Notes:    "Debian Bullseye, 32-bit builder.",
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-longtest",
		HostType: "host-linux-amd64-bullseye",
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
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-longtest-race",
		HostType: "host-linux-amd64-bullseye",
		Notes:    "Debian Bullseye with the race detector enabled and go test -short=false",
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
			"GO_TEST_TIMEOUT_SCALE=5", // Inherited from the longtest builder.
		},
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/56907.
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-longtest",
		HostType: "host-linux-amd64-bullseye",
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
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "js-wasm",
		HostType: "host-linux-amd64-js-wasm",
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
				case "nolibgcc:crypto/x509", "reboot":
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
		Name:        "js-wasm-node18",
		HostType:    "host-linux-amd64-js-wasm-node18",
		KnownIssues: []int{57614},
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := buildRepoByDefault(repo) && atLeastGo1(goBranch, 21)
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
			if isNormalTry && (strings.Contains(distTest, "/internal/") || distTest == "reboot") {
				// Skip some tests in an attempt to speed up normal trybots, inherited from CL 121938.
				run = false
			}
			return run
		},
		numTryTestHelpers: 3,
		env: []string{
			"GOOS=js", "GOARCH=wasm", "GOHOSTOS=linux", "GOHOSTARCH=amd64",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/workdir/go/misc/wasm",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-amd64-72",
		HostType:          "host-openbsd-amd64-72",
		tryBot:            defaultTrySet(),
		distTestAdjust:    noTestDirAndNoReboot,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "openbsd-386-72",
		HostType: "host-openbsd-386-72",
		tryBot:   explicitTrySet("sys"),
		buildsRepo: func(repo, branch, goBranch string) bool {
			// https://go.dev/issue/49529: git seems to be too slow on this
			// platform.
			return repo != "review" && buildRepoByDefault(repo)
		},
		distTestAdjust:    noTestDirAndNoReboot,
		numTryTestHelpers: 4,
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
		Name:           "netbsd-386-9_3",
		HostType:       "host-netbsd-386-9_3",
		distTestAdjust: noTestDirAndNoReboot,
	})
	addBuilder(BuildConfig{
		Name:           "netbsd-amd64-9_3",
		HostType:       "host-netbsd-amd64-9_3",
		distTestAdjust: noTestDirAndNoReboot,
		tryBot:         explicitTrySet("sys"),
	})
	addBuilder(BuildConfig{
		Name:     "netbsd-arm-bsiegert",
		HostType: "host-netbsd-arm-bsiegert",
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "review" {
				// https://go.dev/issue/49530: This test seems to be too slow even
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
		FlakyNet: true,
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
		FlakyNet: true,
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
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has modern/recent C compilers installed,
			// meaning that we only want to use it with 1.20+ versions
			// of Go, hence the atLeastGo1 call below; versions of Go
			// prior to 1.20 will use the *-oldcc variant instead. See
			// issue 35006 for more details.
			return onlyGo(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
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
		Name:           "windows-amd64-2008-oldcc",
		HostType:       "host-windows-amd64-2008-oldcc",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has legacy C compilers installed, suitable
			// for versions of Go prior to 1.20, hence the atMostGo1
			// call below. Newer (1.20 and later) will use the
			// non-oldcc variant instead. See issue 35006 for more
			// details.
			return onlyGo(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
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
		Name:     "windows-386-2008",
		HostType: "host-windows-amd64-2008",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has modern/recent C compilers installed,
			// meaning that we only want to use it with 1.20+ versions
			// of Go, hence the atLeastGo1 call below; versions of Go
			// prior to 1.20 will use the *-oldcc variant instead. See
			// issue 35006 for more details.
			return defaultPlusExpBuild(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
		env: []string{"GOARCH=386", "GOHOSTARCH=386"},
		tryBot: func(repo, branch, goBranch string) bool {
			// See comment above about the atLeastGo1 call below.
			dft := defaultTrySet()
			return dft(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "windows-386-2008-oldcc",
		HostType: "host-windows-amd64-2008-oldcc",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has legacy C compilers installed, suitable
			// for versions of Go prior to 1.20, hence the atMostGo1
			// call below. Newer (1.20 and later) will use the
			// non-oldcc variant instead. See issue 35006 for more
			// details.
			return defaultPlusExpBuild(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
		distTestAdjust: fasterTrybots,
		env:            []string{"GOARCH=386", "GOHOSTARCH=386"},
		tryBot: func(repo, branch, goBranch string) bool {
			// See comment above about the atMostGo1 call below.
			dft := defaultTrySet()
			return dft(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:           "windows-386-2012",
		HostType:       "host-windows-amd64-2012",
		distTestAdjust: fasterTrybots,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has modern/recent C compilers installed,
			// meaning that we only want to use it with 1.20+ versions
			// of Go, hence the atLeastGo1 call below; versions of Go
			// prior to 1.20 will use the *-oldcc variant instead. See
			// issue 35006 for more details.
			return onlyGo(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
		env: []string{"GOARCH=386", "GOHOSTARCH=386"},
		tryBot: func(repo, branch, goBranch string) bool {
			// See comment above about the atLeastGo1 call below.
			dft := defaultTrySet()
			return dft(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:           "windows-386-2012-oldcc",
		HostType:       "host-windows-amd64-2012-oldcc",
		distTestAdjust: fasterTrybots,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has legacy C compilers installed, suitable
			// for versions of Go prior to 1.20, hence the atMostGo1
			// call below. Newer (1.20 and later) will use the
			// non-oldcc variant instead. See issue 35006 for more
			// details.
			return onlyGo(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
		env: []string{"GOARCH=386", "GOHOSTARCH=386"},
		tryBot: func(repo, branch, goBranch string) bool {
			// See comment above about the atMostGo1 call below.
			dft := defaultTrySet()
			return dft(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:           "windows-amd64-2012-oldcc",
		HostType:       "host-windows-amd64-2012-oldcc",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has legacy C compilers installed, suitable
			// for versions of Go prior to 1.20, hence the atMostGo1
			// call below. Newer (1.20 and later) will use the
			// non-oldcc variant instead. See issue 35006 for more
			// details.
			return onlyGo(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
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
		Name:           "windows-amd64-2012",
		HostType:       "host-windows-amd64-2012",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has modern/recent C compilers installed,
			// meaning that we only want to use it with 1.20+ versions
			// of Go, hence the atLeastGo1 call below; versions of Go
			// prior to 1.20 will use the *-oldcc variant instead. See
			// issue 35006 for more details.
			return onlyGo(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
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
		Name:     "windows-amd64-2016",
		HostType: "host-windows-amd64-2016",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has modern/recent C compilers installed,
			// meaning that we only want to use it with 1.20+ versions
			// of Go, hence the atLeastGo1 call below; versions of Go
			// prior to 1.20 will use the *-oldcc variant instead. See
			// issue 35006 for more details.
			return defaultPlusExpBuild(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
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
		tryBot: func(repo, branch, goBranch string) bool {
			// See comment above about the atLeastGo1 call below.
			dft := defaultTrySet()
			return dft(repo, branch, goBranch) &&
				atLeastGo1(goBranch, 20)
		},
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-2016-oldcc",
		HostType: "host-windows-amd64-2016-oldcc",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has legacy C compilers installed, suitable
			// for versions of Go prior to 1.20, hence the atMostGo1
			// call below. Newer (1.20 and later) will use the
			// non-oldcc variant instead. See issue 35006 for more
			// details.
			return defaultPlusExpBuild(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
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
		tryBot: func(repo, branch, goBranch string) bool {
			// See comment above about the atMostGo1 call below.
			dft := defaultTrySet()
			return dft(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
		},
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-longtest-oldcc",
		HostType: "host-windows-amd64-2016-big-oldcc",
		Notes:    "Windows Server 2016 with go test -short=false",
		tryBot: func(repo, branch, goBranch string) bool {
			onReleaseBranch := strings.HasPrefix(branch, "release-branch.")
			// This builder has legacy C compilers installed, suitable
			// for versions of Go prior to 1.20, hence the atMostGo1
			// call below. Newer (1.20 and later) will use the
			// non-oldcc variant instead. See issue 35006 for more
			// details.
			return atMostGo1(goBranch, 19) &&
				repo == "go" && onReleaseBranch // See issue 37827.
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			// See comment above about the atMostGo1 call below.
			b := defaultPlusExpBuild(repo, branch, goBranch) &&
				atMostGo1(goBranch, 19)
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
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-longtest",
		HostType: "host-windows-amd64-2016-big",
		Notes:    "Windows Server 2016 with go test -short=false",
		tryBot: func(repo, branch, goBranch string) bool {
			onReleaseBranch := strings.HasPrefix(branch, "release-branch.")
			// This builder has modern/recent C compilers installed,
			// meaning that we only want to use it with 1.20+ versions
			// of Go, hence the atLeastGo1 call below; versions of Go
			// prior to 1.20 will use the *-oldcc variant instead. See
			// issue 35006 for more details.
			return atLeastGo1(goBranch, 20) &&
				repo == "go" && onReleaseBranch // See issue 37827.
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			// See comment above about the atMostGo1 call below.
			b := atLeastGo1(goBranch, 20) &&
				defaultPlusExpBuild(repo, branch, goBranch)
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
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-oldcc-race",
		HostType: "host-windows-amd64-2016-oldcc",
		Notes:    "Only runs -race tests (./race.bat)",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has legacy C compilers installed, suitable
			// for versions of Go prior to 1.20, hence the atMostGo1
			// call below. Newer (1.20 and later) will use the
			// non-oldcc variant instead. See issue 35006 for more
			// details.
			return atMostGo1(goBranch, 19) &&
				buildRepoByDefault(repo)
		},
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
		Name:     "windows-amd64-race",
		HostType: "host-windows-amd64-2016",
		Notes:    "Only runs -race tests (./race.bat)",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// This builder has modern/recent C compilers installed,
			// meaning that we only want to use it with 1.20+ versions
			// of Go, hence the atLeastGo1 call below; versions of Go
			// prior to 1.20 will use the *-oldcc variant instead. See
			// issue 35006 for more details.
			return atLeastGo1(goBranch, 20) &&
				buildRepoByDefault(repo)
		},
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
		Name:     "windows-arm-zx2c4",
		HostType: "host-windows-arm64-zx2c4",
		env: []string{
			"GOARM=7",
			"GO_TEST_TIMEOUT_SCALE=3"},
	})
	addBuilder(BuildConfig{
		Name:              "windows-arm64-11",
		HostType:          "host-windows11-arm64-azure",
		numTryTestHelpers: 1,
		env: []string{
			"GOARCH=arm64",
			// Note: GOMAXPROCS=4 workaround for go.dev/issue/51019
			// tentatively removed here, since Azure VMs have 3x more
			// RAM than the previous win11/arm64 machines.
		},
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-10_14",
		HostType:       "host-darwin-amd64-10_14-aws",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExp,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-10_15",
		HostType:       "host-darwin-amd64-10_15-aws",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-11_0",
		HostType:       "host-darwin-amd64-11-aws",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-12_0",
		HostType:       "host-darwin-amd64-12-aws",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-13",
		HostType:       "host-darwin-amd64-13-aws",
		distTestAdjust: macTestPolicy,
		buildsRepo:     defaultPlusExpBuild,
	})
	addBuilder(BuildConfig{
		Name:           "darwin-amd64-nocgo",
		HostType:       "host-darwin-amd64-12-aws",
		distTestAdjust: noTestDirAndNoReboot,
		env:            []string{"CGO_ENABLED=0"},
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
		HostType:       "host-darwin-amd64-12-aws",
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
			"GO_TEST_TIMEOUT_SCALE=2", // inherited from cmd/dist's default for GOARCH=arm
		},
	})
	addBuilder(BuildConfig{
		Name:     "android-386-emu",
		HostType: "host-linux-amd64-androidemu", // same amd64 host is used for 386 builder
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
		HostType:          "host-linux-amd64-androidemu",
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
		Name:           "linux-ppc64-sid-buildlet",
		HostType:       "host-linux-ppc64-sid",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see go.dev/issues/44422
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64le-buildlet",
		HostType:       "host-linux-ppc64le-osu",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see go.dev/issues/44422
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64le-power9osu",
		HostType:       "host-linux-ppc64le-power9-osu",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see go.dev/issues/44422
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64le-power10osu",
		HostType:       "host-linux-ppc64le-power10-osu",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see go.dev/issues/44422
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atLeastGo1(goBranch, 20) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:              "linux-arm64",
		HostType:          "host-linux-arm64-bullseye",
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 1,
	})
	addBuilder(BuildConfig{
		Name:     "linux-arm64-boringcrypto",
		HostType: "host-linux-arm64-bullseye",
		env: []string{
			"GOEXPERIMENT=boringcrypto",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atLeastGo1(goBranch, 19) && buildRepoByDefault(repo)
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-arm64-longtest",
		HostType: "host-linux-arm64-bullseye-high-disk",
		Notes:    "Debian Bullseye with go test -short=false",
		tryBot: func(repo, branch, goBranch string) bool {
			onReleaseBranch := strings.HasPrefix(branch, "release-branch.")
			return repo == "go" && onReleaseBranch // See issue 37827.
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := atLeastGo1(goBranch, 20) && buildRepoByDefault(repo)
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
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
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
			"GO_TEST_TIMEOUT_SCALE=2", // inherited from cmd/dist's default for GOARCH=arm
		},
	})
	addBuilder(BuildConfig{
		FlakyNet:     true,
		HostType:     "host-linux-loong64-3a5000",
		Name:         "linux-loong64-3a5000",
		SkipSnapshot: true,
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			switch distTest {
			case "api", "reboot":
				return false
			}
			return run
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "go":
				return atLeastGo1(goBranch, 19)
			case "net", "sys":
				return branch == "master" && atLeastGo1(goBranch, 19)
			default:
				return false
			}
		},
		privateGoProxy: true, // this builder is behind firewall
		env: []string{
			"GOARCH=loong64",
			"GOHOSTARCH=loong64",
		},
		KnownIssues: []int{53116, 53093},
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
			"GO_TEST_TIMEOUT_SCALE=4", // inherited from cmd/dist's default for GOARCH=mips{,le,64,64le}
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
			"GO_TEST_TIMEOUT_SCALE=4", // inherited from cmd/dist's default for GOARCH=mips{,le,64,64le}
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
			"GO_TEST_TIMEOUT_SCALE=4", // inherited from cmd/dist's default for GOARCH=mips{,le,64,64le}
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
			"GO_TEST_TIMEOUT_SCALE=4", // inherited from cmd/dist's default for GOARCH=mips{,le,64,64le}
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
		distTestAdjust: riscvDistTestPolicy,
		privateGoProxy: true, // this builder is behind firewall
		buildsRepo: func(repo, branch, goBranch string) bool {
			// see https://go.dev/issue/53745
			if repo == "perf" {
				return false
			}
			return onlyMasterDefault(repo, branch, goBranch)
		},
	})
	addBuilder(BuildConfig{
		Name:           "linux-s390x-ibm",
		HostType:       "host-linux-s390x",
		numTestHelpers: 0,
		FlakyNet:       true,
	})
	addBuilder(BuildConfig{
		Name:        "linux-s390x-crosscompile",
		HostType:    "host-linux-amd64-s390x-cross",
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
		Name:         "dragonfly-amd64-622",
		HostType:     "host-dragonfly-amd64-622",
		Notes:        "DragonFly BSD 6.2.2, running on GCE",
		SkipSnapshot: true,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return atLeastGo1(goBranch, 20) && buildRepoByDefault(repo)
		},
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
			"GO_TEST_TIMEOUT_SCALE=8", // from builder's local environment as of 2022-12-06
		},
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-arm64-dmgk",
		HostType: "host-freebsd-arm64-dmgk",
	})
	addBuilder(BuildConfig{
		Name:           "freebsd-riscv64-unmatched",
		HostType:       "host-freebsd-riscv64-unmatched",
		env:            []string{"GO_TEST_TIMEOUT_SCALE=4"},
		FlakyNet:       true,
		distTestAdjust: riscvDistTestPolicy,
		privateGoProxy: true, // this builder is behind firewall
		SkipSnapshot:   true, // The builder has a slow uplink bandwidth.
		buildsRepo: func(repo, branch, goBranch string) bool {
			// see https://go.dev/issue/53745
			if repo == "perf" {
				return false
			}
			return onlyMasterDefault(repo, branch, goBranch)
		},
	})
	addBuilder(BuildConfig{
		Name:           "plan9-arm",
		HostType:       "host-plan9-arm-0intro",
		distTestAdjust: noTestDirAndNoReboot,
		buildsRepo:     plan9Default,
		KnownIssues:    []int{49338},
		env: []string{
			"GO_TEST_TIMEOUT_SCALE=3", // from builder's local environment as of 2022-12-06
		},
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
				// (https://go.dev/issue/49218).
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
			if repo == "benchmarks" {
				// Benchmark the main Go repo.
				return true
			}
			if repo == "tools" {
				// Benchmark x/tools.
				//
				// When benchmarking subrepos, we ignore the Go
				// commit and always use the most recent Go
				// release, meaning we get identical duplicate
				// runs for each Go commit that runs at the
				// same subrepo commit.
				//
				// Limit to running on release branches since
				// they have far fewer Go commits than tip,
				// thus reducing the the number of duplicate
				// runs.
				return strings.HasPrefix(goBranch, "release-branch.")
			}
			return false
		},
		RunBench:     true,
		SkipSnapshot: true,
		KnownIssues:  []int{53538},
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
	if c.HostConfig().GoogleReverse && !c.IsReverse() {
		panic("GoogleReverse is set but the builder isn't reverse")
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
		HostType:    "host-linux-amd64-bullseye",
		buildsRepo:  func(repo, branch, goBranch string) bool { return repo == "go" && branch == "master" },
		KnownIssues: []int{knownIssue},
		GoDeps:      goDeps,
		env:         []string{"GO_DISABLE_OUTBOUND_NETWORK=1"},
		CompileOnly: true,
		Notes:       fmt.Sprintf("Tries buildall.bash to cross-compile & vet std+cmd packages for "+rx+", but doesn't run any tests. See go.dev/issue/%d.", knownIssue),
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
// that's shared by linux-ppc64le, -ppc64le-power{9,10}-osu, and -ppc64.
func ppc64DistTestPolicy(run bool, distTest string, isNormalTry bool) bool {
	if distTest == "reboot" {
		// Skip test. It seems to use a lot of memory?
		// See https://go.dev/issue/35233.
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
