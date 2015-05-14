// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package dashboard contains shared configuration and logic used by various
// pieces of the Go continuous build system.
package dashboard

import "strings"

// Builders are the different build configurations.
// The keys are like "darwin-amd64" or "linux-386-387".
// This map should not be modified by other packages.
var Builders = map[string]BuildConfig{}

// A BuildConfig describes how to run a builder.
type BuildConfig struct {
	// Name is the unique name of the builder, in the form of
	// "darwin-386" or "linux-amd64-race".
	Name string

	Notes       string // notes for humans
	Owner       string // e.g. "bradfitz@golang.org", empty means golang-dev
	VMImage     string // e.g. "openbsd-amd64-56"
	machineType string // optional GCE instance type
	Go14URL     string // URL to built Go 1.4 tar.gz
	buildletURL string // optional override buildlet URL

	IsReverse   bool // if true, only use the reverse buildlet pool
	RegularDisk bool // if true, use spinning disk instead of SSD
	TryOnly     bool // only used for trybots, and not regular builds

	env []string // extra environment ("key=value") pairs
}

func (c *BuildConfig) Env() []string { return append([]string(nil), c.env...) }

func (c *BuildConfig) GOOS() string { return c.Name[:strings.Index(c.Name, "-")] }

func (c *BuildConfig) GOARCH() string {
	arch := c.Name[strings.Index(c.Name, "-")+1:]
	i := strings.Index(arch, "-")
	if i == -1 {
		return arch
	}
	return arch[:i]
}

// BuildletBinaryURL returns the public URL of this builder's buildlet.
func (c *BuildConfig) BuildletBinaryURL() string {
	if c.buildletURL != "" {
		return c.buildletURL
	}
	return "http://storage.googleapis.com/go-builder-data/buildlet." + c.GOOS() + "-" + c.GOARCH()
}

