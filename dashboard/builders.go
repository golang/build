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
	"golang.org/x/build/maintner/maintnerd/maintapi/version"
	"golang.org/x/build/types"
)

// Builders are the different build configurations.
// The keys are like "darwin-amd64" or "linux-386-387".
// This map should not be modified by other packages.
// Initialization happens below, via calls to addBuilder.
var Builders = map[string]*BuildConfig{}

// Hosts contains the names and configs of all the types of
// buildlets. They can be VMs, containers, or dedicated machines.
var Hosts = map[string]*HostConfig{
	"host-linux-jessie": &HostConfig{
		Notes:           "Debian Jessie, our standard Linux container image.",
		ContainerImage:  "linux-x86-jessie:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-stretch": &HostConfig{
		Notes:           "Debian Stretch",
		ContainerImage:  "linux-x86-stretch:latest",
		machineType:     "n1-standard-4", // 4 vCPUs, 15 GB mem
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-stretch-morecpu": &HostConfig{
		Notes:           "Debian Stretch, but on n1-highcpu-8",
		ContainerImage:  "linux-x86-stretch:latest",
		machineType:     "n1-highcpu-16", // 16 vCPUs, 14.4 GB mem
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-stretch-vmx": &HostConfig{
		Notes:           "Debian Stretch w/ Nested Virtualization (VMX CPU bit) enabled, for testing",
		ContainerImage:  "linux-x86-stretch:latest",
		NestedVirt:      true,
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
		SSHUsername:     "root",
	},
	"host-linux-armhf-cross": &HostConfig{
		Notes:           "Debian Jessie with armhf cross-compiler, built from env/crosscompile/linux-armhf-jessie",
		ContainerImage:  "linux-armhf-jessie:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-armel-cross": &HostConfig{
		Notes:           "Debian Jessie with armel cross-compiler, from env/crosscompile/linux-armel-stretch",
		ContainerImage:  "linux-armel-stretch:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-amd64-localdev": &HostConfig{
		IsReverse:   true,
		ExpectNum:   0,
		Notes:       "for localhost development of buildlets/gomote/coordinator only",
		SSHUsername: os.Getenv("USER"),
	},
	"host-nacl-kube": &HostConfig{
		Notes:           "Container with Native Client binaries.",
		ContainerImage:  "linux-x86-nacl:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-js-wasm": &HostConfig{
		Notes:           "Container with node.js for testing js/wasm.",
		ContainerImage:  "js-wasm:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-s390x-cross-kube": &HostConfig{
		Notes:           "Container with s390x cross-compiler.",
		ContainerImage:  "linux-s390x-stretch:latest",
		buildletURLTmpl: "https://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-x86-alpine": &HostConfig{
		Notes:           "Alpine container",
		ContainerImage:  "linux-x86-alpine:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64-static",
		env:             []string{"GOROOT_BOOTSTRAP=/usr/lib/go"},
	},
	"host-linux-clang": &HostConfig{
		Notes:           "Container with clang.",
		ContainerImage:  "linux-x86-clang:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-sid": &HostConfig{
		Notes:           "Debian sid, updated occasionally.",
		ContainerImage:  "linux-x86-sid:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-fedora": &HostConfig{
		Notes:           "Fedora 30",
		ContainerImage:  "linux-x86-fedora:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/goboot"},
	},
	"host-linux-arm-scaleway": &HostConfig{
		IsReverse:       true,
		HermeticReverse: true,
		ExpectNum:       50,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		ReverseAliases:  []string{"linux-arm", "linux-arm-arm5"},
		SSHUsername:     "root",
	},
	"host-linux-arm5spacemonkey": &HostConfig{
		IsReverse:      true,
		ExpectNum:      3,
		env:            []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		ReverseAliases: []string{"linux-arm-arm5spacemonkey"},
		OwnerGithub:    "zeebo",
	},
	"host-openbsd-amd64-60": &HostConfig{
		VMImage:            "openbsd-amd64-60",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-amd64-60.tar.gz",
		Notes:              "OpenBSD 6.0; GCE VM is built from script in build/env/openbsd-amd64",
		SSHUsername:        "gopher",
	},
	"host-openbsd-386-60": &HostConfig{
		VMImage:            "openbsd-386-60",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-386-60.tar.gz",
		Notes:              "OpenBSD 6.0; GCE VM is built from script in build/env/openbsd-386",
		SSHUsername:        "gopher",
	},
	"host-openbsd-amd64-62": &HostConfig{
		VMImage:            "openbsd-amd64-62",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-amd64-go1_12.tar.gz",
		Notes:              "OpenBSD 6.2; GCE VM is built from script in build/env/openbsd-amd64",
		SSHUsername:        "gopher",
	},
	"host-openbsd-386-62": &HostConfig{
		VMImage:            "openbsd-386-62-a",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-386-go1_12.tar.gz",
		Notes:              "OpenBSD 6.2; GCE VM is built from script in build/env/openbsd-386",
		SSHUsername:        "gopher",
	},
	"host-openbsd-amd64-64": &HostConfig{
		VMImage:            "openbsd-amd64-64-190129a",
		MinCPUPlatform:     "Intel Skylake", // for better TSC? Maybe? see Issue 29223. builds faster at least.
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-amd64-64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-amd64-go1_12.tar.gz",
		Notes:              "OpenBSD 6.4 with hw.smt=1; GCE VM is built from script in build/env/openbsd-amd64",
		SSHUsername:        "gopher",
	},
	"host-openbsd-386-64": &HostConfig{
		VMImage:            "openbsd-386-64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-386-64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-386-go1_12.tar.gz",
		Notes:              "OpenBSD 6.4; GCE VM is built from script in build/env/openbsd-386",
		SSHUsername:        "gopher",
	},
	"host-freebsd-93-gce": &HostConfig{
		VMImage:            "freebsd-amd64-gce93",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-freebsd-10_3": &HostConfig{
		VMImage:            "freebsd-amd64-103-b",
		Notes:              "FreeBSD 10.3; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64", // TODO(bradfitz): why was this http instead of https?
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		env:                []string{"CC=clang"},
		SSHUsername:        "gopher",
	},
	"host-freebsd-10_4": &HostConfig{
		VMImage:            "freebsd-amd64-104",
		Notes:              "FreeBSD 10.4; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-freebsd-11_1": &HostConfig{
		VMImage:            "freebsd-amd64-111-b",
		Notes:              "FreeBSD 11.1; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64", // TODO(bradfitz): why was this http instead of https?
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		env:                []string{"CC=clang"},
		SSHUsername:        "gopher",
	},
	"host-freebsd-11_2": &HostConfig{
		VMImage:            "freebsd-amd64-112",
		Notes:              "FreeBSD 11.2; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-freebsd-12_0": &HostConfig{
		VMImage:            "freebsd-amd64-120-v1",
		Notes:              "FreeBSD 12.0; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-netbsd-amd64-8_0": &HostConfig{
		VMImage:            "netbsd-amd64-8-0-2018q1",
		Notes:              "NetBSD 8.0RC1; GCE VM is built from script in build/env/netbsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.netbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-netbsd-amd64-2da6b33.tar.gz",
		SSHUsername:        "root",
	},
	// Note: the netbsd-386 host hangs during the ../test phase of all.bash,
	// so we don't use this for now. (See the netbsd-386-8 BuildConfig below.)
	"host-netbsd-386-8_0": &HostConfig{
		VMImage:            "netbsd-i386-8-0-2018q1",
		Notes:              "NetBSD 8.0RC1; GCE VM is built from script in build/env/netbsd-386",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.netbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-netbsd-386-0b3b511.tar.gz",
		SSHUsername:        "root",
	},
	"host-netbsd-arm-bsiegert": &HostConfig{
		IsReverse:   true,
		ExpectNum:   1,
		env:         []string{"GOROOT_BOOTSTRAP=/usr/pkg/go112"},
		OwnerGithub: "bsiegert",
	},
	"host-dragonfly-amd64-tdfbsd": &HostConfig{
		IsReverse:      true,
		ExpectNum:      1,
		env:            []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		ReverseAliases: []string{"dragonfly-amd64"},
		OwnerGithub:    "tdfbsd",
	},
	"host-freebsd-arm-paulzhol": &HostConfig{
		IsReverse:      true,
		ExpectNum:      1,
		Notes:          "Cubiboard2 1Gb RAM dual-core Cortex-A7 (Allwinner A20), FreeBSD 11.1-RELEASE",
		env:            []string{"GOROOT_BOOTSTRAP=/usr/home/paulzhol/go1.4"},
		ReverseAliases: []string{"freebsd-arm-paulzhol"},
		OwnerGithub:    "paulzhol",
	},
	"host-plan9-arm-0intro": &HostConfig{
		IsReverse:   true,
		ExpectNum:   1,
		Notes:       "Raspberry Pi 3 Model B, Plan 9 from Bell Labs",
		OwnerGithub: "0intro",
	},
	"host-plan9-amd64-0intro": &HostConfig{
		IsReverse:   true,
		ExpectNum:   1,
		OwnerGithub: "0intro",
	},
	"host-plan9-386-0intro": &HostConfig{
		IsReverse:   true,
		ExpectNum:   1,
		OwnerGithub: "0intro",
	},
	"host-plan9-386-gce": &HostConfig{
		VMImage:            "plan9-386-v7",
		Notes:              "Plan 9 from 0intro; GCE VM is built from script in build/env/plan9-386",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.plan9-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-plan9-386.tar.gz",

		// We *were* using n1-standard-1 because Plan 9 can only
		// reliably use a single CPU. Using 2 or 4 and we see
		// test failures. See:
		//    https://golang.org/issue/8393
		//    https://golang.org/issue/9491
		// n1-standard-1 has 3.6 GB of memory which WAS (see below)
		// overkill (userspace probably only sees 2GB anyway),
		// but it's the cheapest option. And plenty to keep
		// our ~250 MB of inputs+outputs in its ramfs.
		//
		// But the docs says "For the n1 series of machine
		// types, a virtual CPU is implemented as a single
		// hyperthread on a 2.6GHz Intel Sandy Bridge Xeon or
		// Intel Ivy Bridge Xeon (or newer) processor. This
		// means that the n1-standard-2 machine type will see
		// a whole physical core."
		//
		// ... so we used n1-highcpu-2 (1.80 RAM, still
		// plenty), just so we can get 1 whole core for the
		// single-core Plan 9. It will see 2 virtual cores and
		// only use 1, but we hope that 1 will be more powerful
		// and we'll stop timing out on tests.
		machineType: "n1-highcpu-4",
		env:         []string{"GO_TEST_TIMEOUT_SCALE=3"},
	},
	"host-windows-amd64-2008": &HostConfig{
		VMImage:            "windows-amd64-server-2008r2-v7",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2012": &HostConfig{
		VMImage:            "windows-amd64-server-2012r2-v7",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-amd64-2016": &HostConfig{
		VMImage:            "windows-amd64-server-2016-v7",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		SSHUsername:        "gopher",
	},
	"host-windows-arm-iotcore": &HostConfig{
		IsReverse:   true,
		ExpectNum:   1,
		OwnerGithub: "jordanrh1",
		env:         []string{"GOROOT_BOOTSTRAP=C:\\Data\\Go"},
	},
	"host-darwin-10_8": &HostConfig{
		IsReverse: true,
		ExpectNum: 0,
		Notes:     "MacStadium OS X 10.8 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases:  []string{"darwin-amd64-10_8"},
		SSHUsername:     "gopher",
		HermeticReverse: false, // TODO: make it so, like 10.12
	},
	"host-darwin-10_10": &HostConfig{
		IsReverse: true,
		ExpectNum: 3,
		Notes:     "MacStadium OS X 10.10 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases:  []string{"darwin-amd64-10_10"},
		SSHUsername:     "gopher",
		HermeticReverse: false, // TODO: make it so, like 10.12
	},
	"host-darwin-10_11": &HostConfig{
		IsReverse: true,
		ExpectNum: 7,
		Notes:     "MacStadium OS X 10.11 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases:  []string{"darwin-amd64-10_11"},
		SSHUsername:     "gopher",
		HermeticReverse: false, // TODO: make it so, like 10.12
	},
	"host-darwin-10_12": &HostConfig{
		IsReverse: true,
		ExpectNum: 3,
		Notes:     "MacStadium OS X 10.12 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases:  []string{"darwin-amd64-10_12"},
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & let cmd/makemac recreate
	},
	"host-darwin-10_14": &HostConfig{
		IsReverse: true,
		ExpectNum: 7,
		Notes:     "MacStadium macOS Mojave (10.14) VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/goboot", // Go 1.12.1
		},
		SSHUsername:     "gopher",
		HermeticReverse: true, // we destroy the VM when done & let cmd/makemac recreate
	},
	"host-linux-s390x": &HostConfig{
		Notes:          "run by IBM",
		OwnerGithub:    "mundaym",
		IsReverse:      true,
		env:            []string{"GOROOT_BOOTSTRAP=/var/buildlet/go-linux-s390x-bootstrap"},
		ReverseAliases: []string{"linux-s390x-ibm"},
	},
	"host-linux-ppc64-osu": &HostConfig{
		Notes:           "Debian jessie; run by Go team on osuosl.org",
		IsReverse:       true,
		ExpectNum:       5,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		ReverseAliases:  []string{"linux-ppc64-buildlet"},
		SSHUsername:     "debian",
		HermeticReverse: false, // TODO: use rundockerbuildlet like arm64
	},
	"host-linux-ppc64le-osu": &HostConfig{
		Notes:           "Debian jessie; run by Go team on osuosl.org",
		IsReverse:       true,
		ExpectNum:       5,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		ReverseAliases:  []string{"linux-ppc64le-buildlet"},
		SSHUsername:     "debian",
		HermeticReverse: false, // TODO: use rundockerbuildlet like arm64
	},
	"host-linux-ppc64le-power9-osu": &HostConfig{
		Notes:           "Debian jessie; run by IBM",
		OwnerGithub:     "ceseo",
		IsReverse:       true,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		ReverseAliases:  []string{"linux-ppc64le-power9osu"},
		SSHUsername:     "debian",
		HermeticReverse: false, // TODO: use rundockerbuildlet like arm64
	},
	"host-linux-arm64-packet": &HostConfig{
		Notes:           "On 96 core packet.net host (Xenial) in Docker containers (Jessie); run by Go team. See x/build/env/linux-arm64/packet",
		IsReverse:       true,
		HermeticReverse: true,
		ExpectNum:       20,
		env:             []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		SSHUsername:     "root",
	},
	"host-solaris-amd64": &HostConfig{
		Notes:          "run by Go team on Joyent, on a SmartOS 'infrastructure container'",
		IsReverse:      true,
		ExpectNum:      5,
		env:            []string{"GOROOT_BOOTSTRAP=/root/go-solaris-amd64-bootstrap", "HOME=/root"},
		ReverseAliases: []string{"solaris-amd64-smartosbuildlet"},
	},
	"host-illumos-amd64-joyent": &HostConfig{
		Notes:     "run by Go team on Joyent, on a SmartOS 'infrastructure container'",
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/root/goboot",
			"HOME=/root",
			"PATH=/usr/sbin:/usr/bin:/opt/local/bin", // gcc is in /opt/local/bin
		},
	},
	"host-solaris-oracle-amd64-oraclerel": &HostConfig{
		Notes:       "Oracle Solaris amd64 Release System",
		Owner:       "", // TODO: find current owner
		OwnerGithub: "", // TODO: find current owner
		IsReverse:   true,
		ExpectNum:   1,
		env:         []string{"GOROOT_BOOTSTRAP=/opt/golang/go-solaris-amd64-bootstrap"},
	},
	"host-linux-mipsle-mengzhuo": &HostConfig{
		Notes:       "Loongson 3A Box hosted by Meng Zhuo",
		OwnerGithub: "mengzhuo",
		IsReverse:   true,
		ExpectNum:   1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/lib/golang",
			"GOMIPS64=hardfloat",
		},
	},
	"host-darwin-amd64-zenly-ios": &HostConfig{
		Notes:       "MacBook Pro hosted by Zenly, running the ios reverse buildlet",
		OwnerGithub: "znly",
		IsReverse:   true,
		ExpectNum:   1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/Cellar/1.10.3/libexec",
			"GOHOSTARCH=amd64",
		},
	},
	"host-darwin-arm64-corellium-ios": &HostConfig{
		Notes:       "Virtual iOS devices hosted by Zenly on Corellium",
		OwnerGithub: "znly",
		IsReverse:   true,
		ExpectNum:   3,
		env: []string{
			"GOROOT_BOOTSTRAP=/var/mobile/go-darwin-arm64-bootstrap",
		},
	},
	"host-android-arm64-corellium-android": &HostConfig{
		Notes:       "Virtual Android devices hosted by Zenly on Corellium",
		OwnerGithub: "znly",
		IsReverse:   true,
		ExpectNum:   3,
		env: []string{
			"GOROOT_BOOTSTRAP=/data/data/com.termux/files/home/go-android-arm64-bootstrap",
		},
	},
	"host-aix-ppc64-osuosl": &HostConfig{
		Notes:       "AIX 7.2 VM on OSU; run by Tony Reix",
		OwnerGithub: "trex58",
		IsReverse:   true,
		ExpectNum:   1,
		env:         []string{"GOROOT_BOOTSTRAP=/opt/freeware/lib/golang"},
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
		if c.ContainerImage != "" {
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
// different types. Some host configs can server multiple
// builders. For example, a host config of "host-linux-jessie" can
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

	// Exactly 1 of these must be set:
	VMImage        string // e.g. "openbsd-amd64-60"
	ContainerImage string // e.g. "linux-buildlet-std:latest" (suffix after "gcr.io/<PROJ>/")
	IsReverse      bool   // if true, only use the reverse buildlet pool

	// GCE options, if VMImage != ""
	machineType    string // optional GCE instance type
	RegularDisk    bool   // if true, use spinning disk instead of SSD
	MinCPUPlatform string // optional; https://cloud.google.com/compute/docs/instances/specify-min-cpu-platform

	// ReverseOptions:
	ExpectNum       int  // expected number of reverse buildlets of this type
	HermeticReverse bool // whether reverse buildlet has fresh env per conn

	// Container image options, if ContainerImage != "":
	NestedVirt    bool   // container requires VMX nested virtualization
	KonletVMImage string // optional VM image (containing konlet) to use instead of default

	// Optional base env. GOROOT_BOOTSTRAP should go here if the buildlet
	// has Go 1.4+ baked in somewhere.
	env []string

	// These template URLs may contain $BUCKET which is expanded to the
	// relevant Cloud Storage bucket as specified by the build environment.
	goBootstrapURLTmpl string // optional URL to a built Go 1.4+ tar.gz

	Owner       string // optional email of owner; "bradfitz@golang.org", empty means golang-dev
	OwnerGithub string // optional GitHub username of owner
	Notes       string // notes for humans

	SSHUsername string // username to ssh as, empty means not supported

	// ReverseAliases lists alternate names for this buildlet
	// config, for older clients doing a reverse dial into the
	// coordinator from outside. This prevents us from updating
	// 75+ dedicated machines/VMs atomically, switching them to
	// the new "host-*" names.
	// This is only applicable if IsReverse.
	ReverseAliases []string
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
	// For example, "host-linux-jessie".
	HostType string

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

	// buildsRepo optionally specifies whether this
	// builder does builds (of any type) for the given repo ("go",
	// "net", etc) and its branch ("master", "release-branch.go1.12").
	// If nil, a default policy is used. (see buildsRepoAtAll for details)
	// goBranch is the branch of "go" to build against. If repo == "go",
	// goBranch == branch.
	buildsRepo func(repo, branch, goBranch string) bool

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

	// MaxAtOnce optionally specifies a cap of how many builds of
	// this type can run at once. Zero means unlimited. This is a
	// temporary measure until the build scheduler
	// (golang.org/issue/19178) is done, at which point this field
	// should be deleted.
	MaxAtOnce int

	// SkipSnapshot, if true, means to not fetch a tarball
	// snapshot of the world post-make.bash from the buildlet (and
	// thus to not write it to Google Cloud Storage). This is
	// incompatible with sharded tests, and should only be used
	// for very slow builders or networks, unable to transfer
	// the tarball in under ~5 minutes.
	SkipSnapshot bool

	// RunBench causes the coordinator to run benchmarks on this buildlet type.
	RunBench bool

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

	// InstallRacePackages controls which packages to "go install
	// -race <pkgs>" after running make.bash (or equivalent).  If
	// the builder ends in "-race", the default if non-nil is just
	// "std".
	InstallRacePackages []string

	// GoDeps is a list of of git sha1 commits that must be in the
	// commit to be tested's history. If absent, this builder is
	// not run for that commit.
	GoDeps []string

	// shouldRunDistTest optionally specifies a function to
	// override the BuildConfig.ShouldRunDistTest method's
	// default behavior.
	shouldRunDistTest func(distTest string, isTry bool) bool

	// numTestHelpers is the number of _additional_ buildlets
	// past the first one to help out with sharded tests.
	// For trybots, the numTryHelpers value is used, unless it's
	// zero, in which case numTestHelpers is used.
	numTestHelpers    int
	numTryTestHelpers int // for trybots. if 0, numTesthelpers is used

	env           []string // extra environment ("key=value") pairs
	allScriptArgs []string

	testHostConf *HostConfig // override HostConfig for testing, at least for now
}

// Env returns the environment variables this builder should run with.
func (c *BuildConfig) Env() []string {
	env := []string{"GO_BUILDER_NAME=" + c.Name}
	if c.FlakyNet {
		env = append(env, "GO_BUILDER_FLAKY_NET=1")
	}
	env = append(env, c.hostConf().env...)
	return append(env, c.env...)
}

// ModulesEnv returns the extra module-specific environment variables
// to append to this builder as a function of the repo being built
// ("go", "oauth2", "net", etc).
func (c *BuildConfig) ModulesEnv(repo string) (env []string) {
	if c.IsReverse() && repo != "go" {
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

// ShouldTestPackageInGOPATHMode is used to control whether the package
// with the specified import path should be tested in GOPATH mode.
//
// When running tests for all golang.org/* repositories in GOPATH mode,
// this method is called repeatedly with the full import path of each
// package that is found and is being considered for testing in GOPATH
// mode. It's not used and has no effect on import paths in the main
// "go" repository. It has no effect on tests done in module mode.
//
// When considering making changes here, keep the release policy in mind:
//
// 	https://golang.org/doc/devel/release.html#policy
//
func (*BuildConfig) ShouldTestPackageInGOPATHMode(importPath string) bool {
	if importPath == "golang.org/x/tools/gopls" ||
		strings.HasPrefix(importPath, "golang.org/x/tools/gopls/") {
		// Don't test golang.org/x/tools/gopls/... in GOPATH mode.
		return false
	}
	if importPath == "golang.org/x/net/http2/h2demo" {
		// Don't test golang.org/x/net/http2/h2demo in GOPATH mode.
		//
		// It was never tested before golang.org/issue/34361 because it
		// had a +build h2demo constraint. But that build constraint is
		// being removed, so explicitly skip testing it in GOPATH mode.
		//
		// The package is supported only in module mode now, since
		// it requires third-party dependencies.
		return false
	}
	// Test everything else in GOPATH mode as usual.
	return true
}

func (c *BuildConfig) IsReverse() bool { return c.hostConf().IsReverse }

func (c *BuildConfig) IsContainer() bool { return c.hostConf().IsContainer() }
func (c *HostConfig) IsContainer() bool  { return c.ContainerImage != "" }

func (c *BuildConfig) IsVM() bool { return c.hostConf().IsVM() }
func (c *HostConfig) IsVM() bool  { return c.VMImage != "" }

func (c *BuildConfig) GOOS() string { return c.Name[:strings.Index(c.Name, "-")] }

func (c *BuildConfig) GOARCH() string {
	arch := c.Name[strings.Index(c.Name, "-")+1:]
	i := strings.Index(arch, "-")
	if i == -1 {
		return arch
	}
	return arch[:i]
}

// FilePathJoin is mostly like filepath.Join (without the cleaning) except
// it uses the path separator of c.GOOS instead of the host system's.
func (c *BuildConfig) FilePathJoin(x ...string) string {
	if c.GOOS() == "windows" {
		return strings.Join(x, "\\")
	}
	return strings.Join(x, "/")
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
	for _, env := range [][]string{c.env, c.hostConf().env} {
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

func (c *BuildConfig) hostConf() *HostConfig {
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
	return strings.Replace(c.hostConf().goBootstrapURLTmpl, "$BUCKET", e.BuildletBucket, 1)
}

// BuildletBinaryURL returns the public URL of this builder's buildlet.
func (c *HostConfig) BuildletBinaryURL(e *buildenv.Environment) string {
	tmpl := c.buildletURLTmpl
	return strings.Replace(tmpl, "$BUCKET", e.BuildletBucket, 1)
}

func (c *BuildConfig) IsRace() bool {
	return strings.HasSuffix(c.Name, "-race")
}

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
	if strings.HasPrefix(c.Name, "nacl-") {
		return "src/nacltest.bash"
	}
	if strings.HasPrefix(c.Name, "darwin-arm") && !strings.Contains(c.Name, "corellium") {
		return "src/iostest.bash"
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
		"src/all.rc",
		"src/nacltest.bash":
		// These we've verified to work.
		return true
	}
	// TODO(bradfitz): make iostest.bash work too. And
	// buildall.bash should really just be N small container jobs
	// instead of a "buildall.bash". Then we can delete this whole
	// method.
	return false
}

func (c *BuildConfig) IsTryOnly() bool { return c.tryOnly }

func (c *BuildConfig) NeedsGoProxy() bool { return c.needsGoProxy }

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
// run for this build config. The isTry parameter is whether this is
// for a trybot (pre-submit) run.
//
// In general, this returns true, unless a builder has configured it
// otherwise. Certain portable, slow tests are only run on fast builders in
// trybot mode.
func (c *BuildConfig) ShouldRunDistTest(distTest string, isTry bool) bool {
	if c.shouldRunDistTest != nil {
		return c.shouldRunDistTest(distTest, isTry)
	}
	if distTest == "api" {
		// This test is slow and has the same behavior
		// everywhere, so only run it on our fastest buidler
		// (linux-amd64) when in trybot mode.
		return !isTry || c.Name == "linux-amd64"
	}
	return true
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
	return defaultBuildsRepoPolicy(repo, branch, goBranch)
}

func defaultBuildsRepoPolicy(repo, branch, goBranch string) bool {
	switch repo {
	case "go":
		return true
	case "term":
		// no code yet in repo
		return false
	case "mobile", "exp":
		// mobile and exp are opt-in.
		return false
	}
	return true
}

func defaultPlusExp(repo, branch, goBranch string) bool {
	if repo == "exp" {
		return true
	}
	return defaultBuildsRepoPolicy(repo, branch, goBranch)
}

// AllScriptArgs returns the set of arguments that should be passed to the
// all.bash-equivalent script. Usually empty.
func (c *BuildConfig) AllScriptArgs() []string {
	if strings.HasPrefix(c.Name, "darwin-arm") && !strings.Contains(c.Name, "corellium") {
		return []string{"-restart"}
	}
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
	if strings.HasPrefix(c.Name, "nacl-") {
		return "src/naclmake.bash"
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
	if c.IsContainer() {
		// Set a higher default machine size for containers,
		// so their /workdir tmpfs can be larger. The COS
		// image has no swap, so we want to make sure the
		// /workdir fits completely in memory.
		return "n1-standard-4" // 4 CPUs, 15GB RAM
	}
	return "n1-highcpu-2"
}

// ShortOwner returns a short human-readable owner.
func (c BuildConfig) ShortOwner() string {
	owner := c.hostConf().Owner
	if owner == "" {
		return "go-dev"
	}
	return strings.TrimSuffix(owner, "@golang.org")
}

// OwnerGithub returns the Github handle of the owner.
func (c BuildConfig) OwnerGithub() string {
	return c.hostConf().OwnerGithub
}

// PoolName returns a short summary of the builder's host type for the
// https://farmer.golang.org/builders page.
func (c *HostConfig) PoolName() string {
	switch {
	case c.IsReverse:
		return "Reverse (dedicated machine/VM)"
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
		Name:       "freebsd-amd64-gce93",
		HostType:   "host-freebsd-93-gce",
		buildsRepo: disabledBuilder,
		MaxAtOnce:  2,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-10_3",
		HostType: "host-freebsd-10_3",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return branch == "release-branch.go1.11" || goBranch == "release-branch.go1.12"
		},
		tryBot: func(repo, branch, goBranch string) bool {
			return branch == "release-branch.go1.11" || branch == "release-branch.go1.12"
		},
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-10_4",
		HostType: "host-freebsd-10_4",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return goBranch == "release-branch.go1.11" || goBranch == "release-branch.go1.12"
		},
		tryBot:    nil,
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-11_1",
		HostType: "host-freebsd-11_1",
		tryBot:   nil,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return goBranch == "release-branch.go1.11" || goBranch == "release-branch.go1.12"
		},
		shouldRunDistTest: fasterTrybots,
		numTryTestHelpers: 4,
		MaxAtOnce:         2,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-amd64-11_2",
		HostType:          "host-freebsd-11_2",
		tryBot:            explicitTrySet("sys"),
		shouldRunDistTest: fasterTrybots,
		numTryTestHelpers: 4,
		MaxAtOnce:         2,
	})
	addBuilder(BuildConfig{
		Name:             "freebsd-amd64-12_0",
		HostType:         "host-freebsd-12_0",
		MinimumGoVersion: types.MajorMinor{1, 11},
		tryBot:           defaultTrySet("sys"),

		shouldRunDistTest: fasterTrybots,
		numTryTestHelpers: 4,
		MaxAtOnce:         2,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-386-12_0",
		HostType:          "host-freebsd-12_0",
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		shouldRunDistTest: fasterTrybots,
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "net" && branch == "master" && goBranch == "release-branch.go1.11" {
				return false
			}
			return defaultBuildsRepoPolicy(repo, branch, goBranch)
		},
		numTryTestHelpers: 4,
		MaxAtOnce:         2,
	})
	addBuilder(BuildConfig{
		Name:      "freebsd-amd64-race",
		HostType:  "host-freebsd-11_1",
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-386-10_3",
		HostType: "host-freebsd-10_3",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return goBranch == "release-branch.go1.11" || goBranch == "release-branch.go1.12"
		},
		env:       []string{"GOARCH=386", "GOHOSTARCH=386"},
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-386-10_4",
		HostType: "host-freebsd-10_4",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return goBranch == "release-branch.go1.11" || goBranch == "release-branch.go1.12"
		},
		env:       []string{"GOARCH=386", "GOHOSTARCH=386"},
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-386-11_1",
		HostType:          "host-freebsd-11_1",
		shouldRunDistTest: noTestDir,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return goBranch == "release-branch.go1.11" || goBranch == "release-branch.go1.12"
		},
		env:       []string{"GOARCH=386", "GOHOSTARCH=386"},
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-386-11_2",
		HostType:          "host-freebsd-11_2",
		shouldRunDistTest: noTestDir,
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "net" && branch == "master" && goBranch == "release-branch.go1.11" {
				return false
			}
			return defaultBuildsRepoPolicy(repo, branch, goBranch)
		},
		tryBot:    explicitTrySet("sys"),
		env:       []string{"GOARCH=386", "GOHOSTARCH=386"},
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:              "linux-386",
		HostType:          "host-linux-jessie",
		shouldRunDistTest: fasterTrybots,
		tryBot:            defaultTrySet(),
		env: []string{
			"GOARCH=386",
			"GOHOSTARCH=386",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		numTestHelpers:    1,
		numTryTestHelpers: 3,
	})
	addBuilder(BuildConfig{
		Name:  "linux-386-387",
		Notes: "GO386=387",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" || (repo == "crypto" && branch == "master" && goBranch == "master")
		},
		HostType: "host-linux-jessie",
		env:      []string{"GOARCH=386", "GOHOSTARCH=386", "GO386=387"},
	})
	addBuilder(BuildConfig{
		Name:       "linux-amd64",
		HostType:   "host-linux-stretch",
		tryBot:     defaultTrySet(),
		buildsRepo: defaultPlusExp,
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		MaxAtOnce:         3,
		numTestHelpers:    1,
		numTryTestHelpers: 4,
		RunBench:          true,
	})
	addBuilder(BuildConfig{
		Name:       "linux-amd64-vmx",
		HostType:   "host-linux-stretch-vmx",
		MaxAtOnce:  1,
		buildsRepo: disabledBuilder,
	})

	const testAlpine = false // Issue 22689 (hide all red builders), Issue 19938 (get Alpine passing)
	if testAlpine {
		addBuilder(BuildConfig{
			Name:     "linux-amd64-alpine",
			HostType: "host-linux-x86-alpine",
		})
	}

	// addMiscCompile adds a misc-compile builder that runs
	// buildall.bash on a subset of platforms matching the egrep
	// pattern rx. The pattern is matched against the "go tool
	// dist list" name, but with hyphens instead of forward
	// slashes ("linux-amd64", etc).
	addMiscCompile := func(suffix, rx string) {
		addBuilder(BuildConfig{
			Name:     "misc-compile" + suffix,
			HostType: "host-linux-jessie",
			tryBot:   defaultTrySet(),
			env: []string{
				"GO_DISABLE_OUTBOUND_NETWORK=1",
			},
			tryOnly:     true,
			CompileOnly: true,
			Notes:       "Runs buildall.sh to cross-compile & vet std+cmd packages for " + rx + ", but doesn't run any tests.",
			allScriptArgs: []string{
				// Filtering pattern to buildall.bash:
				rx,
			},
		})
	}
	addMiscCompile("-linuxarm", "^linux-arm")        // 2: arm, arm64
	addMiscCompile("-darwin", "^darwin")             // 4: 386, amd64 + iOS: armb, arm64
	addMiscCompile("-mips", "^linux-mips")           // 4: mips, mipsle, mips64, mips64le
	addMiscCompile("-ppc", "^(linux-ppc64|aix-)")    // 3: linux-ppc64{,le}, aix-ppc64
	addMiscCompile("-solaris", "^(solaris|illumos)") // 2: both amd64
	addMiscCompile("-plan9", "^plan9-")              // 3: amd64, 386, arm
	addMiscCompile("-freebsd", "^freebsd-(386|arm)") // 2: 386, arm (amd64 already trybot)
	addMiscCompile("-netbsd", "^netbsd-")            // 4: amd64, 386, arm, arm64
	addMiscCompile("-openbsd", "^openbsd-")          // 4: amd64, 386, arm, arm64
	// And 3 that don't fit above:
	addMiscCompile("-other", "^(windows-arm|linux-s390x|dragonfly-amd64)$")
	// TODO: Issue 25963, get the misc-compile trybots for
	// subrepos too, so "mobile" can at least be included as a
	// misc-compile for ^android- and ^darwin-arm.

	addBuilder(BuildConfig{
		Name:      "linux-amd64-nocgo",
		HostType:  "host-linux-jessie",
		MaxAtOnce: 1,
		Notes:     "cgo disabled",
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "perf":
				// Requires sqlite, which requires cgo.
				return false
			case "mobile":
				return false
			}
			return true
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
		HostType:   "host-linux-jessie",
		buildsRepo: onlyGo,
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GO_GCFLAGS=-N -l",
		},
		MaxAtOnce: 1,
	})
	addBuilder(BuildConfig{
		Name:        "linux-amd64-ssacheck",
		HostType:    "host-linux-jessie",
		MaxAtOnce:   1,
		buildsRepo:  onlyGo,
		tryBot:      nil, // TODO: add a func to conditionally run this trybot if compiler dirs are touched
		CompileOnly: true,
		Notes:       "SSA internal checks enabled",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
			"GO_GCFLAGS=-d=ssa/check/on,dclstack",
		},
		GoDeps: []string{
			"f65abf6ddc8d1f3d403a9195fd74eaffa022b07f", // adds dclstack
		},
	})
	addBuilder(BuildConfig{
		Name:                "linux-amd64-racecompile",
		HostType:            "host-linux-jessie",
		tryBot:              nil, // TODO: add a func to conditionally run this trybot if compiler dirs are touched
		MaxAtOnce:           1,
		CompileOnly:         true,
		SkipSnapshot:        true,
		StopAfterMake:       true,
		InstallRacePackages: []string{"cmd/compile"},
		Notes:               "race-enabled cmd/compile",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
		GoDeps: []string{
			"22f1b56dab29d397d2bdbdd603d85e60fb678089", // adds cmd/compile -c; Issue 20222
		},
	})
	addBuilder(BuildConfig{
		Name:              "linux-amd64-race",
		HostType:          "host-linux-jessie",
		tryBot:            defaultTrySet(),
		buildsRepo:        defaultPlusExp,
		MaxAtOnce:         1,
		shouldRunDistTest: fasterTrybots,
		numTestHelpers:    1,
		numTryTestHelpers: 5,
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:      "linux-386-clang",
		HostType:  "host-linux-clang",
		MaxAtOnce: 1,
		Notes:     "Debian jessie + clang 3.9 instead of gcc",
		env:       []string{"CC=/usr/bin/clang", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:      "linux-amd64-clang",
		HostType:  "host-linux-clang",
		MaxAtOnce: 1,
		Notes:     "Debian jessie + clang 3.9 instead of gcc",
		env:       []string{"CC=/usr/bin/clang"},
	})
	addBuilder(BuildConfig{
		Name:      "linux-386-sid",
		HostType:  "host-linux-sid",
		Notes:     "Debian sid (unstable)",
		MaxAtOnce: 1,
		env:       []string{"GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:      "linux-amd64-sid",
		HostType:  "host-linux-sid",
		MaxAtOnce: 1,
		Notes:     "Debian sid (unstable)",
	})
	addBuilder(BuildConfig{
		Name:      "linux-amd64-fedora",
		HostType:  "host-linux-fedora",
		MaxAtOnce: 1,
		Notes:     "Fedora",
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
		Name:      "linux-amd64-jessie",
		HostType:  "host-linux-jessie",
		MaxAtOnce: 5,
		Notes:     "Debian Jessie. The normal 'linux-amd64' builder is stretch. We use Jessie for our release builds due to https://golang.org/issue/31293",
		env: []string{
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:      "linux-amd64-longtest",
		HostType:  "host-linux-stretch-morecpu",
		MaxAtOnce: 1,
		Notes:     "Debian Stretch with go test -short=false",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" || (branch == "master" && goBranch == "master")
		},
		needsGoProxy: true, // for cmd/go module tests
		env: []string{
			"GO_TEST_SHORT=0",
			"GO_TEST_TIMEOUT_SCALE=5", // give them lots of time
		},
	})
	addBuilder(BuildConfig{
		Name:              "linux-arm",
		HostType:          "host-linux-arm-scaleway",
		tryBot:            nil, // Issue 22748, Issue 22749
		FlakyNet:          true,
		numTestHelpers:    2,
		numTryTestHelpers: 7,
	})
	addBuilder(BuildConfig{
		Name:          "linux-arm-nativemake",
		Notes:         "runs make.bash on real ARM hardware, but does not run tests",
		HostType:      "host-linux-arm-scaleway",
		StopAfterMake: true,
	})
	addBuilder(BuildConfig{
		Name:     "linux-arm-arm5spacemonkey",
		HostType: "host-linux-arm5spacemonkey",
		env: []string{
			"GOARM=5",
			"GO_TEST_TIMEOUT_SCALE=4", // arm is normally 2; double that.
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			return branch == "master" && goBranch == "master"
		},
		shouldRunDistTest: func(distTest string, isTry bool) bool {
			if strings.Contains(distTest, "vendor/github.com/google/pprof") {
				// Not worth it. And broken.
				return false
			}
			if distTest == "api" {
				// Broken on this build config (Issue
				// 24754), and not worth it on slow
				// builder. It's covered by other
				// builders anyway.
				return false
			}
			if strings.HasPrefix(distTest, "test:") {
				// Slow, and not worth it on slow builder.
				return false
			}
			return true
		},
	})
	addBuilder(BuildConfig{
		Name:     "nacl-386",
		HostType: "host-nacl-kube",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// nacl support is removed in Go 1.14.
			return repo == "go" && !atLeastGo1(goBranch, 14)
		},
		MaxAtOnce:         2,
		numTryTestHelpers: 3,
		env:               []string{"GOOS=nacl", "GOARCH=386", "GOHOSTOS=linux", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:     "nacl-amd64p32",
		HostType: "host-nacl-kube",
		buildsRepo: func(repo, branch, goBranch string) bool {
			// nacl support is removed in Go 1.14.
			return repo == "go" && !atLeastGo1(goBranch, 14)
		},
		tryBot:            explicitTrySet("go"),
		MaxAtOnce:         2,
		numTryTestHelpers: 3,
		env:               []string{"GOOS=nacl", "GOARCH=amd64p32", "GOHOSTOS=linux", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:     "js-wasm",
		HostType: "host-js-wasm",
		tryBot:   explicitTrySet("go"),
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "go":
				return true
			case "mobile", "exp", "benchmarks", "debug", "perf", "talks", "tools", "tour", "website":
				return false
			default:
				return branch == "master" && goBranch == "master"
			}
		},
		shouldRunDistTest: func(distTest string, isTry bool) bool {
			if isTry {
				if strings.HasPrefix(distTest, "test:") {
					return false
				}
				if strings.Contains(distTest, "/internal/") ||
					strings.Contains(distTest, "vendor/golang.org/x/arch") {
					return false
				}
				switch distTest {
				case "cmd/go", "nolibgcc:crypto/x509":
					return false
				}
				return true
			}
			return true
		},
		numTryTestHelpers: 4,
		GoDeps: []string{
			"3dced519cbabc213df369d9112206986e62687fa", // first passing commit
		},
		env: []string{
			"GOOS=js", "GOARCH=wasm", "GOHOSTOS=linux", "GOHOSTARCH=amd64",
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/workdir/go/misc/wasm",
			"GO_DISABLE_OUTBOUND_NETWORK=1",
		},
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-amd64-60",
		HostType:          "host-openbsd-amd64-60",
		shouldRunDistTest: noTestDir,
		buildsRepo:        disabledBuilder,
		MaxAtOnce:         1,
		numTestHelpers:    2,
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-386-60",
		HostType:          "host-openbsd-386-60",
		shouldRunDistTest: noTestDir,
		buildsRepo:        disabledBuilder,
		MaxAtOnce:         1,
		env: []string{
			// cmd/go takes ~192 seconds on openbsd-386
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-386-62",
		HostType:          "host-openbsd-386-62",
		shouldRunDistTest: noTestDir,
		MaxAtOnce:         1,
		env: []string{
			// cmd/go takes ~192 seconds on openbsd-386
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-amd64-62",
		HostType:          "host-openbsd-amd64-62",
		shouldRunDistTest: noTestDir,
		tryBot:            nil,
		numTestHelpers:    0,
		numTryTestHelpers: 5,
		MaxAtOnce:         1,
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-amd64-64",
		HostType:          "host-openbsd-amd64-64",
		MinimumGoVersion:  types.MajorMinor{1, 11},
		shouldRunDistTest: noTestDir,
		tryBot:            defaultTrySet(),
		numTestHelpers:    0,
		numTryTestHelpers: 5,
		MaxAtOnce:         1,
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-386-64",
		HostType:          "host-openbsd-386-64",
		tryBot:            explicitTrySet("sys"),
		shouldRunDistTest: noTestDir,
		MaxAtOnce:         1,
	})
	netBSDDistTestPolicy := func(distTest string, isTry bool) bool {
		// Skip the test directory (slow, and adequately
		// covered by other builders), and skip the "reboot"
		// test, which takes more disk space on /tmp than the
		// NetBSD image had as of 2019-03-13. (Issue 30839)
		if strings.HasPrefix(distTest, "test:") || distTest == "reboot" {
			return false
		}
		return true
	}
	addBuilder(BuildConfig{
		Name:              "netbsd-amd64-8_0",
		HostType:          "host-netbsd-amd64-8_0",
		shouldRunDistTest: netBSDDistTestPolicy,
		MaxAtOnce:         1,
		tryBot:            explicitTrySet("sys"),
	})
	addBuilder(BuildConfig{
		Name:              "netbsd-386-8_0",
		HostType:          "host-netbsd-386-8_0",
		shouldRunDistTest: netBSDDistTestPolicy,
		MaxAtOnce:         1,
		// This builder currently hangs in the runtime tests; Issue 31726.
		buildsRepo: disabledBuilder,
	})
	addBuilder(BuildConfig{
		Name:              "netbsd-arm-bsiegert",
		HostType:          "host-netbsd-arm-bsiegert",
		shouldRunDistTest: netBSDDistTestPolicy,
		MaxAtOnce:         1,
		tryBot:            nil,
		env: []string{
			// The machine is slow.
			"GO_TEST_TIMEOUT_SCALE=10",
		},
	})
	addBuilder(BuildConfig{
		Name:           "plan9-386",
		HostType:       "host-plan9-386-gce",
		MaxAtOnce:      2,
		numTestHelpers: 1,
		tryOnly:        true, // disable it for now; Issue 31261, Issue 29801
		shouldRunDistTest: func(distTestName string, isTry bool) bool {
			switch distTestName {
			case "api",
				"go_test:cmd/go": // takes over 20 minutes without working SMP
				return false
			}
			return true
		},
		buildsRepo: onlyMaster,
	})
	addBuilder(BuildConfig{
		Name:              "windows-amd64-2008",
		HostType:          "host-windows-amd64-2008",
		shouldRunDistTest: noTestDir,
		buildsRepo:        onlyGo,
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
		Name:              "windows-386-2008",
		HostType:          "host-windows-amd64-2008",
		buildsRepo:        defaultPlusExp,
		shouldRunDistTest: fasterTrybots,
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		MaxAtOnce:         2,
		tryBot:            defaultTrySet(),
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:              "windows-amd64-2012",
		HostType:          "host-windows-amd64-2012",
		shouldRunDistTest: noTestDir,
		buildsRepo:        onlyGo,
		env: []string{
			"GOARCH=amd64",
			"GOHOSTARCH=amd64",
			// cmd/go takes ~188 seconds on windows-amd64
			// now, which is over the 180 second default
			// dist test timeout. So, bump this builder
			// up:
			"GO_TEST_TIMEOUT_SCALE=2",
		},
		MaxAtOnce: 2,
	})
	addBuilder(BuildConfig{
		Name:              "windows-amd64-2016",
		HostType:          "host-windows-amd64-2016",
		buildsRepo:        defaultPlusExp,
		shouldRunDistTest: fasterTrybots,
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
		Name:     "windows-amd64-race",
		HostType: "host-windows-amd64-2008",
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
		Name:         "windows-arm",
		HostType:     "host-windows-arm-iotcore",
		SkipSnapshot: true,
		env: []string{
			"GOARM=7",
			"GO_TEST_TIMEOUT_SCALE=2",
		},
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-10_8",
		HostType:          "host-darwin-10_8",
		shouldRunDistTest: macTestPolicy,
		buildsRepo:        disabledBuilder,
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-10_10",
		HostType:          "host-darwin-10_10",
		shouldRunDistTest: macTestPolicy,
		buildsRepo: func(repo, branch, goBranch string) bool {
			// https://tip.golang.org/doc/go1.12 says:
			// "Go 1.12 is the last release that will run on macOS 10.10 Yosemite."
			major, minor, ok := version.ParseReleaseBranch(branch)
			return repo == "go" && ok && major == 1 && minor <= 12
		},
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-10_11",
		HostType:          "host-darwin-10_11",
		tryBot:            nil, // disabled until Macs fixed; https://golang.org/issue/23859
		buildsRepo:        onlyGo,
		shouldRunDistTest: macTestPolicy,
		numTryTestHelpers: 3,
	})
	addBuilder(BuildConfig{
		Name:              "darwin-386-10_14",
		HostType:          "host-darwin-10_14",
		shouldRunDistTest: macTestPolicy,
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && atLeastGo1(branch, 13)
		},
		MaxAtOnce: 1,
		env:       []string{"GOARCH=386", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-10_12",
		HostType:          "host-darwin-10_12",
		shouldRunDistTest: macTestPolicy,
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-10_14",
		HostType:          "host-darwin-10_14",
		shouldRunDistTest: macTestPolicy,
		buildsRepo:        defaultPlusExp,
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-nocgo",
		HostType:          "host-darwin-10_14",
		MaxAtOnce:         1,
		shouldRunDistTest: noTestDir,
		env:               []string{"CGO_ENABLED=0"},
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-race",
		HostType:          "host-darwin-10_12",
		shouldRunDistTest: macTestPolicy,
		buildsRepo:        onlyGo,
	})
	addBuilder(BuildConfig{
		Name:     "darwin-arm-mg912baios",
		HostType: "host-darwin-amd64-zenly-ios",
		Notes:    "iPhone 5C (model MG912B/A), via a MacBook Pro; owned by zenly",
		env: []string{
			"GOARCH=arm",
			"GOIOS_DEVICE_ID=8e5c23a5d0843d1ffe164ea0b2f2500599c3ebff",
		},
	})
	addBuilder(BuildConfig{
		Name:     "darwin-arm64-mn4m2zdaios",
		HostType: "host-darwin-amd64-zenly-ios",
		Notes:    "iPhone 7+ (model MN4M2ZD/A), via a MacBook Pro; owned by zenly",
		env: []string{
			"GOARCH=arm64",
			"GOIOS_DEVICE_ID=5ec20fafe317e1c8ff51efc6d508cf19808474a2",
		},
	})
	addBuilder(BuildConfig{
		Name:     "darwin-arm64-corellium",
		HostType: "host-darwin-arm64-corellium-ios",
		Notes:    "Virtual iPhone SE running on Corellium; owned by zenly",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && branch == "master" && goBranch == "master"
		},
	})
	addBuilder(BuildConfig{
		Name:     "android-arm64-corellium",
		HostType: "host-android-arm64-corellium-android",
		Notes:    "Virtual Android running on Corellium; owned by zenly",
		buildsRepo: func(repo, branch, goBranch string) bool {
			return repo == "go" && branch == "master" && goBranch == "master"
		},
	})
	addBuilder(BuildConfig{
		Name:     "android-arm-corellium",
		HostType: "host-android-arm64-corellium-android",
		Notes:    "Virtual Android running on Corellium; owned by zenly",
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
		Notes:    "Android emulator on GCE",
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "blog", "talks", "review", "tour", "website":
				return false
			}
			return atLeastGo1(branch, 13) && atLeastGo1(goBranch, 13)
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
		HostType:          "host-android-amd64-emu",
		Notes:             "Android emulator on GCE",
		numTryTestHelpers: 3,
		tryBot: func(repo, branch, goBranch string) bool {
			switch repo {
			case "go", "mobile", "sys", "net", "tools", "crypto", "sync", "text", "time":
				return atLeastGo1(branch, 13) && atLeastGo1(goBranch, 13)
			}
			return false
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "blog", "talks", "review", "tour", "website":
				return false
			}
			return atLeastGo1(branch, 13) && atLeastGo1(goBranch, 13)
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
		Name:     "solaris-amd64-oraclerel",
		HostType: "host-solaris-oracle-amd64-oraclerel",
		Notes:    "Oracle Solaris release version",
	})
	addBuilder(BuildConfig{
		Name:     "solaris-amd64-smartosbuildlet",
		HostType: "host-solaris-amd64",
	})
	addBuilder(BuildConfig{
		Name:             "illumos-amd64-joyent",
		HostType:         "host-illumos-amd64-joyent",
		MinimumGoVersion: types.MajorMinor{1, 13},
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "review" {
				// '.git/hooks/pre-commit' cannot be executed on this builder,
				// which causes the x/review tests to fail.
				// (https://golang.org/issue/32836)
				return false
			}
			return defaultBuildsRepoPolicy(repo, branch, goBranch)
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-ppc64-buildlet",
		HostType: "host-linux-ppc64-osu",
		FlakyNet: true,
	})
	addBuilder(BuildConfig{
		Name:     "linux-ppc64le-buildlet",
		HostType: "host-linux-ppc64le-osu",
		FlakyNet: true,
	})
	addBuilder(BuildConfig{
		Name:     "linux-ppc64le-power9osu",
		HostType: "host-linux-ppc64le-power9-osu",
		FlakyNet: true,
	})
	addBuilder(BuildConfig{
		Name:     "linux-arm64-packet",
		HostType: "host-linux-arm64-packet",
		FlakyNet: true, // maybe not flaky, but here conservatively
	})
	addBuilder(BuildConfig{
		FlakyNet:     true,
		HostType:     "host-linux-mipsle-mengzhuo",
		Name:         "linux-mips64le-mengzhuo",
		SkipSnapshot: true,
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
		Name:           "linux-s390x-ibm",
		HostType:       "host-linux-s390x",
		numTestHelpers: 0,
	})
	addBuilder(BuildConfig{
		Name:        "linux-s390x-crosscompile",
		HostType:    "host-s390x-cross-kube",
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
		Name:              "dragonfly-amd64",
		HostType:          "host-dragonfly-amd64-tdfbsd",
		shouldRunDistTest: noTestDir,
		SkipSnapshot:      true,
		buildsRepo: func(repo, branch, goBranch string) bool {
			if repo == "review" {
				// '.git/hooks/pre-commit' cannot be executed on this builder,
				// which causes the x/review tests to fail.
				// (https://golang.org/issue/32836)
				return false
			}
			return defaultBuildsRepoPolicy(repo, branch, goBranch)
		},
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-arm-paulzhol",
		HostType:          "host-freebsd-arm-paulzhol",
		shouldRunDistTest: noTestDir,
		SkipSnapshot:      true,
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
	})
	addBuilder(BuildConfig{
		Name:              "plan9-arm",
		HostType:          "host-plan9-arm-0intro",
		SkipSnapshot:      true,
		shouldRunDistTest: noTestDir,
		buildsRepo:        onlyMaster,
	})
	addBuilder(BuildConfig{
		Name:         "plan9-amd64-9front",
		HostType:     "host-plan9-amd64-0intro",
		SkipSnapshot: true,
		shouldRunDistTest: func(distTestName string, isTry bool) bool {
			if !noTestDir(distTestName, isTry) {
				return false
			}
			switch distTestName {
			case "api",
				"go_test:cmd/go": // takes over 20 minutes without working SMP
				return false
			}
			return true
		},
		buildsRepo: onlyMaster,
	})
	addBuilder(BuildConfig{
		Name:         "plan9-386-0intro",
		HostType:     "host-plan9-386-0intro",
		SkipSnapshot: true,
		shouldRunDistTest: func(distTestName string, isTry bool) bool {
			if !noTestDir(distTestName, isTry) {
				return false
			}
			switch distTestName {
			case "api",
				"go_test:cmd/go": // takes over 20 minutes without working SMP
				return false
			}
			return true
		},
		buildsRepo: onlyMaster,
	})
	addBuilder(BuildConfig{
		Name:             "aix-ppc64",
		HostType:         "host-aix-ppc64-osuosl",
		MinimumGoVersion: types.MajorMinor{1, 12},
		env: []string{
			"PATH=/opt/freeware/bin:/usr/bin:/etc:/usr/sbin:/usr/ucb:/usr/bin/X11:/sbin:/usr/java7_64/jre/bin:/usr/java7_64/bin",
		},
		buildsRepo: func(repo, branch, goBranch string) bool {
			switch repo {
			case "net":
				// The x/net package wasn't working in Go 1.12; AIX folk plan to have
				// it ready by Go 1.13. See https://golang.org/issue/31564#issuecomment-484786144
				return atLeastGo1(branch, 13) && atLeastGo1(goBranch, 13)
			case "review", "tools", "tour", "website":
				// The PATH on this builder is misconfigured in a way that causes
				// any test that executes a 'go' command as a subprocess to fail.
				// (https://golang.org/issue/31567).
				// Skip affected repos until the builder is fixed.
				return false
			}
			return atLeastGo1(branch, 12) && atLeastGo1(goBranch, 12) && defaultBuildsRepoPolicy(repo, branch, goBranch)
		},
	})

}

// addBuilder adds c to the Builders map after doing some sanity
// checks.
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

// fasterTrybots is a shouldRunDistTest policy function.
// It skips (returns false) the test/ directory tests for trybots.
func fasterTrybots(distTest string, isTry bool) bool {
	if isTry {
		if strings.HasPrefix(distTest, "test:") ||
			distTest == "reboot" {
			return false // skip test
		}
	}
	return true
}

// noTestDir is a shouldRunDistTest policy function.
// It skips (returns false) the test/ directory tests for all builds,
// as well as the "reboot" test that tests that recompiling Go with
// the just-built Go works.
func noTestDir(distTest string, isTry bool) bool {
	if strings.HasPrefix(distTest, "test:") || distTest == "reboot" {
		return false // skip test
	}
	return true
}

// TryBuildersForProject returns the builders that should run as part of
// a TryBot set for the given project.
// The project argument is of the form "go", "net", "sys", etc.
// The branch is the branch of that project ("master", "release-branch.go1.12", etc)
// The goBranch is the branch of Go to use. If proj == "go", then branch == goBranch.
func TryBuildersForProject(proj, branch, goBranch string) []*BuildConfig {
	var confs []*BuildConfig
	for _, conf := range Builders {
		if conf.BuildsRepoTryBot(proj, branch, goBranch) {
			confs = append(confs, conf)
		}
	}
	sort.Slice(confs, func(i, j int) bool {
		return confs[i].Name < confs[j].Name
	})
	return confs
}

// atLeastGo1 reports whether branch is either "master" or "release-branch.go1.N" where N >= min.
func atLeastGo1(branch string, min int) bool {
	if branch == "master" {
		return true
	}
	major, minor, ok := version.ParseReleaseBranch(branch)
	return ok && major == 1 && minor >= min
}

// onlyGo is a common buildsRepo policy value that only builds the main "go" repo.
func onlyGo(repo, branch, goBranch string) bool { return repo == "go" }

// onlyMaster is a common buildsRepo policy value that only builds things on the master branch.
func onlyMaster(repo, branch, goBranch string) bool { return branch == "master" && goBranch == "master" }

// disabledBuilder is a buildsRepo policy function that always return false.
func disabledBuilder(repo, branch, goBranch string) bool { return false }

// macTestPolicy is the test policy for Macs.
//
// We have limited Mac resources. It's not worth wasting time testing
// portable things on them. That is, if there's a slow test that will
// still fail slowly on another builder where we have more resources
// (like linux-amd64), then there's no point testing it redundantly on
// the Macs.
func macTestPolicy(distTest string, isTry bool) bool {
	if strings.HasPrefix(distTest, "test:") {
		return false
	}
	switch distTest {
	case "reboot", "api", "doc_progs",
		"wiki", "bench_go1", "codewalk":
		return false
	}
	if isTry {
		switch distTest {
		case "runtime:cpu124", "race", "moved_goroot":
			return false
		}
		// TODO: more. Look at bigquery results once we have more data.
	}
	return true
}
