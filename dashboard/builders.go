// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package dashboard contains shared configuration and logic used by various
// pieces of the Go continuous build system.
package dashboard

import (
	"errors"
	"io/ioutil"
	"os"
	"strings"
)

// Builders are the different build configurations.
// The keys are like "darwin-amd64" or "linux-386-387".
// This map should not be modified by other packages.
var Builders = map[string]BuildConfig{}

// A BuildConfig describes how to run either a Docker-based or VM-based builder.
type BuildConfig struct {
	// Name is the unique name of the builder, in the form of
	// "darwin-386" or "linux-amd64-race".
	Name string

	// VM-specific settings:
	VMImage     string // e.g. "openbsd-amd64-56"
	machineType string // optional GCE instance type
	Go14URL     string // URL to built Go 1.4 tar.gz
	buildletURL string // optional override buildlet URL

	// Docker-specific settings: (used if VMImage == "")
	Image   string // Docker image to use to build
	cmd     string // optional -cmd flag (relative to go/src/)
	dashURL string // url of the build dashboard
	tool    string // the tool this configuration is for

	// Use by both VMs and Docker:
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
	return "src/all.bash"
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

func (c *BuildConfig) UsesDocker() bool { return c.VMImage == "" }
func (c *BuildConfig) UsesVM() bool     { return c.VMImage != "" }

// MachineType returns the GCE machine type to use for this builder.
func (c *BuildConfig) MachineType() string {
	if v := c.machineType; v != "" {
		return v
	}
	return "n1-highcpu-2"
}

// DockerRunArgs returns the arguments that go after "docker run" to execute
// this docker image. The rev (git hash) is required. The builderKey is optional.
// TODO(bradfitz): remove the builderKey being passed down, once the coordinator
// does the reporting to the dashboard for docker builds too.
func (conf BuildConfig) DockerRunArgs(rev, builderKey string) ([]string, error) {
	if !conf.UsesDocker() {
		return nil, errors.New("not a docker-based build")
	}
	var args []string
	if builderKey != "" {
		tmpKey := "/tmp/" + conf.Name + ".buildkey"
		if _, err := os.Stat(tmpKey); err != nil {
			if err := ioutil.WriteFile(tmpKey, []byte(builderKey), 0600); err != nil {
				return nil, err
			}
		}
		// Images may look for .gobuildkey in / or /root, so provide both.
		// TODO(adg): fix images that look in the wrong place.
		args = append(args, "-v", tmpKey+":/.gobuildkey")
		args = append(args, "-v", tmpKey+":/root/.gobuildkey")
	}
	for _, pair := range conf.env {
		args = append(args, "-e", pair)
	}
	if strings.HasPrefix(conf.Name, "linux-amd64") {
		args = append(args, "-e", "GOROOT_BOOTSTRAP=/go1.4-amd64/go")
	} else if strings.HasPrefix(conf.Name, "linux-386") {
		args = append(args, "-e", "GOROOT_BOOTSTRAP=/go1.4-386/go")
	}
	args = append(args,
		conf.Image,
		"/usr/local/bin/builder",
		"-rev="+rev,
		"-dashboard="+conf.dashURL,
		"-tool="+conf.tool,
		"-buildroot=/",
		"-v",
	)
	if conf.cmd != "" {
		args = append(args, "-cmd", conf.cmd)
	}
	args = append(args, conf.Name)
	return args, nil
}

func init() {
	// Docker-based gccgo builders:
	addBuilder(BuildConfig{
		Name:    "linux-amd64-gccgo",
		Image:   "gobuilders/linux-x86-gccgo",
		cmd:     "make RUNTESTFLAGS=\"--target_board=unix/-m64\" check-go -j16",
		dashURL: "https://build.golang.org/gccgo",
		tool:    "gccgo",
	})
	addBuilder(BuildConfig{
		Name:    "linux-386-gccgo",
		Image:   "gobuilders/linux-x86-gccgo",
		cmd:     "make RUNTESTFLAGS=\"--target_board=unix/-m32\" check-go -j16",
		dashURL: "https://build.golang.org/gccgo",
		tool:    "gccgo",
	})

	// TODO(bradfitz,adg,jbd): convert these (sid, nacl) to VMs too:
	addBuilder(BuildConfig{Name: "linux-386-sid", Image: "gobuilders/linux-x86-sid"})
	addBuilder(BuildConfig{Name: "linux-amd64-sid", Image: "gobuilders/linux-x86-sid"})
	addBuilder(BuildConfig{Name: "nacl-386"})
	addBuilder(BuildConfig{Name: "nacl-amd64p32"})

	// VMs:
	addBuilder(BuildConfig{
		Name:        "freebsd-amd64-gce93",
		VMImage:     "freebsd-amd64-gce93",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-freebsd-amd64.tar.gz",
	})
	addBuilder(BuildConfig{
		Name:        "freebsd-amd64-gce101",
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
		Name:    "linux-amd64-nocgo",
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
		env:         []string{"GOROOT_BOOTSTRAP=/go1.4", "CC=/usr/bin/clang"},
	})
	addBuilder(BuildConfig{
		Name:    "linux-amd64-clang",
		VMImage: "linux-buildlet-clang",
		env:     []string{"GOROOT_BOOTSTRAP=/go1.4", "CC=/usr/bin/clang"},
	})
	addBuilder(BuildConfig{
		Name:        "openbsd-amd64-gce56",
		VMImage:     "openbsd-amd64-56",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-openbsd-amd64.tar.gz",
	})
	addBuilder(BuildConfig{
		Name:        "openbsd-386-gce56",
		VMImage:     "openbsd-386-56",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-openbsd-386.tar.gz",
	})
	addBuilder(BuildConfig{
		Name:    "plan9-386-gcepartial",
		VMImage: "plan9-386",
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
		// n1-standard-1 has 3.6 GB of memory which is
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
		// ... so we use n1-highcpu-2 (1.80 RAM, still
		// plenty), just so we can get 1 whole core for the
		// single-core Plan 9. It will see 2 virtual cores and
		// only use 1, but we hope that 1 will be more powerful
		// and we'll stop timing out on tests.
		machineType: "n1-highcpu-2",
	})
	addBuilder(BuildConfig{
		Name:        "windows-amd64-gce",
		VMImage:     "windows-buildlet",
		machineType: "n1-highcpu-2",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-windows-amd64.tar.gz",
		env:         []string{"GOARCH=amd64", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:        "windows-amd64-race",
		VMImage:     "windows-buildlet",
		machineType: "n1-highcpu-4",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-windows-amd64.tar.gz",
		env:         []string{"GOARCH=amd64", "GOHOSTARCH=amd64"},
	})
	addBuilder(BuildConfig{
		Name:        "windows-386-gce",
		VMImage:     "windows-buildlet",
		machineType: "n1-highcpu-2",
		buildletURL: "http://storage.googleapis.com/go-builder-data/buildlet.windows-amd64",
		Go14URL:     "https://storage.googleapis.com/go-builder-data/go1.4-windows-386.tar.gz",
		env:         []string{"GOARCH=386", "GOHOSTARCH=386"},
	})
}

func addBuilder(c BuildConfig) {
	if c.tool == "gccgo" {
		// TODO(cmang,bradfitz,adg): fix gccgo
		return
	}
	if c.Name == "" {
		panic("empty name")
	}
	if _, dup := Builders[c.Name]; dup {
		panic("dup name")
	}
	if c.dashURL == "" {
		c.dashURL = "https://build.golang.org"
	}
	if c.tool == "" {
		c.tool = "go"
	}

	if strings.HasPrefix(c.Name, "nacl-") {
		if c.Image == "" {
			c.Image = "gobuilders/linux-x86-nacl"
		}
		if c.cmd == "" {
			c.cmd = "/usr/local/bin/build-command.pl"
		}
	}
	if strings.HasPrefix(c.Name, "linux-") && c.Image == "" && c.VMImage == "" {
		c.Image = "gobuilders/linux-x86-base"
	}
	if c.Image == "" && c.VMImage == "" {
		panic("empty image and vmImage")
	}
	if c.Image != "" && c.VMImage != "" {
		panic("can't specify both image and vmImage")
	}
	Builders[c.Name] = c
}
