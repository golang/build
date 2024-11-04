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
	"golang.org/x/build/internal/migration"
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

	// Fully ported to LUCI and stopped in the coordinator.
	"js":                    "",
	"wasip1":                "",
	"wasm":                  "",
	"js-wasm":               "",
	"wasip1-wasm":           "",
	"ppc64":                 "",
	"ppc64p10":              "",
	"ppc64le":               "",
	"ppc64lep9":             "",
	"ppc64lep10":            "",
	"linux-ppc64":           "",
	"linux-ppc64-power10":   "",
	"linux-ppc64le":         "",
	"linux-ppc64le-power9":  "",
	"linux-ppc64le-power10": "",
	"loong64":               "",
	"linux-loong64":         "",

	"386":             "linux-386",
	"aix":             "aix-ppc64",
	"amd64":           "linux-amd64",
	"android":         "android-amd64-emu",
	"android-386":     "android-386-emu",
	"android-amd64":   "android-amd64-emu",
	"android-arm":     "android-arm-corellium",
	"android-arm64":   "android-arm64-corellium",
	"arm":             "linux-arm-aws",
	"arm64":           "linux-arm64",
	"boringcrypto":    "linux-amd64-boringcrypto",
	"darwin":          "darwin-amd64-13",
	"darwin-amd64":    "darwin-amd64-13",
	"darwin-arm64":    "darwin-arm64-12",
	"ios-arm64":       "ios-arm64-corellium",
	"dragonfly":       "dragonfly-amd64-622",
	"dragonfly-amd64": "dragonfly-amd64-622",
	"freebsd":         "freebsd-amd64-13_0",
	"freebsd-386":     "freebsd-386-13_0",
	"freebsd-amd64":   "freebsd-amd64-13_0",
	"freebsd-arm":     "freebsd-arm-paulzhol",
	"freebsd-arm64":   "freebsd-arm64-dmgk",
	"freebsd-riscv64": "freebsd-riscv64-unmatched",
	"illumos":         "illumos-amd64",
	"ios":             "ios-arm64-corellium",
	"linux":           "linux-amd64",
	"linux-arm":       "linux-arm-aws",
	"linux-mips":      "linux-mips-rtrk",
	"linux-mips64":    "linux-mips64-rtrk",
	"linux-mips64le":  "linux-mips64le-rtrk",
	"linux-mipsle":    "linux-mipsle-rtrk",
	"linux-riscv64":   "linux-riscv64-unmatched",
	"linux-s390x":     "linux-s390x-ibm",
	"longtest":        "linux-amd64-longtest",
	"mips":            "linux-mips-rtrk",
	"mips64":          "linux-mips64-rtrk",
	"mips64le":        "linux-mips64le-rtrk",
	"mipsle":          "linux-mipsle-rtrk",
	"netbsd":          "netbsd-amd64-9_3",
	"netbsd-386":      "netbsd-386-9_3",
	"netbsd-amd64":    "netbsd-amd64-9_3",
	"netbsd-arm":      "netbsd-arm-bsiegert",
	"netbsd-arm64":    "netbsd-arm64-bsiegert",
	"nocgo":           "linux-amd64-nocgo",
	"openbsd":         "openbsd-amd64-72",
	"openbsd-386":     "openbsd-386-72",
	"openbsd-amd64":   "openbsd-amd64-72",
	"openbsd-arm":     "openbsd-arm-jsing",
	"openbsd-arm64":   "openbsd-arm64-jsing",
	"openbsd-mips64":  "openbsd-mips64-jsing",
	"openbsd-ppc64":   "openbsd-ppc64-n2vi",
	"openbsd-riscv64": "openbsd-riscv64-jsing",
	"plan9":           "plan9-arm",
	"plan9-386":       "plan9-386-0intro",
	"plan9-amd64":     "plan9-amd64-0intro",
	"riscv64":         "linux-riscv64-unmatched",
	"s390x":           "linux-s390x-ibm",
	"solaris":         "solaris-amd64-oraclerel",
	"solaris-amd64":   "solaris-amd64-oraclerel",
	"windows":         "windows-amd64-2016",
	"windows-386":     "windows-386-2016",
	"windows-amd64":   "windows-amd64-2016",
	"windows-arm":     "windows-arm-zx2c4",
	"windows-arm64":   "windows-arm64-11",
}

// Builders are the different build configurations.
// The keys are like "darwin-amd64" or "linux-386-387".
// This map should not be modified by other packages.
// Initialization happens below, via calls to addBuilder.
var Builders = map[string]*BuildConfig{}

