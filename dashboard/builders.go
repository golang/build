// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package dashboard contains shared configuration and logic used by various
// pieces of the Go continuous build system.
package dashboard

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/build/buildenv"
)

// Builders are the different build configurations.
// The keys are like "darwin-amd64" or "linux-386-387".
// This map should not be modified by other packages.
var Builders = map[string]BuildConfig{}

// Hosts contains the names and configs of all the types of
// buildlets. They can be VMs, containers, or dedicated machines.
var Hosts = map[string]*HostConfig{
	"host-linux-kubestd": &HostConfig{
		Notes:           "Kubernetes container on GKE.",
		KubeImage:       "linux-x86-std-kube:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-armhf-cross": &HostConfig{
		Notes:           "Kubernetes container on GKE built from env/crosscompile/linux-armhf-jessie",
		KubeImage:       "linux-armhf-jessie:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},

	"host-nacl-kube": &HostConfig{
		Notes:           "Kubernetes container on GKE.",
		KubeImage:       "linux-x86-nacl:latest",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-s390x-cross-kube": &HostConfig{
		Notes:           "Kubernetes container on GKE.",
		KubeImage:       "linux-s390x-stretch:latest",
		buildletURLTmpl: "https://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-clang": &HostConfig{
		Notes:           "GCE VM with clang.",
		VMImage:         "linux-buildlet-clang",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-sid": &HostConfig{
		Notes:           "GCE VM with Debian sid.",
		VMImage:         "linux-buildlet-sid",
		buildletURLTmpl: "http://storage.googleapis.com/$BUCKET/buildlet.linux-amd64",
		env:             []string{"GOROOT_BOOTSTRAP=/go1.4"},
	},
	"host-linux-arm": &HostConfig{
		IsReverse:      true,
		ExpectNum:      50,
		env:            []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
		ReverseAliases: []string{"linux-arm", "linux-arm-arm5"},
	},
	"host-openbsd-amd64-60": &HostConfig{
		VMImage:            "openbsd-amd64-60",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-amd64-60.tar.gz",
		Notes:              "OpenBSD 6.0; GCE VM is built from script in build/env/openbsd-amd64",
	},
	"host-openbsd-386-60": &HostConfig{
		VMImage:            "openbsd-386-60",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.openbsd-386",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-openbsd-386-60.tar.gz",
		Notes:              "OpenBSD 6.0; GCE VM is built from script in build/env/openbsd-386",
	},
	"host-freebsd-93-gce": &HostConfig{
		VMImage:            "freebsd-amd64-gce93",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "https://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
	},
	"host-freebsd-101-gce": &HostConfig{
		VMImage:            "freebsd-amd64-gce101",
		Notes:              "FreeBSD 10.1; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64", // TODO(bradfitz): why was this http instead of https?
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		env:                []string{"CC=clang"},
	},
	"host-freebsd-110": &HostConfig{
		VMImage:            "freebsd-amd64-110",
		Notes:              "FreeBSD 11.0; GCE VM is built from script in build/env/freebsd-amd64",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.freebsd-amd64", // TODO(bradfitz): why was this http instead of https?
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-freebsd-amd64.tar.gz",
		env:                []string{"CC=clang"},
	},
	"host-netbsd-70": &HostConfig{
		VMImage:            "netbsd-amd64-70",
		Notes:              "NetBSD 7.0_2016Q4; GCE VM is built from script in build/env/netbsd-amd64",
		machineType:        "n1-highcpu-2",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.netbsd-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/gobootstrap-netbsd-amd64.tar.gz",
	},
	"host-plan9-386-gce": &HostConfig{
		VMImage:            "plan9-386-v4",
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
		env:         []string{"GO_TEST_TIMEOUT_SCALE=2"},
	},
	"host-windows-gce": &HostConfig{
		VMImage:            "windows-buildlet-v2",
		machineType:        "n1-highcpu-4",
		buildletURLTmpl:    "http://storage.googleapis.com/$BUCKET/buildlet.windows-amd64",
		goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-windows-amd64.tar.gz",
		RegularDisk:        true,
	},
	"host-darwin-10_8": &HostConfig{
		IsReverse: true,
		ExpectNum: 1,
		Notes:     "MacStadium OS X 10.8 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases: []string{"darwin-amd64-10_8"},
	},
	"host-darwin-10_10": &HostConfig{
		IsReverse: true,
		ExpectNum: 2,
		Notes:     "MacStadium OS X 10.10 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases: []string{"darwin-amd64-10_10"},
	},
	"host-darwin-10_11": &HostConfig{
		IsReverse: true,
		ExpectNum: 15,
		Notes:     "MacStadium OS X 10.11 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases: []string{"darwin-amd64-10_11"},
	},
	"host-darwin-10_12": &HostConfig{
		IsReverse: true,
		ExpectNum: 2,
		Notes:     "MacStadium OS X 10.12 VM under VMWare ESXi",
		env: []string{
			"GOROOT_BOOTSTRAP=/Users/gopher/go1.4",
		},
		ReverseAliases: []string{"darwin-amd64-10_12"},
	},
	"host-linux-s390x": &HostConfig{
		Notes:          "run by IBM",
		IsReverse:      true,
		env:            []string{"GOROOT_BOOTSTRAP=/var/buildlet/go-linux-s390x-bootstrap"},
		ReverseAliases: []string{"linux-s390x-ibm"},
	},
	"host-linux-ppc64-osu": &HostConfig{
		Notes:          "Debian jessie; run by Go team on osuosl.org",
		IsReverse:      true,
		ExpectNum:      5,
		env:            []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		ReverseAliases: []string{"linux-ppc64-buildlet"},
	},
	"host-linux-ppc64le-osu": &HostConfig{
		Notes:          "Debian jessie; run by Go team on osuosl.org",
		IsReverse:      true,
		ExpectNum:      5,
		env:            []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		ReverseAliases: []string{"linux-ppc64le-buildlet"},
	},
	"host-linux-arm64-linaro": &HostConfig{
		Notes:          "Ubuntu wily; run by Go team, from linaro",
		IsReverse:      true,
		ExpectNum:      5,
		env:            []string{"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap"},
		ReverseAliases: []string{"linux-arm64-buildlet"},
	},
	"host-solaris-amd64": &HostConfig{
		Notes:          "run by Go team on Joyent, on a SmartOS 'infrastructure container'",
		IsReverse:      true,
		ExpectNum:      5,
		env:            []string{"GOROOT_BOOTSTRAP=/root/go-solaris-amd64-bootstrap"},
		ReverseAliases: []string{"solaris-amd64-smartosbuildlet"},
	},
	"host-linux-mips": &HostConfig{
		Notes:     "Run by Brendan Kirby, imgtec.com",
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap-mips",
			"GOARCH=mips",
			"GOHOSTARCH=mips",
			"GO_TEST_TIMEOUT_SCALE=4",
		},
		ReverseAliases: []string{"linux-mips"},
	},
	"host-linux-mipsle": &HostConfig{
		Notes:     "Run by Brendan Kirby, imgtec.com",
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap-mipsle",
			"GOARCH=mipsle",
			"GOHOSTARCH=mipsle",
		},
		ReverseAliases: []string{"linux-mipsle"},
	},
	"host-linux-mips64": &HostConfig{
		Notes:     "Run by Brendan Kirby, imgtec.com",
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap-mips64",
			"GOARCH=mips64",
			"GOHOSTARCH=mips64",
			"GO_TEST_TIMEOUT_SCALE=4",
		},
		ReverseAliases: []string{"linux-mips64"},
	},
	"host-linux-mips64le": &HostConfig{
		Notes:     "Run by Brendan Kirby, imgtec.com",
		IsReverse: true,
		ExpectNum: 1,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/go-bootstrap-mips64le",
			"GOARCH=mips64le",
			"GOHOSTARCH=mips64le",
		},
		ReverseAliases: []string{"linux-mips64le"},
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
		if c.KubeImage != "" {
			nSet++
		}
		if c.IsReverse {
			nSet++
		}
		if nSet != 1 {
			panic(fmt.Sprintf("exactly one of VMImage, KubeImage, IsReverse must be set for host %q; got %v", key, nSet))
		}
		if c.buildletURLTmpl == "" && (c.VMImage != "" || c.KubeImage != "") {
			panic(fmt.Sprintf("missing buildletURLTmpl for host type %q", key))
		}
	}
}