// AllScript returns the relative path to the operating system's script to
// do the build and run its standard set of tests.
// Example values are "src/all.bash", "src/all.bat", "src/all.rc".
func (c *BuildConfig) AllScript() string {
	if strings.HasSuffix(c.Name, "-race") {
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
	if strings.HasPrefix(c.Name, "darwin-arm") {
		return "src/iostest.bash"
	}
	if c.Name == "all-compile" {
		return "src/buildall.bash"
	}
	return "src/all.bash"
}

// AllScript returns the set of arguments that should be passed to the
// all.bash-equivalent script. Usually empty.
func (c *BuildConfig) AllScriptArgs() []string {
	if strings.HasPrefix(c.Name, "darwin-arm") {
		return []string{"-restart"}
	}
	return nil
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

// GorootFinal returns the default install location for
// releases for this platform.
func (c *BuildConfig) GorootFinal() string {
	if strings.HasPrefix(c.Name, "windows-") {
		return "c:\\go"
	}
	return "/usr/local/go"
}

// MachineType returns the GCE machine type to use for this builder.
func (c *BuildConfig) MachineType() string {
	if v := c.machineType; v != "" {
		return v
	}
	return "n1-highcpu-2"
}

// ShortOwner returns a short human-readable owner.
func (c BuildConfig) ShortOwner() string {
	if c.Owner == "" {
		return "go-dev"
	}
	return strings.TrimSuffix(c.Owner, "@golang.org")
}

func init() {
	addBuilder(BuildConfig{
		Name:        "freebsd-amd64-gce93",
		VMImage:     "freebsd-amd64-gce93",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-freebsd-amd64.tar.gz",
	})
	addBuilder(BuildConfig{
		Name:        "freebsd-amd64-gce101",
		Notes:       "FreeBSD 10.1; GCE VM is built from script in build/env/freebsd-amd64",
		VMImage:     "freebsd-amd64-gce101",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-freebsd-amd64.tar.gz",
		env:         []string{"CC=clang"},
	})
	addBuilder(BuildConfig{
		Name:        "freebsd-amd64-race",
		VMImage:     "freebsd-amd64-gce101",
		machineType: "n1-highcpu-4",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-freebsd-amd64.tar.gz",
		env:         []string{"CC=clang"},
	})
	addBuilder(BuildConfig{
		Name:        "freebsd-386-gce101",
		VMImage:     "freebsd-amd64-gce101",
		machineType: "n1-highcpu-2",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.freebsd-amd64",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-freebsd-amd64.tar.gz",
		// TODO(bradfitz): setting GOHOSTARCH=386 should work
		// to eliminate some unnecessary work (it works on
		// Linux), but fails on FreeBSD with:
		//   ##### ../misc/cgo/testso
		//   Shared object "libcgosotest.so" not found, required by "main"
		// Maybe this is a clang thing? We'll see when we do linux clang too.
		env: []string{"GOARCH=386", "CC=clang"},
	})
	addBuilder(BuildConfig{
		Name:        "linux-386",
		VMImage:     "linux-buildlet-std",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.linux-amd64",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4", "GOARCH=386", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:        "linux-386-387",
		Notes:       "GO386=387",
		VMImage:     "linux-buildlet-std",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.linux-amd64",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4", "GOARCH=386", "GOHOSTARCH=386", "GO386=387"},
	})
	addBuilder(BuildConfig{
		Name:    "linux-amd64",
		VMImage: "linux-buildlet-std",
		env:     []string{"GOROOT_BOOTSTRAP=/go1.4"},
	})
	addBuilder(BuildConfig{
		Name:        "all-compile",
		TryOnly:     true, // TODO: for speed, restrict this to builds not covered by other trybots
		VMImage:     "linux-buildlet-std",
		machineType: "n1-highcpu-16", // CPU-bound, uses it well.
		Notes:       "Runs buildall.sh to compile stdlib for all GOOS/GOARCH, but doesn't run any tests.",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.linux-amd64",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4"},
	})
	addBuilder(BuildConfig{
		Name:    "linux-amd64-nocgo",
		Notes:   "cgo disabled",
		VMImage: "linux-buildlet-std",
		env: []string{
			"GOROOT_BOOTSTRAP=/go1.4",
			"CGO_ENABLED=0",
			// This USER=root was required for Docker-based builds but probably isn't required
			// in the VM anymore, since the buildlet probably already has this in its environment.
			// (It was required because without cgo, it couldn't find the username)
			"USER=root",
		},
	})
	addBuilder(BuildConfig{
		Name:    "linux-amd64-noopt",
		Notes:   "optimizations and inlining disabled",
		VMImage: "linux-buildlet-std",
		env:     []string{"GOROOT_BOOTSTRAP=/go1.4", "GO_GCFLAGS=-N -l"},
	})
	addBuilder(BuildConfig{
		Name:        "linux-amd64-race",
		VMImage:     "linux-buildlet-std",
		machineType: "n1-highcpu-4",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4"},
	})
	addBuilder(BuildConfig{
		Name:        "linux-386-clang",
		VMImage:     "linux-buildlet-clang",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.linux-amd64",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4", "CC=/usr/bin/clang", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:    "linux-amd64-clang",
		Notes:   "Debian wheezy + clang 3.5 instead of gcc",
		VMImage: "linux-buildlet-clang",
		env:     []string{"GOROOT_BOOTSTRAP=/go1.4", "CC=/usr/bin/clang"},
	})
	addBuilder(BuildConfig{
		Name:        "linux-386-sid",
		VMImage:     "linux-buildlet-sid",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.linux-amd64",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:    "linux-amd64-sid",
		Notes:   "Debian sid (unstable)",
		VMImage: "linux-buildlet-sid",
		env:     []string{"GOROOT_BOOTSTRAP=/go1.4"},
	})
	addBuilder(BuildConfig{
		Name:    "linux-arm-qemu",
		VMImage: "linux-buildlet-arm",
		env:     []string{"GOROOT_BOOTSTRAP=/go1.4", "IN_QEMU=1"},
	})
	addBuilder(BuildConfig{
		Name:      "linux-arm",
		IsReverse: true,
		env:       []string{"GOROOT_BOOTSTRAP=/usr/local/go"},
	})
	addBuilder(BuildConfig{
		Name:      "linux-arm-arm5",
		IsReverse: true,
		env: []string{
			"GOROOT_BOOTSTRAP=/usr/local/go",
			"GOARM=5",
			"GO_TEST_TIMEOUT_SCALE=5", // slow.
		},
	})
	addBuilder(BuildConfig{
		Name:        "nacl-386",
		VMImage:     "linux-buildlet-nacl-v2",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.linux-amd64",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4", "GOOS=nacl", "GOARCH=386", "GOHOSTOS=linux", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:        "nacl-amd64p32",
		VMImage:     "linux-buildlet-nacl-v2",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.linux-amd64",
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4", "GOOS=nacl", "GOARCH=amd64p32", "GOHOSTOS=linux", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:        "openbsd-amd64-gce56",
		Notes:       "OpenBSD 5.6; GCE VM is built from script in build/env/openbsd-amd64",
		VMImage:     "openbsd-amd64-56",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-openbsd-amd64.tar.gz",
	})
	addBuilder(BuildConfig{
		Name:        "openbsd-386-gce56",
		Notes:       "OpenBSD 5.6; GCE VM is built from script in build/env/openbsd-386",
		VMImage:     "openbsd-386-56",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-openbsd-386.tar.gz",
	})
	addBuilder(BuildConfig{
		Name:    "plan9-386-gcepartial",
		Notes:   "Plan 9 from 0intro; GCE VM is built from script in build/env/plan9-386; runs with GOTESTONLY=std (only stdlib tests)",
		VMImage: "plan9-386-v2",
		Go14URL: "https://storage.googleapis.com/go-builder-data/go1.4-plan9-386.tar.gz",
		// It's named "partial" because the buildlet sets
		// GOTESTONLY=std to stop after the "go test std"
		// tests because it's so slow otherwise.
		env: []string{"GOTESTONLY=std"},

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
		//
		// But then with the toolchain conversion to Go and
		// using ramfs, it turns out we need more memory
		// anyway, so use n1-highcpu-4.
		machineType: "n1-highcpu-4",
	})
	addBuilder(BuildConfig{
		Name:        "windows-amd64-gce",
		VMImage:     "windows-buildlet-v2",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-windows-amd64.tar.gz",
		RegularDisk: true,
		env:         []string{"GOARCH=amd64", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:        "windows-amd64-race",
		Notes:       "Only runs -race tests (./race.bat)",
		VMImage:     "windows-buildlet-v2",
		machineType: "n1-highcpu-4",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-windows-amd64.tar.gz",
		RegularDisk: true,
		env:         []string{"GOARCH=amd64", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:        "windows-386-gce",
		VMImage:     "windows-buildlet-v2",
		machineType: "n1-highcpu-2",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.windows-amd64",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-windows-386.tar.gz",
		RegularDisk: true,
		env:         []string{"GOARCH=386", "GOHOSTARCH=386"},
	})
	addBuilder(BuildConfig{
		Name:      "darwin-amd64-10_10",
		Notes:     "Mac Mini running OS X 10.10 (Yosemite)",
		Go14URL:   "https://storage.googleapis.com/go-builder-data/go1.4-darwin-amd64.tar.gz",
		IsReverse: true,
	})
	addBuilder(BuildConfig{
		Name:      "darwin-arm-a5ios",
		Notes:     "iPhone 4S (A5 processor), via a Mac Mini",
		Owner:     "crawshaw@golang.org",
		Go14URL:   "https://storage.googleapis.com/go-builder-data/go1.4-darwin-amd64.tar.gz",
		IsReverse: true,
		env:       []string{"GOARCH=arm", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:      "darwin-arm64-a7ios",
		Notes:     "iPad Mini 3 (A7 processor), via a Mac Mini",
		Owner:     "crawshaw@golang.org",
		Go14URL:   "https://storage.googleapis.com/go-builder-data/go1.4-darwin-amd64.tar.gz",
		IsReverse: true,
		env:       []string{"GOARCH=arm64", "GOHOSTARCH=amd64"},
	})
}

func addBuilder(c BuildConfig) {
	if c.Name == "" {
		panic("empty name")
	}
	if _, dup := Builders[c.Name]; dup {
		panic("dup name")
	}
	if c.VMImage == "" && !c.IsReverse {
		panic("empty VMImage")
	}
	Builders[c.Name] = c
}