// GoBootstrap is the bootstrap Go version.
//
// For bootstrap versions Go 1.21.0 and newer,
// bootstrap Go builds for Windows (only) with this name must be in the buildlet bucket,
// usually uploaded by 'genbootstrap -upload windows-*'.
// However, as of 2024-08-16 all existing coordinator builders for Windows have migrated
// to LUCI, so nothing needs to be done for Windows after all.
const GoBootstrap = "go1.22.6"

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
	"host-darwin-amd64-10_15-aws": {
		IsReverse:       true,
		ExpectNum:       0, // was 2 before migration to LUCI
		Notes:           "AWS macOS Catalina (10.15) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-amd64-11-aws": {
		IsReverse:       true,
		ExpectNum:       0, // was 2 before migration to LUCI
		Notes:           "AWS macOS Big Sur (11) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-amd64-12-aws": {
		IsReverse:       true,
		ExpectNum:       0, // was 6 before migration to LUCI
		Notes:           "AWS macOS Monterey (12) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-amd64-13-aws": {
		IsReverse:       true,
		ExpectNum:       0, // was 2 before migration to LUCI
		Notes:           "AWS macOS Ventura (13) VM under QEMU",
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & recreate
		GoogleReverse:   true,
	},
	"host-darwin-arm64-11": {
		IsReverse:     true,
		Notes:         "macOS Big Sur (11) ARM64 (M1) on Mac minis in a Google office",
		ExpectNum:     0, // was 3 before migration to LUCI
		SSHUsername:   "gopher",
		GoogleReverse: true,
	},
	"host-darwin-arm64-12": {
		IsReverse:     true,
		ExpectNum:     0, // was 3 before migration to LUCI
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
	"host-linux-amd64-bookworm": {
		Notes:          "Debian Bookworm",
		ContainerImage: "linux-x86-bookworm:latest",
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
		CustomDeleteTimeout: 12 * time.Hour,
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
	"host-linux-amd64-wasip1-wasm-wasmedge": {
		Notes:          "Container with wasmedge for testing wasip1/wasm.",
		ContainerImage: "wasip1-wasm-wasmedge:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-wasip1-wasm-wasmer": {
		Notes:          "Container with wasmer for testing wasip1/wasm.",
		ContainerImage: "wasip1-wasm-wasmer:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-wasip1-wasm-wasmtime": {
		Notes:          "Container with wasmtime for testing wasip1/wasm.",
		ContainerImage: "wasip1-wasm-wasmtime:latest",
		SSHUsername:    "root",
	},
	"host-linux-amd64-wasip1-wasm-wazero": {
		Notes:          "Container with Wazero for testing wasip1/wasm.",
		ContainerImage: "wasip1-wasm-wazero:latest",
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
		IsEC2:          true,
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
		Owners:      []*gophers.Person{gh("abner-chenc")},
		IsReverse:   true,
		ExpectNum:   5,
		GoBootstrap: "none",
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/lib/go-linux-loong64-bootstrap",
		},
	},
	"host-linux-mips64-rtrk": {
		Notes:     "cavium,rhino_utm8 board hosted at RT-RK.com; quad-core cpu, 8GB of ram and 240GB ssd disks.",
		Owners:    []*gophers.Person{gh("nrakovic")}, // See https://github.com/golang/go/issues/53574#issuecomment-1169891255.
		IsReverse: true,
		ExpectNum: 1,
	},
	"host-linux-mips64le-rtrk": {
		Notes:     "cavium,rhino_utm8 board hosted at RT-RK.com; quad-core cpu, 8GB of ram and 240GB ssd disks.",
		Owners:    []*gophers.Person{gh("nrakovic")}, // See https://github.com/golang/go/issues/53574#issuecomment-1169891255.
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
	"host-linux-ppc64-sid-power10": {
		Notes:           "debian sid; run by Go team on osuosl.org; see x/build/env/linux-ppc64le/osuosl",
		Owners:          []*gophers.Person{gh("pmur")},
		IsReverse:       true,
		env:             []string{"GOPPC64=power10"},
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
		Notes:           "Ubuntu 22.04; run by Go team on osuosl.org; see x/build/env/linux-ppc64le/osuosl",
		Owners:          []*gophers.Person{gh("pmur")},
		IsReverse:       true,
		env:             []string{"GOPPC64=power10"},
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
	},
	"host-netbsd-amd64-9_3": {
		VMImage:     "netbsd-amd64-9-3-202211120320v2",
		Notes:       "NetBSD 9.3; GCE VM is built from script in build/env/netbsd-amd64",
		machineType: "n2", // force Intel; see go.dev/issue/49209
		SSHUsername: "root",
	},
	"host-netbsd-arm-bsiegert": {
		IsReverse: true,
		ExpectNum: 0, // was 1 before migration to LUCI
		Owners:    []*gophers.Person{gh("bsiegert")},
	},
	"host-netbsd-arm64-bsiegert": {
		IsReverse: true,
		ExpectNum: 0, // was 1 before migration to LUCI
		Owners:    []*gophers.Person{gh("bsiegert")},
	},
	"host-openbsd-386-72": {
		VMImage:     "openbsd-386-72",
		machineType: "n2", // force Intel; see go.dev/issue/49209
		Notes:       "OpenBSD 7.2; GCE VM, built from build/env/openbsd-386",
		SSHUsername: "gopher",
	},
	"host-openbsd-amd64-72": {
		VMImage:     "openbsd-amd64-72",
		machineType: "n2", // force Intel; see go.dev/issue/49209
		Notes:       "OpenBSD 7.2; GCE VM, built from build/env/openbsd-amd64",
		SSHUsername: "gopher",
	},
	"host-openbsd-arm-joelsing": {
		IsReverse: true,
		ExpectNum: 1,
		Owners:    []*gophers.Person{gh("4a6f656c")},
	},
	"host-openbsd-arm64-joelsing": {
		IsReverse: true,
		ExpectNum: 1,
		Owners:    []*gophers.Person{gh("4a6f656c")},
	},
	"host-openbsd-mips64-joelsing": {
		IsReverse: true,
		ExpectNum: 1,
		Owners:    []*gophers.Person{gh("4a6f656c")},
	},
	"host-openbsd-ppc64-n2vi": {
		IsReverse:   true,
		ExpectNum:   1,
		Owners:      []*gophers.Person{gh("n2vi")},
		Notes:       "TalosII T2P9D01 (dual Power9 32GB) OpenBSD-current",
		GoBootstrap: "none",
		env: []string{
			"GOROOT_BOOTSTRAP=/home/gopher/go-openbsd-ppc64-bootstrap",
		},
	},
	"host-openbsd-riscv64-joelsing": {
		IsReverse:   true,
		ExpectNum:   1,
		Owners:      []*gophers.Person{gh("4a6f656c")},
		GoBootstrap: "none",
		env: []string{
			"GOROOT_BOOTSTRAP=/home/gopher/go-openbsd-riscv64-bootstrap",
		},
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
	"host-windows-amd64-2016": {
		VMImage:     "windows-amd64-server-2016-v9",
		SSHUsername: "gopher",
	},
	"host-windows-amd64-2016-big": {
		VMImage:     "windows-amd64-server-2016-v9",
		machineType: "e2-standard-16",
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
		ExpectNum: 0, // was 2 before migration to LUCI
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
		if c.ContainerImage != "" && !c.IsEC2 {
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

// CosArch defines the different COS images types used.
type CosArch string

const (
	// TODO(59418,59419): The kernel in COS release 105 likely breaks ASAN and MSAN for both
	// gcc and clang. Pin to 101, the previous release.
	CosArchAMD64 CosArch = "cos-101-lts"      // COS image for AMD64 architecture
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
	// If unset, it is set in func init to GoBootstrap (the global constant).
	//
	// If GoBootstrap is set to "none", it means this buildlet is not given a new bootstrap
	// toolchain for each build, typically because it cannot download from
	// storage.googleapis.com.
	//
	// For bootstrap versions Go 1.21.0 and newer,
	// bootstrap Go builds for Windows (only) with that version must be in the buildlet bucket,
	// usually uploaded by 'genbootstrap -upload windows-*'.
	// However, as of 2024-08-16 all existing coordinator builders for Windows have migrated
	// to LUCI, so nothing needs to be done for Windows after all.
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
	IsEC2 bool // if true, the instance is configured to run on EC2

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
	// nil means off. Even if tryBot returns true, BuildConfig.buildsRepoAtAll must
	// also return true. See the implementation of BuildConfig.BuildsRepoTryBot.
	// The proj is "go", "net", etc. The branch is proj's branch.
	// The goBranch is the same as branch for proj "go", else it's the go branch
	// ("master, "release-branch.go1.12", etc).
	tryBot  func(proj, branch, goBranch string) bool
	tryOnly bool // only used for trybots, and not regular builds

	// CompileOnly indicates that tests should only be compiled but not run.
	// Note that this will not prevent the test binary from running for tests
	// for the main Go repo, so this flag is insufficient for disabling
	// tests in a cross-compiled setting. See #58297.
	//
	// For subrepo tests however, this flag is sufficient to ensure that test
	// binaries will only be built, not executed.
	CompileOnly bool

	// FlakyNet indicates that network tests are flaky on this builder.
	// The builder will try to run the tests anyway, but will ignore some of the
	// failures.
	FlakyNet bool

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
	// storage.
	StopAfterMake bool

	// privateGoProxy for builder has it's own Go proxy instead of
	// proxy.golang.org, pre-set in GOPROXY on the builder.
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
	// distTestAdjust works with dist test names expressed in the Go 1.20 format.
	// See ShouldRunDistTest for details.
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

	TestHostConf *HostConfig // override HostConfig for testing, at least for now

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

func (c *BuildConfig) IsReverse() bool { return c.HostConfig().IsReverse }

func (c *BuildConfig) IsGCE() bool { return !c.HostConfig().IsReverse && !c.HostConfig().IsEC2 }

func (c *BuildConfig) IsContainer() bool { return c.HostConfig().IsContainer() }
func (c *HostConfig) IsContainer() bool  { return c.ContainerImage != "" }

func (c *BuildConfig) IsVM() bool { return c.HostConfig().IsVM() }

// IsVM reports whether the instance running the job is ultimately a VM. Hosts where
// a VM is used only to initiate a container are considered a container, not a VM.
// EC2 instances may be configured to run in containers that are running
// on custom AMIs.
func (c *HostConfig) IsVM() bool {
	if c.IsEC2 {
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
//
// The dist test names are expressed in the Go 1.20 format, even for
// newer Go versions. See go120DistTestNames.
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
	if c.TestHostConf != nil {
		return c.TestHostConf
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
	if x, ok := version.Go1PointX(hc.GoBootstrap); ok && x < 21 {
		return "https://storage.googleapis.com/" + e.BuildletBucket +
			"/gobootstrap-" + hc.HostArch + "-" + hc.GoBootstrap + ".tar.gz"
	}
	if c.GOOS() == "windows" {
		// Can't use Windows downloads from go.dev/dl, they're in .zip
		// format but the buildlet API accepts only the .tar.gz format.
		//
		// Since all this will be replaced by LUCI fairly soon,
		// keep using the bootstrap bucket for Windows for now.
		return "https://storage.googleapis.com/" + e.BuildletBucket +
			"/gobootstrap-" + hc.HostArch + "-" + hc.GoBootstrap + ".tar.gz"
	}
	hostOSArch := strings.TrimSuffix(hc.HostArch, "-7") // Issue 69038.
	return "https://go.dev/dl/" + hc.GoBootstrap + "." + hostOSArch + ".tar.gz"
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

// TargetArch returns the build target architecture.
//
// It starts from the host arch, then applies any environment
// variables that would override either GOOS, GOARCH, or GOARM.
func (c *BuildConfig) TargetArch() string {
	var goos, goarch, goarm string
	hostArch := c.HostConfig().HostArch
	h := strings.Split(hostArch, "-")
	switch len(h) {
	case 3:
		goarm = h[2]
		fallthrough
	case 2:
		goos = h[0]
		goarch = h[1]
	default:
		panic("bad host arch " + hostArch)
	}
	for _, v := range c.Env() {
		if strings.HasPrefix(v, "GOOS=") {
			goos = v[len("GOOS="):]
		}
		if strings.HasPrefix(v, "GOARCH=") {
			goarch = v[len("GOARCH="):]
		}
		if strings.HasPrefix(v, "GOARM=") {
			goarm = v[len("GOARM="):]
		}
	}
	if goarch == "arm" && goarm == "" {
		goarm = "7"
	}
	targetArch := goos + "-" + goarch
	if goarm != "" {
		targetArch += "-" + goarm
	}
	return targetArch
}

// IsCrossCompileOnly indicates that this builder configuration
// cross-compiles for some platform other than the host, and that
// it does not run any tests.
//
// TODO(#58297): Remove this and make c.CompileOnly sufficient.
func (c *BuildConfig) IsCrossCompileOnly() bool {
	return c.TargetArch() != c.HostConfig().HostArch && c.CompileOnly
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
	if c.IsCrossCompileOnly() {
		return "src/make.bash"
	}
	return "src/all.bash"
}

func (c *BuildConfig) IsTryOnly() bool { return c.tryOnly }

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
// run for this build config. The dist test name is expressed in the
// Go 1.20 format, even for newer Go versions. See go120DistTestNames.
//
// The isNormalTry parameter specifies whether this is for a normal
// TryBot build. It's false for SlowBot and post-submit builds.
//
// In general, this returns true. When in normal trybot mode,
// some slow portable tests are only run on the fastest builder.
//
// It's possible for individual builders to adjust this policy for their needs,
// though it is preferable to handle that by adjusting test skips in the tests
// instead of here. That has the advantage of being easier to maintain over time
// since both the test and its skips would be in one repository rather than two,
// and having effect when tests are run locally by developers.
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

	// Individual builders have historically sometimes adjusted the cmd/dist test policy.
	// Over time these can migrate to better ways of doing platform-based or speed-based test skips.
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
			if repo == "pkgsite" && bmm.Less(types.MajorMinor{Major: 1, Minor: 23}) {
				// x/pkgsite started requiring Go 1.23 sooner. See CL 609142.
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
	case "build", "exp", "mobile", "pkgsite-metrics", "vulndb", "website":
		// Builders need to explicitly opt-in to build these repos.
		return false
	default:
		// Build all other golang.org/x repositories by default.
		return true
	}
}

var (
	defaultPlusExp      = defaultPlus("exp")
	defaultPlusExpBuild = defaultPlus("exp", "build")
)

// linux-amd64 and linux-amd64-race build all the repos.
// Many team repos are disabled on other builders because
// we only run them on servers and don't need to test the
// many different architectures that Go supports (like ios).
func linuxAmd64Repos(repo, branch, goBranch string) bool {
	return true
}

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
// releases for the given GOOS.
func GorootFinal(goos string) string {
	if goos == "windows" {
		return "c:\\go"
	}
	return "/usr/local/go"
}

// MachineType returns the AWS or GCE machine type to use for this builder.
func (c *HostConfig) MachineType() string {
	if c.IsEC2 {
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
// PoolName returns a short summary of the builder's host type for the
// https://farmer.golang.org/builders page.
func (c *HostConfig) PoolName() string {
	switch {
	case c.IsReverse:
		return "Reverse (dedicated machine/VM)"
	case c.IsEC2:
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
	if c.IsEC2 && c.ContainerImage != "" {
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
	case c.IsEC2:
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

// miscCompileBuildSet returns a policy function that reports whether a project
// should use trybots based on the platform.
func miscCompileBuildSet(goos, goarch string) func(proj, branch, goBranch string) bool {
	return func(proj, branch, goBranch string) bool {
		if proj != "go" && branch != "master" {
			return false // #58311
		}
		switch proj {
		case "benchmarks":
			// Failure to build because of a dependency not supported on plan9.
			// #58306 for loong64.
			return goos != "plan9" && goarch != "loong64"
		case "build":
			return goarch != "riscv64" // #58307
		case "exp":
			// exp fails to build most cross-compile platforms, partly because of x/mobile dependencies.
			return false
		case "mobile":
			// mobile fails to build on all cross-compile platforms. This is somewhat expected
			// given the nature of the repository. Leave this as a blanket policy for now.
			return false
		case "pkgsite": // #61341
			return goos != "plan9" && goos != "wasip1"
		case "vuln":
			// Failure to build because of a dependency not supported on plan9.
			return goos != "plan9"
		case "vulndb":
			return goos != "aix" // #58308
		case "website":
			// Failure to build because of a dependency not supported on plan9.
			return goos != "plan9"
		}
		return true
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
		env: []string{
			// Give this builder more time. The default timeout appears to be too small for x/tools
			// tests (specifically, I/O seems to be slower on this builder). See #64473.
			"GO_TEST_TIMEOUT_SCALE=2",
		},
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
		buildsRepo: linuxAmd64Repos,
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
	})
	addBuilder(BuildConfig{
		Name:       "linux-amd64-vmx",
		HostType:   "host-linux-amd64-bullseye-vmx",
		buildsRepo: disabledBuilder,
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-alpine",
		HostType: "host-linux-amd64-alpine",
	})

	// addMiscCompileGo1 adds a misc-compile TryBot that
	// runs buildall.bash on the specified target ("$goos-$goarch").
	// If min is non-zero, it specifies the minimum Go 1.x version.
	addMiscCompileGo1 := func(min int, goos, goarch, extraSuffix string, extraEnv ...string) {
		if migration.StopLegacyMiscCompileTryBots {
			return
		}

		var v types.MajorMinor
		var alsoNote string
		if min != 0 {
			v = types.MajorMinor{Major: 1, Minor: min}
			alsoNote = fmt.Sprintf(" Applies to Go 1.%d and newer.", min)
		}
		platform := goos + "-" + goarch + extraSuffix
		addBuilder(BuildConfig{
			Name:             "misc-compile-" + platform,
			HostType:         "host-linux-amd64-bullseye",
			tryBot:           miscCompileBuildSet(goos, goarch),
			env:              append(extraEnv, "GO_DISABLE_OUTBOUND_NETWORK=1", "GOOS="+goos, "GOARCH="+goarch),
			tryOnly:          true,
			MinimumGoVersion: v,
			CompileOnly:      true,
			SkipSnapshot:     true,
			Notes:            "Runs make.bash (or compile-only go test) for " + platform + ", but doesn't run any tests." + alsoNote,
		})
	}
	// addMiscCompile adds a misc-compile TryBot
	// for all supported Go versions.
	addMiscCompile := func(goos, goarch string) { addMiscCompileGo1(0, goos, goarch, "") }

	// To keep things simple, have each misc-compile builder handle exactly one platform.
	//
	// This is potentially wasteful as there could be much more VM creation overhead, but
	// it shouldn't add any latency. It also adds some visual noise. The alternative was
	// more complex support for subrepos; this keeps things simple by following the same
	// general principle as all the other builders.
	//
	// See https://go.dev/issue/58163 for more details.
	addMiscCompile("windows", "arm")
	addMiscCompile("windows", "arm64")
	addMiscCompile("darwin", "amd64")
	addMiscCompile("darwin", "arm64")
	addMiscCompile("linux", "mips")
	addMiscCompile("linux", "mips64")
	addMiscCompile("linux", "mipsle")
	addMiscCompile("linux", "mips64le")
	addMiscCompile("linux", "ppc64")
	addMiscCompile("linux", "ppc64le")
	addMiscCompile("aix", "ppc64")
	addMiscCompile("freebsd", "386")
	addMiscCompile("freebsd", "arm")
	addMiscCompile("freebsd", "arm64")
	addMiscCompile("freebsd", "riscv64")
	addMiscCompile("netbsd", "386")
	addMiscCompile("netbsd", "amd64")
	addMiscCompile("netbsd", "arm")
	addMiscCompile("netbsd", "arm64")
	addMiscCompile("openbsd", "386")
	//addMiscCompile("openbsd", "mips64") is disabled due to go.dev/issue/58110.
	addMiscCompile("openbsd", "arm")
	addMiscCompile("openbsd", "arm64")
	addMiscCompileGo1(22, "openbsd", "ppc64", "-go1.22")
	addMiscCompileGo1(23, "openbsd", "riscv64", "-go1.23")
	addMiscCompile("plan9", "386")
	addMiscCompile("plan9", "amd64")
	addMiscCompile("plan9", "arm")
	addMiscCompile("solaris", "amd64")
	addMiscCompile("illumos", "amd64")
	addMiscCompile("dragonfly", "amd64")
	addMiscCompile("linux", "loong64")
	addMiscCompile("linux", "riscv64")
	addMiscCompile("linux", "s390x")
	addMiscCompile("linux", "arm")
	addMiscCompileGo1(0, "linux", "arm", "-arm5", "GOARM=5")

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
			"GO_GCFLAGS=-d=ssa/check/on",
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
		Name:     "linux-amd64-newinliner",
		HostType: "host-linux-amd64-bullseye",
		Notes:    "builder with GOEXPERIMENT=newinliner, see go.dev/issue/61883",
		tryBot: func(repo, branch, goBranch string) bool {
			return repo == "go" && goBranch == "master"
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && goBranch == "master"
		},
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GOEXPERIMENT=newinliner",
		},
		GoDeps: []string{
			"fbf9076ee8c8f665f1e8bba08fdc473cc7a2d690", // CL 511555, which added GOEXPERIMENT=newinliner
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
		buildsRepo:        linuxAmd64Repos,
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
		Name:     "linux-amd64-bookworm",
		HostType: "host-linux-amd64-bookworm",
		Notes:    "Debian Bookworm.",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
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
			// Test all repos, ignoring buildRepoByDefault.
			// For golang.org/x repos, don't test non-latest versions.
			return repo == "go" || (branch == "master" && goBranch == "master")
		},
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
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "js-wasm-node18",
		HostType: "host-linux-amd64-js-wasm-node18",
		tryBot:   explicitTrySet("go"),
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
		KnownIssues:  []int{36435, 58110, 61546},
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
		Name:         "openbsd-ppc64-n2vi",
		HostType:     "host-openbsd-ppc64-n2vi",
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
	})
	addBuilder(BuildConfig{
		Name:         "openbsd-riscv64-jsing",
		HostType:     "host-openbsd-riscv64-joelsing",
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
			"GO_TEST_TIMEOUT_SCALE=3",
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
		Name:              "windows-386-2016",
		HostType:          "host-windows-amd64-2016",
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 4,
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
		env: []string{
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-race",
		HostType: "host-windows-amd64-2016",
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
		Name:           "darwin-amd64-10_15",
		HostType:       "host-darwin-amd64-10_15-aws",
		distTestAdjust: macTestPolicy,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return defaultPlusExpBuild(repo, branch, goBranch) && atMostGo1(goBranch, 22)
		},
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
		Name:     "darwin-amd64-longtest",
		HostType: "host-darwin-amd64-13-aws",
		Notes:    "macOS 13 with go test -short=false",
		buildsRepo: func(repo, branch, goBranch string) bool {
			b := buildRepoByDefault(repo)
			if repo == "go" && !atLeastGo1(goBranch, 21) {
				// The builder was added during Go 1.21 dev cycle.
				// It uncovered some tests that weren't passing and needed to be fixed.
				// Disable the builder on older release branches unless/until it's decided
				// that we should backport the needed fixes (and that the older releases
				// don't have even more that needs fixing before the builder passes fully).
				b = false
			}
			if repo != "go" && !(branch == "master" && goBranch == "master") {
				// For golang.org/x repos, don't test non-latest versions.
				b = false
			}
			return b
		},
		env: []string{
			// We use a timeout scale value of 5 for most longtest builders
			// to give them lots of time. This particular builder is not as fast
			// as the rest, so we give it 2x headroom for a scale value of 10.
			// See go.dev/issue/60919.
			"GO_TEST_TIMEOUT_SCALE=10",
		},
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
		env: []string{
			// Increase the timeout scale for this builder: it was observed to be
			// timing out frequently in
			// https://go.dev/issue/55311#issuecomment-1571986012.
			//
			// TODO(bcmills): The darwin-amd64-longtest builder was running extremely
			// slowly because it was hitting swap. Race-enabled builds are also
			// memory-hungry — is it possible that the -race builder is also swapping?
			"GO_TEST_TIMEOUT_SCALE=2",
		},
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
			case "pkgsite":
				// The pkgsite tests need CL 472096, released in 1.21 to run properly.
				b = atLeastGo1(goBranch, 21)
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
			case "pkgsite":
				// The pkgsite tests need CL 472096, released in 1.21 to run properly.
				b = atLeastGo1(goBranch, 21)
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
	})
	addBuilder(BuildConfig{
		Name:     "illumos-amd64",
		HostType: "host-illumos-amd64-jclulow",
	})
	addBuilder(BuildConfig{
		Name:     "solaris-amd64-oraclerel",
		HostType: "host-solaris-oracle-amd64-oraclerel",
		Notes:    "Oracle Solaris release version",
		FlakyNet: true,
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64-sid-buildlet",
		HostType:       "host-linux-ppc64-sid",
		FlakyNet:       true,
		distTestAdjust: ppc64DistTestPolicy,
		env:            []string{"GO_TEST_TIMEOUT_SCALE=2"}, // see go.dev/issues/44422
	})
	addBuilder(BuildConfig{
		Name:           "linux-ppc64-sid-power10",
		HostType:       "host-linux-ppc64-sid-power10",
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
	})
	addBuilder(BuildConfig{
		Name:              "linux-arm64",
		HostType:          "host-linux-arm64-bullseye",
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 1,
	})
	addBuilder(BuildConfig{
		Name:     "linux-arm64-race",
		HostType: "host-linux-arm64-bullseye",
	})
	addBuilder(BuildConfig{
		Name:     "linux-arm64-boringcrypto",
		HostType: "host-linux-arm64-bullseye",
		env: []string{
			"GOEXPERIMENT=boringcrypto",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
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
			b := buildRepoByDefault(repo)
			if repo != "go" && !(branch == "master" && goBranch == "master") {
				// For golang.org/x repos, don't test non-latest versions.
				b = false
			}
			return b
		},
		env: []string{
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
		numTryTestHelpers: 4, // Target time is < 15 min for go.dev/issue/42661.
	})
	addBuilder(BuildConfig{
		Name:              "linux-arm-aws",
		HostType:          "host-linux-arm-aws",
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
				return true
			case "arch", "net", "sys":
				return branch == "master"
			default:
				return false
			}
		},
		privateGoProxy: true, // this builder is behind firewall
		env: []string{
			"GOARCH=loong64",
			"GOHOSTARCH=loong64",
		},
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
		env:            []string{"GO_TEST_TIMEOUT_SCALE=5"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-s390x-ibm-race",
		HostType: "host-linux-s390x",
		Notes:    "Only runs -race tests (./race.bash)",
		FlakyNet: true,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && goBranch == "master"
		},
		env: []string{"GO_TEST_TIMEOUT_SCALE=5"},
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
			case "vulndb", "vuln", "pkgsite":
				// vulndb and pkgsite currently use a dependency which does not build cleanly
				// on aix-ppc64. Until that issue is resolved, skip vulndb on this builder.
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
				// thus reducing the number of duplicate
				// runs.
				return strings.HasPrefix(goBranch, "release-branch.")
			}
			return false
		},
		RunBench:     true,
		SkipSnapshot: true,
	})
	addBuilder(BuildConfig{
		Name:       "wasip1-wasm-wazero",
		HostType:   "host-linux-amd64-wasip1-wasm-wazero",
		buildsRepo: wasip1Default,
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			if isNormalTry && (strings.Contains(distTest, "/internal/") || distTest == "reboot") {
				// Skip some tests in an attempt to speed up normal trybots, inherited from CL 121938.
				run = false
			}
			return run
		},
		numTryTestHelpers: 3,
		env: []string{
			"GOOS=wasip1", "GOARCH=wasm", "GOHOSTOS=linux", "GOHOSTARCH=amd64",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/workdir/go/misc/wasm",
			"GO_DISABLE_OUTBOUND_NETWORK=1", "GOWASIRUNTIME=wazero",
		},
	})
	addBuilder(BuildConfig{
		Name:       "wasip1-wasm-wasmtime",
		HostType:   "host-linux-amd64-wasip1-wasm-wasmtime",
		tryBot:     explicitTrySet("go"),
		buildsRepo: wasip1Default,
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			if isNormalTry && (strings.Contains(distTest, "/internal/") || distTest == "reboot") {
				// Skip some tests in an attempt to speed up normal trybots, inherited from CL 121938.
				run = false
			}
			return run
		},
		numTryTestHelpers: 3,
		env: []string{
			"GOOS=wasip1", "GOARCH=wasm", "GOHOSTOS=linux", "GOHOSTARCH=amd64",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/workdir/go/misc/wasm",
			"GO_DISABLE_OUTBOUND_NETWORK=1", "GOWASIRUNTIME=wasmtime",
		},
	})
	addBuilder(BuildConfig{
		Name:        "wasip1-wasm-wasmer",
		HostType:    "host-linux-amd64-wasip1-wasm-wasmer",
		KnownIssues: []int{59907},
		buildsRepo:  wasip1Default,
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			if isNormalTry && (strings.Contains(distTest, "/internal/") || distTest == "reboot") {
				// Skip some tests in an attempt to speed up normal trybots, inherited from CL 121938.
				run = false
			}
			return run
		},
		numTryTestHelpers: 3,
		env: []string{
			"GOOS=wasip1", "GOARCH=wasm", "GOHOSTOS=linux", "GOHOSTARCH=amd64",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/workdir/go/misc/wasm",
			"GO_DISABLE_OUTBOUND_NETWORK=1", "GOWASIRUNTIME=wasmer",
		},
	})
	addBuilder(BuildConfig{
		Name:        "wasip1-wasm-wasmedge",
		HostType:    "host-linux-amd64-wasip1-wasm-wasmedge",
		KnownIssues: []int{60097},
		buildsRepo:  wasip1Default,
		distTestAdjust: func(run bool, distTest string, isNormalTry bool) bool {
			if isNormalTry && (strings.Contains(distTest, "/internal/") || distTest == "reboot") {
				// Skip some tests in an attempt to speed up normal trybots, inherited from CL 121938.
				run = false
			}
			return run
		},
		numTryTestHelpers: 3,
		env: []string{
			"GOOS=wasip1", "GOARCH=wasm", "GOHOSTOS=linux", "GOHOSTARCH=amd64",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/workdir/go/misc/wasm",
			"GO_DISABLE_OUTBOUND_NETWORK=1", "GOWASIRUNTIME=wasmedge",
		},
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

	if migration.BuildersPortedToLUCI[c.Name] && migration.StopPortedBuilder {
		c.buildsRepo = func(_, _, _ string) bool { return false }
		c.Notes = "Unavailable in the coordinator. Use LUCI (https://go.dev/wiki/LUCI) instead."
	}

	Builders[c.Name] = &c
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
	case "benchmarks":
		// Failure to build because of a dependency not supported on plan9.
		return false
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
	case "pkgsite":
		// pkgsite has a dependency (github.com/lib/pq) that is broken on Plan 9.
		return false
	default:
		return onlyMasterDefault(repo, branch, goBranch)
	}
}

// wasip1Default returns whether we should build the repo and branch on wasip1.
func wasip1Default(repo, branch, goBranch string) bool {
	b := buildRepoByDefault(repo) && atLeastGo1(goBranch, 21)
	switch repo {
	case "benchmarks", "debug", "perf", "pkgsite", "talks", "tools", "tour", "website":
		// Don't test these golang.org/x repos.
		b = false
	}
	if repo != "go" && !(branch == "master" && goBranch == "master") {
		// For golang.org/x repos, don't test non-latest versions.
		b = false
	}
	return b
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