// A HostConfig describes the available ways to obtain buildlets of
// different types. Some host configs can server multiple
// builders. For example, a host config of "host-linux-kube-std" can
// serve linux-amd64, linux-amd64-race, linux-386, linux-386-387, etc.
type HostConfig struct {
	// HostType is the unique name of this host config. It is also
	// the key in the Hosts map.
	HostType string

	// buildletURLTmpl is the URL "template" ($BUCKET is auto-expanded)
	// for the URL to the buildlet binary.
	// This field is required for GCE and Kubernetes builders. It's not
	// needed for reverse buildlets because in that case, the buildlets
	// are already running and their stage0 should know how to update it
	// it automatically.
	buildletURLTmpl string

	// Exactly 1 of these must be set:
	VMImage   string // e.g. "openbsd-amd64-60"
	KubeImage string // e.g. "linux-buildlet-std:latest" (suffix after "gcr.io/<PROJ>/")
	IsReverse bool   // if true, only use the reverse buildlet pool

	// GCE options, if VMImage != ""
	machineType string // optional GCE instance type
	RegularDisk bool   // if true, use spinning disk instead of SSD

	// ReverseOptions:
	ExpectNum int // expected number of reverse buildlets of this type

	// Optional base env. GOROOT_BOOTSTRAP should go here if the buildlet
	// has Go 1.4+ baked in somewhere.
	env []string

	// These template URLs may contain $BUCKET which is expanded to the
	// relevant Cloud Storage bucket as specified by the build environment.
	goBootstrapURLTmpl string // optional URL to a built Go 1.4+ tar.gz

	Owner string // optional email of owner; "bradfitz@golang.org", empty means golang-dev
	Notes string // notes for humans

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
	// For example, "host-linux-kube-std".
	HostType string

	Notes string // notes for humans

	TryOnly     bool // only used for trybots, and not regular builds
	CompileOnly bool // if true, compile tests, but don't run them
	FlakyNet    bool // network tests are flaky (try anyway, but ignore some failures)

	// StopAfterMake causes the build to stop after the make
	// script completes, returning its result as the result of the
	// whole build. It does not run or compile any of the tests,
	// nor does it write a snapshot of the world to cloud
	// storage. This option is only supported for builders whose
	// BuildConfig.SplitMakeRun returns true.
	StopAfterMake bool

	// numTestHelpers is the number of _additional_ buildlets
	// past the first one to help out with sharded tests.
	// For trybots, the numTryHelpers value is used, unless it's
	// zero, in which case numTestHelpers is used.
	numTestHelpers    int
	numTryTestHelpers int // for trybots. if 0, numTesthelpers is used

	env           []string // extra environment ("key=value") pairs
	allScriptArgs []string
}

func (c *BuildConfig) Env() []string {
	env := []string{"GO_BUILDER_NAME=" + c.Name}
	if c.FlakyNet {
		env = append(env, "GO_BUILDER_FLAKY_NET=1")
	}
	env = append(env, c.hostConf().env...)
	return append(env, c.env...)
}

func (c *BuildConfig) IsReverse() bool { return c.hostConf().IsReverse }

func (c *BuildConfig) IsKube() bool { return c.hostConf().IsKube() }
func (c *HostConfig) IsKube() bool  { return c.KubeImage != "" }

func (c *BuildConfig) IsGCE() bool { return c.hostConf().IsGCE() }
func (c *HostConfig) IsGCE() bool  { return c.VMImage != "" }

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

func (c *BuildConfig) hostConf() *HostConfig {
	if c, ok := Hosts[c.HostType]; ok {
		return c
	}
	panic(fmt.Sprintf("missing buildlet config for buildlet %q", c.Name))
}

// BuildletBinaryURL returns the public URL of this builder's buildlet.
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

// AllScript returns the relative path to the operating system's script to
// do the build and run its standard set of tests.
// Example values are "src/all.bash", "src/all.bat", "src/all.rc".
func (c *BuildConfig) AllScript() string {
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
	if strings.HasPrefix(c.Name, "android-") {
		return "src/androidtest.bash"
	}
	if strings.HasPrefix(c.Name, "darwin-arm") {
		return "src/iostest.bash"
	}
	if strings.HasPrefix(c.Name, "misc-compile") {
		return "src/buildall.bash"
	}
	return "src/all.bash"
}

// SplitMakeRun reports whether the coordinator should first compile
// (using c.MakeScript), then snapshot, then run the tests (ideally
// sharded) using c.RunScript.
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
	// TODO(bradfitz): make androidtest.bash and iotest.bash work
	// too. And buildall.bash should really just be N small
	// Kubernetes jobs instead of a "buildall.bash". Then we can
	// delete this whole method.
	return false
}

func (c *BuildConfig) BuildSubrepos() bool {
	if !c.SplitMakeRun() {
		return false
	}
	// TODO(adg,bradfitz): expand this as required
	switch c.Name {
	case "darwin-amd64-10_8",
		"darwin-amd64-10_10",
		"darwin-amd64-10_11",
		"darwin-386-10_11",
		"freebsd-386-gce101", "freebsd-amd64-gce101",
		"freebsd-386-110", "freebsd-amd64-110",
		"linux-386", "linux-amd64", "linux-amd64-nocgo",
		"openbsd-386-60", "openbsd-amd64-60",
		"plan9-386",
		"windows-386-gce", "windows-amd64-gce":
		return true
	case "darwin-amd64-10_12":
		// Don't build subrepos on Sierra until
		// https://github.com/golang/go/issues/18751#issuecomment-274955794
		// is addressed.
		return false
	default:
		return false
	}
}

// AllScriptArgs returns the set of arguments that should be passed to the
// all.bash-equivalent script. Usually empty.
func (c *BuildConfig) AllScriptArgs() []string {
	if strings.HasPrefix(c.Name, "darwin-arm") {
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

// RunScript returns the relative path to the operating system's script to
// run the test suite.
// Example values are "src/run.bash", "src/run.bat", "src/run.rc".
func (c *BuildConfig) RunScript() string {
	if strings.HasPrefix(c.Name, "windows-") {
		return "src/run.bat"
	}
	if strings.HasPrefix(c.Name, "plan9-") {
		return "src/run.rc"
	}
	return "src/run.bash"
}

// RunScriptArgs returns the set of arguments that should be passed to the
// run.bash-equivalent script.
func (c *BuildConfig) RunScriptArgs() []string {
	return []string{"--no-rebuild"}
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

// PoolName returns a short summary of the builder's host type for the
// http://farmer.golang.org/builders page.
func (c *HostConfig) PoolName() string {
	switch {
	case c.IsReverse:
		return "Reverse (dedicated machine/VM)"
	case c.IsGCE():
		return "GCE VM"
	case c.IsKube():
		return "Kubernetes container"
	}
	return "??"
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

func init() {
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-gce93",
		HostType: "host-freebsd-93-gce",
	})
	addBuilder(BuildConfig{
		Name:              "freebsd-amd64-gce101",
		HostType:          "host-freebsd-101-gce",
		numTestHelpers:    2,
		numTryTestHelpers: 4,
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-110",
		HostType: "host-freebsd-110",
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-amd64-race",
		HostType: "host-freebsd-101-gce", // TODO(bradfitz): switch to FreeBSD 11? test first.
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-386-gce101",
		HostType: "host-freebsd-101-gce",
		env:      []string{"GOARCH=386", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:     "freebsd-386-110",
		HostType: "host-freebsd-110",
		env:      []string{"GOARCH=386", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:              "linux-386",
		HostType:          "host-linux-kubestd",
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		numTestHelpers:    1,
		numTryTestHelpers: 3,
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-387",
		Notes:    "GO386=387",
		HostType: "host-linux-kubestd",
		env:      []string{"GOARCH=386", "GOHOSTARCH=386", "GO386=387"},
	})
	addBuilder(BuildConfig{
		Name:           "linux-amd64",
		HostType:       "host-linux-kubestd",
		numTestHelpers: 3,
	})
	// Add the -vetall builder. The builder name suffix "-vetall" is recognized by cmd/dist/test.go
	// to only run the "go vet std cmd" test and no others.
	addBuilder(BuildConfig{
		Name:           "misc-vet-vetall",
		HostType:       "host-linux-kubestd",
		Notes:          "Runs vet over the standard library.",
		numTestHelpers: 5,
	})

	addMiscCompile := func(suffix, rx string) {
		addBuilder(BuildConfig{
			Name:        "misc-compile" + suffix,
			HostType:    "host-linux-kubestd",
			TryOnly:     true,
			CompileOnly: true,
			Notes:       "Runs buildall.sh to cross-compile std packages for " + rx + ", but doesn't run any tests.",
			allScriptArgs: []string{
				// Filtering pattern to buildall.bash:
				rx,
			},
		})
	}
	addMiscCompile("", "^(linux-arm64|linux-mips64.*|nacl-arm|solaris-amd64|freebsd-arm|darwin-386)$")
	// TODO(bradfitz): add linux-mips* (or just make a "-mips" suffix builder) to add 32-bit
	// mips, once that port is finished.
	addMiscCompile("-ppc", "^(linux-ppc64|linux-ppc64le)$")
	addMiscCompile("-netbsd", "^netbsd-")
	addMiscCompile("-plan9", "^plan9-")

	addBuilder(BuildConfig{
		Name:     "linux-amd64-nocgo",
		HostType: "host-linux-kubestd",
		Notes:    "cgo disabled",
		env: []string{
			"CGO_ENABLED=0",
			// This USER=root was required for Docker-based builds but probably isn't required
			// in the VM anymore, since the buildlet probably already has this in its environment.
			// (It was required because without cgo, it couldn't find the username)
			"USER=root",
		},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-noopt",
		Notes:    "optimizations and inlining disabled",
		HostType: "host-linux-kubestd",
		env:      []string{"GO_GCFLAGS=-N -l"},
	})
	addBuilder(BuildConfig{
		Name:        "linux-amd64-ssacheck",
		HostType:    "host-linux-kubestd",
		CompileOnly: true,
		Notes:       "SSA internal checks enabled",
		env:         []string{"GO_GCFLAGS=-d=ssa/check/on"},
	})
	addBuilder(BuildConfig{
		Name:              "linux-amd64-race",
		HostType:          "host-linux-kubestd",
		numTestHelpers:    2,
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:     "linux-386-clang",
		HostType: "host-linux-clang",
		Notes:    "Debian wheezy + clang 3.5 instead of gcc",
		env:      []string{"CC=/usr/bin/clang", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:     "linux-amd64-clang",
		HostType: "host-linux-clang",
		Notes:    "Debian wheezy + clang 3.5 instead of gcc",
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
		Name:              "linux-arm",
		HostType:          "host-linux-arm",
		FlakyNet:          true,
		numTestHelpers:    2,
		numTryTestHelpers: 7,
	})
	addBuilder(BuildConfig{
		Name:          "linux-arm-nativemake",
		Notes:         "runs make.bash on real ARM hardware, but does not run tests",
		HostType:      "host-linux-arm",
		StopAfterMake: true,
	})
	addBuilder(BuildConfig{
		Name:     "linux-arm-arm5",
		HostType: "host-linux-arm",
		Notes:    "GOARM=5, but running on newer-than GOARM=5 hardware",
		FlakyNet: true,
		env: []string{
			"GOARM=5",
			"GO_TEST_TIMEOUT_SCALE=5", // slow.
		},
	})
	addBuilder(BuildConfig{
		Name:           "nacl-386",
		HostType:       "host-nacl-kube",
		numTestHelpers: 3,
		env:            []string{"GOOS=nacl", "GOARCH=386", "GOHOSTOS=linux", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:           "nacl-amd64p32",
		HostType:       "host-nacl-kube",
		numTestHelpers: 3,
		env:            []string{"GOOS=nacl", "GOARCH=amd64p32", "GOHOSTOS=linux", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-amd64-60",
		HostType:          "host-openbsd-amd64-60",
		numTestHelpers:    2,
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:              "openbsd-386-60",
		HostType:          "host-openbsd-386-60",
		numTestHelpers:    2,
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:     "netbsd-amd64-70",
		HostType: "host-netbsd-70",
	})
	addBuilder(BuildConfig{
		Name:     "netbsd-386-70",
		HostType: "host-netbsd-70",
		env:      []string{"GOARCH=386", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:           "plan9-386",
		HostType:       "host-plan9-386-gce",
		numTestHelpers: 1,
	})
	addBuilder(BuildConfig{
		Name:              "windows-amd64-gce",
		HostType:          "host-windows-gce",
		env:               []string{"GOARCH=amd64", "GOHOSTARCH=amd64"},
		numTestHelpers:    1,
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:     "windows-amd64-race",
		HostType: "host-windows-gce",
		Notes:    "Only runs -race tests (./race.bat)",
		env:      []string{"GOARCH=amd64", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:              "windows-386-gce",
		HostType:          "host-windows-gce",
		env:               []string{"GOARCH=386", "GOHOSTARCH=386"},
		numTestHelpers:    1,
		numTryTestHelpers: 5,
	})
	addBuilder(BuildConfig{
		Name:     "darwin-amd64-10_8",
		HostType: "host-darwin-10_8",
	})
	addBuilder(BuildConfig{
		Name:     "darwin-amd64-10_10",
		HostType: "host-darwin-10_10",
	})
	addBuilder(BuildConfig{
		Name:              "darwin-amd64-10_11",
		HostType:          "host-darwin-10_11",
		numTestHelpers:    2,
		numTryTestHelpers: 3,
	})
	addBuilder(BuildConfig{
		Name:     "darwin-amd64-10_12",
		HostType: "host-darwin-10_12",
	})

	addBuilder(BuildConfig{
		Name:  "android-arm-sdk19",
		Notes: "Android ARM device running android-19 (KitKat 4.4), attatched to Mac Mini",
		env:   []string{"GOOS=android", "GOARCH=arm"},
	})
	addBuilder(BuildConfig{
		Name:  "android-arm64-sdk21",
		Notes: "Android arm64 device using the android-21 toolchain, attatched to Mac Mini",
		env:   []string{"GOOS=android", "GOARCH=arm64"},
	})
	addBuilder(BuildConfig{
		Name:  "android-386-sdk21",
		Notes: "Android 386 device using the android-21 toolchain, attatched to Mac Mini",
		env:   []string{"GOOS=android", "GOARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:  "android-amd64-sdk21",
		Notes: "Android amd64 device using the android-21 toolchain, attatched to Mac Mini",
		env:   []string{"GOOS=android", "GOARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:  "darwin-arm-a5ios",
		Notes: "iPhone 4S (A5 processor), via a Mac Mini; owned by crawshaw",
		env:   []string{"GOARCH=arm", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:  "darwin-arm64-a7ios",
		Notes: "iPad Mini 3 (A7 processor), via a Mac Mini; owned by crawshaw",
		env:   []string{"GOARCH=arm64", "GOHOSTARCH=amd64"},
	})

	addBuilder(BuildConfig{
		Name:  "darwin-arm-a1549ios",
		Notes: "iPhone 6 (model A1549), via a Mac Mini; owned by elias.naur",
		env:   []string{"GOARCH=arm", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:  "darwin-arm64-a1549ios",
		Notes: "iPhone 6 (model A1549), via a Mac Mini; owned by elias.naur",
		env:   []string{"GOARCH=arm64", "GOHOSTARCH=amd64"},
	})

	addBuilder(BuildConfig{
		Name:     "solaris-amd64-smartosbuildlet",
		HostType: "host-solaris-amd64",
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
		Name:     "linux-arm64-buildlet",
		HostType: "host-linux-arm64-linaro",
		FlakyNet: true,
	})
	addBuilder(BuildConfig{
		Name:     "linux-mips",
		HostType: "host-linux-mips",
	})
	addBuilder(BuildConfig{
		Name:     "linux-mipsle",
		HostType: "host-linux-mipsle",
	})
	addBuilder(BuildConfig{
		Name:     "linux-mips64",
		HostType: "host-linux-mips64",
	})
	addBuilder(BuildConfig{
		Name:     "linux-mips64le",
		HostType: "host-linux-mips64le",
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
		TryOnly:     true, // but not in trybot set for now
		env: []string{
			"CGO_ENABLED=1",
			"GOARCH=s390x",
			"GOHOSTARCH=amd64",
			"CC_FOR_TARGET=s390x-linux-gnu-gcc",
		},
	})
}

func (c BuildConfig) isMobile() bool {
	return strings.HasPrefix(c.Name, "android-") || strings.HasPrefix(c.Name, "darwin-arm")
}

func addBuilder(c BuildConfig) {
	if c.Name == "" {
		panic("empty name")
	}
	if c.isMobile() && c.HostType == "" {
		htyp := "host-" + c.Name
		if _, ok := Hosts[htyp]; !ok {
			Hosts[htyp] = &HostConfig{
				HostType:           htyp,
				IsReverse:          true,
				goBootstrapURLTmpl: "https://storage.googleapis.com/$BUCKET/go1.4-darwin-amd64.tar.gz",
				ReverseAliases:     []string{c.Name},
			}
			c.HostType = htyp
		}
	}
	if c.HostType == "" {
		panic(fmt.Sprintf("missing HostType for builder %q", c.Name))
	}
	if _, dup := Builders[c.Name]; dup {
		panic("dup name")
	}
	if _, ok := Hosts[c.HostType]; !ok {
		panic(fmt.Sprintf("undefined HostType %q for builder %q", c.HostType, c.Name))
	}
	Builders[c.Name] = c
}
