// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dashboard

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
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

func TestDistTestsExecTimeout(t *testing.T) {
	tests := []struct {
		c    *BuildConfig
		want time.Duration
	}{
		{
			&BuildConfig{
				env:          []string{},
				testHostConf: &HostConfig{},
			},
			20 * time.Minute,
		},
		{
			&BuildConfig{
				env:          []string{"GO_TEST_TIMEOUT_SCALE=2"},
				testHostConf: &HostConfig{},
			},
			40 * time.Minute,
		},
		{
			&BuildConfig{
				env: []string{},
				testHostConf: &HostConfig{
					env: []string{"GO_TEST_TIMEOUT_SCALE=3"},
				},
			},
			60 * time.Minute,
		},
		// BuildConfig's env takes precedence:
		{
			&BuildConfig{
				env: []string{"GO_TEST_TIMEOUT_SCALE=2"},
				testHostConf: &HostConfig{
					env: []string{"GO_TEST_TIMEOUT_SCALE=3"},
				},
			},
			40 * time.Minute,
		},
	}
	for i, tt := range tests {
		got := tt.c.DistTestsExecTimeout(nil)
		if got != tt.want {
			t.Errorf("%d. got %v; want %v", i, got, tt.want)
		}
	}
}

// TestTrybots tests that a given repo & its branch yields the provided
// complete set of builders. See also: TestBuilders, which tests both trybots
// and post-submit builders, both at arbitrary branches.
func TestTrybots(t *testing.T) {
	tests := []struct {
		repo   string // "go", "net", etc
		branch string // of repo
		want   []string
	}{
		{
			repo:   "go",
			branch: "master",
			want: []string{
				"android-amd64-emu",
				"freebsd-amd64-12_0",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"misc-compile-other",
				"misc-compile-darwin",
				"misc-compile-linuxarm",
				"misc-compile-solaris",
				"misc-compile-freebsd",
				"misc-compile-mips",
				"misc-compile-netbsd",
				"misc-compile-openbsd",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"openbsd-amd64-64",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
		{
			repo:   "go",
			branch: "dev.link",
			want: []string{
				"android-amd64-emu",
				"freebsd-amd64-12_0",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"misc-compile-other",
				"misc-compile-darwin",
				"misc-compile-linuxarm",
				"misc-compile-solaris",
				"misc-compile-freebsd",
				"misc-compile-mips",
				"misc-compile-netbsd",
				"misc-compile-openbsd",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"openbsd-amd64-64",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
		{
			repo:   "go",
			branch: "release-branch.go1.14",
			want: []string{
				"android-amd64-emu",
				"freebsd-amd64-12_0",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"misc-compile-darwin",
				"misc-compile-freebsd",
				"misc-compile-linuxarm",
				"misc-compile-mips",
				"misc-compile-netbsd",
				"misc-compile-openbsd",
				"misc-compile-other",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"misc-compile-solaris",
				"openbsd-amd64-64",
				"windows-386-2008",
				"windows-amd64-2016",

				// Include longtest builders on Go repo release branches. See issue 37827.
				"linux-386-longtest",
				"linux-amd64-longtest",
				"windows-amd64-longtest",
			},
		},
		{
			repo:   "mobile",
			branch: "master",
			want: []string{
				"android-amd64-emu",
				"linux-amd64-androidemu",
			},
		},
		{
			repo:   "sys",
			branch: "master",
			want: []string{
				"android-amd64-emu",
				"freebsd-386-11_2",
				"freebsd-amd64-11_2",
				"freebsd-amd64-12_0",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"netbsd-amd64-9_0",
				"openbsd-386-64",
				"openbsd-amd64-64",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
		{
			repo:   "exp",
			branch: "master",
			want: []string{
				"linux-amd64",
				"linux-amd64-race",
				"windows-386-2008",
				"windows-amd64-2016",
			},
		},
	}
	for i, tt := range tests {
		if tt.branch == "" || tt.repo == "" {
			t.Errorf("incomplete test entry %d", i)
			return
		}
		t.Run(fmt.Sprintf("%s/%s", tt.repo, tt.branch), func(t *testing.T) {
			var got []string
			goBranch := tt.branch // hard-code the common case for now
			for _, bc := range TryBuildersForProject(tt.repo, tt.branch, goBranch) {
				got = append(got, bc.Name)
			}
			m := map[string]bool{}
			for _, b := range tt.want {
				m[b] = true
			}
			for _, b := range got {
				if _, ok := m[b]; !ok {
					t.Errorf("got unexpected %q", b)
				}
				delete(m, b)
			}
			for b := range m {
				t.Errorf("missing expected %q", b)
			}
		})
	}
}

// TestBuilderConfig whether a given builder and repo at different
// branches is either a post-submit builder, trybot, neither, or both.
func TestBuilderConfig(t *testing.T) {
	// builderConfigWant is bitmask of 4 different things to assert are wanted:
	// - being a post-submit builder
	// - NOT being a post-submit builder
	// - being a trybot builder
	// - NOT being a post-submit builder
	type want uint8
	const (
		isTrybot want = 1 << iota
		notTrybot
		isBuilder  // post-submit
		notBuilder // not post-submit

		none     = notTrybot + notBuilder
		both     = isTrybot + isBuilder
		onlyPost = notTrybot + isBuilder
	)

	type builderAndRepo struct {
		testName string
		builder  string
		repo     string
		branch   string
		goBranch string
	}
	// builder may end in "@go1.N" or "@1.N" (as alias for "@release-branch.go1.N") or "@branch-name".
	// repo (other than "go") may end in "@go1.N" or "@1.N" (as alias for "@release-branch.go1.N").
	b := func(builder, repo string) builderAndRepo {
		br := builderAndRepo{
			testName: builder + "," + repo,
			builder:  builder,
			goBranch: "master",
			repo:     repo,
			branch:   "master",
		}
		if strings.Contains(builder, "@") {
			f := strings.SplitN(builder, "@", 2)
			br.builder = f[0]
			br.goBranch = f[1]
		}
		if strings.Contains(repo, "@") {
			f := strings.SplitN(repo, "@", 2)
			br.repo = f[0]
			br.branch = f[1]
			if br.repo == "go" {
				panic(fmt.Errorf(`b(%q, %q): for "go" repo, must use the @%s suffix on the builder, not on the repo`, builder, repo, br.branch))
			}
		}
		expandBranch := func(s *string) {
			if strings.HasPrefix(*s, "go1.") {
				*s = "release-branch." + *s
			} else if strings.HasPrefix(*s, "1.") {
				*s = "release-branch.go" + *s
			}
		}
		expandBranch(&br.branch)
		expandBranch(&br.goBranch)
		if br.repo == "go" {
			br.branch = br.goBranch
		}
		return br
	}
	tests := []struct {
		br   builderAndRepo
		want want
	}{
		{b("linux-amd64", "go"), both},
		{b("linux-amd64", "net"), both},
		{b("linux-amd64", "sys"), both},
		{b("linux-amd64", "website"), both},

		// Don't test all subrepos on all the builders.
		{b("linux-amd64-ssacheck", "net"), none},
		{b("linux-amd64-ssacheck@go1.10", "net"), none},
		{b("linux-386-387", "crypto"), onlyPost},
		{b("linux-arm-arm5spacemonkey@go1.12", "net"), none},
		{b("linux-arm-arm5spacemonkey", "exp"), none},
		{b("linux-arm-arm5spacemonkey", "mobile"), none},

		// The mobile repo requires Go 1.13+.
		{b("android-amd64-emu", "mobile"), both},
		{b("android-amd64-emu", "mobile@1.10"), none},
		{b("android-amd64-emu", "mobile@1.11"), none},
		{b("android-amd64-emu@go1.10", "mobile"), none},
		{b("android-amd64-emu@go1.12", "mobile"), none},
		{b("android-amd64-emu@go1.13", "mobile"), both},
		{b("android-amd64-emu", "mobile@1.13"), both},
		{b("freebsd-386-11_1@go1.12", "mobile"), none}, // This was golang.org/issue/36506.

		{b("android-amd64-emu", "go"), both},
		{b("android-amd64-emu", "crypto"), both},
		{b("android-amd64-emu", "net"), both},
		{b("android-amd64-emu", "sync"), both},
		{b("android-amd64-emu", "sys"), both},
		{b("android-amd64-emu", "text"), both},
		{b("android-amd64-emu", "time"), both},
		{b("android-amd64-emu", "tools"), both},
		{b("android-amd64-emu", "website"), none},

		{b("android-386-emu", "go"), onlyPost},
		{b("android-386-emu", "mobile"), onlyPost},
		{b("android-386-emu", "mobile@1.10"), none},
		{b("android-386-emu", "mobile@1.11"), none},
		{b("android-386-emu@go1.10", "mobile"), none},
		{b("android-386-emu@go1.12", "mobile"), none},
		{b("android-386-emu@go1.13", "mobile"), onlyPost},
		{b("android-386-emu", "mobile@1.13"), onlyPost},

		{b("linux-amd64", "net"), both},
		{b("linux-amd64", "net@1.12"), both},
		{b("linux-amd64@go1.12", "net@1.12"), both},
		{b("linux-amd64", "net@1.11"), both},
		{b("linux-amd64", "net@1.11"), both},
		{b("linux-amd64", "net@1.10"), none},   // too old
		{b("linux-amd64@go1.10", "net"), none}, // too old
		{b("linux-amd64@go1.12", "net@1.12"), both},

		{b("linux-mips64le-mengzhuo", "go"), onlyPost},
		{b("linux-mips64le-mengzhuo", "sys"), onlyPost},
		{b("linux-mips64le-mengzhuo", "net"), onlyPost},

		// go1.12.html: "Go 1.12 is the last release that is
		// supported on FreeBSD 10.x [... and 11.1]"
		{b("freebsd-386-10_3", "go"), none},
		{b("freebsd-386-10_3", "net"), none},
		{b("freebsd-386-10_3", "mobile"), none},
		{b("freebsd-amd64-10_3", "go"), none},
		{b("freebsd-amd64-10_3", "net"), none},
		{b("freebsd-amd64-10_3", "mobile"), none},
		{b("freebsd-amd64-11_1", "go"), none},
		{b("freebsd-amd64-11_1", "net"), none},
		{b("freebsd-amd64-11_1", "mobile"), none},
		{b("freebsd-amd64-10_3@go1.12", "go"), both},
		{b("freebsd-amd64-10_3@go1.12", "net@1.12"), both},
		{b("freebsd-amd64-10_3@go1.12", "mobile"), none},
		{b("freebsd-amd64-10_4@go1.12", "go"), isBuilder},
		{b("freebsd-amd64-10_4@go1.12", "net"), isBuilder},
		{b("freebsd-amd64-10_4@go1.12", "mobile"), none},
		{b("freebsd-amd64-11_1@go1.13", "go"), none},
		{b("freebsd-amd64-11_1@go1.13", "net@1.12"), none},
		{b("freebsd-amd64-11_1@go1.13", "mobile"), none},
		{b("freebsd-amd64-11_1@go1.12", "go"), isBuilder},
		{b("freebsd-amd64-11_1@go1.12", "net@1.12"), isBuilder},
		{b("freebsd-amd64-11_1@go1.12", "mobile"), none},

		// FreeBSD 12.0
		{b("freebsd-amd64-12_0", "go"), both},
		{b("freebsd-amd64-12_0", "net"), both},
		{b("freebsd-amd64-12_0", "mobile"), none},
		{b("freebsd-386-12_0", "go"), onlyPost},
		{b("freebsd-386-12_0", "net"), onlyPost},
		{b("freebsd-386-12_0", "mobile"), none},

		// NetBSD
		{b("netbsd-amd64-9_0", "go"), onlyPost},
		{b("netbsd-amd64-9_0", "net"), onlyPost},
		{b("netbsd-amd64-9_0", "sys"), both},
		{b("netbsd-386-9_0", "go"), onlyPost},
		{b("netbsd-386-9_0", "net"), onlyPost},

		// AIX starts at Go 1.12
		{b("aix-ppc64", "go"), onlyPost},
		{b("aix-ppc64", "net"), onlyPost},
		{b("aix-ppc64", "mobile"), none},
		{b("aix-ppc64", "exp"), none},
		{b("aix-ppc64", "term"), onlyPost},
		{b("aix-ppc64@go1.12", "go"), onlyPost},
		{b("aix-ppc64@go1.12", "net"), none},
		{b("aix-ppc64@go1.12", "mobile"), none},
		{b("aix-ppc64@go1.13", "net"), onlyPost},
		{b("aix-ppc64@go1.13", "mobile"), none},
		{b("aix-ppc64@dev.link", "go"), onlyPost},

		{b("linux-amd64-nocgo", "mobile"), none},

		// Virtual mobiledevices
		{b("darwin-arm64-corellium", "go"), isBuilder},
		{b("android-arm64-corellium", "go"), isBuilder},
		{b("android-arm-corellium", "go"), isBuilder},

		// Mobile builders that run with GOOS=linux/darwin and have
		// a device attached.
		{b("linux-amd64-androidemu", "mobile"), both},

		// But the emulators run all:
		{b("android-amd64-emu", "mobile"), isBuilder},
		{b("android-386-emu", "mobile"), isBuilder},
		{b("android-amd64-emu", "net"), isBuilder},
		{b("android-386-emu", "net"), isBuilder},
		{b("android-amd64-emu", "go"), isBuilder},
		{b("android-386-emu", "go"), isBuilder},

		{b("nacl-386", "go"), none},
		{b("nacl-386@dev.link", "go"), none},
		{b("nacl-386@go1.13", "go"), onlyPost},
		{b("nacl-386", "net"), none},
		{b("nacl-amd64p32", "go"), none},
		{b("nacl-amd64p32@dev.link", "go"), none},
		{b("nacl-amd64p32@go1.13", "go"), both},
		{b("nacl-amd64p32", "net"), none},

		// Only test tip for js/wasm, and only for some repos:
		{b("js-wasm", "go"), both},
		{b("js-wasm", "arch"), onlyPost},
		{b("js-wasm", "crypto"), onlyPost},
		{b("js-wasm", "sys"), onlyPost},
		{b("js-wasm", "net"), onlyPost},
		{b("js-wasm@go1.12", "net"), none},
		{b("js-wasm", "benchmarks"), none},
		{b("js-wasm", "debug"), none},
		{b("js-wasm", "mobile"), none},
		{b("js-wasm", "perf"), none},
		{b("js-wasm", "talks"), none},
		{b("js-wasm", "tools"), none},
		{b("js-wasm", "tour"), none},
		{b("js-wasm", "website"), none},

		// Race builders. Linux for all, GCE buidlers for
		// post-submit, and only post-submit for "go" for
		// Darwin (limited resources).
		{b("linux-amd64-race", "go"), both},
		{b("linux-amd64-race", "net"), both},
		{b("windows-amd64-race", "go"), onlyPost},
		{b("windows-amd64-race", "net"), onlyPost},
		{b("freebsd-amd64-race", "go"), onlyPost},
		{b("freebsd-amd64-race", "net"), onlyPost},
		{b("darwin-amd64-race", "go"), onlyPost},
		{b("darwin-amd64-race", "net"), none},

		// Long test.
		{b("linux-amd64-longtest", "go"), onlyPost},
		{b("linux-amd64-longtest", "net"), onlyPost},
		{b("linux-amd64-longtest@go1.14", "go"), both},
		{b("linux-amd64-longtest@go1.14", "net"), none},
		{b("windows-amd64-longtest", "go"), onlyPost},
		{b("windows-amd64-longtest@go1.14", "go"), both},
		{b("windows-amd64-longtest", "net"), onlyPost},
		{b("windows-amd64-longtest", "exp"), onlyPost},
		{b("windows-amd64-longtest", "mobile"), none},

		// Experimental exp repo runs in very few places.
		{b("linux-amd64", "exp"), both},
		{b("linux-amd64-race", "exp"), both},
		{b("linux-amd64-longtest", "exp"), onlyPost},
		{b("windows-386-2008", "exp"), both},
		{b("windows-amd64-2016", "exp"), both},
		{b("darwin-amd64-10_14", "exp"), onlyPost},
		{b("darwin-amd64-10_15", "exp"), onlyPost},
		// ... but not on most others:
		{b("darwin-amd64-10_12", "exp"), none},
		{b("freebsd-386-10_3@go1.12", "exp"), none},
		{b("freebsd-386-10_4@go1.12", "exp"), none},
		{b("freebsd-386-11_1@go1.12", "exp"), none},
		{b("freebsd-386-11_2", "exp"), none},
		{b("freebsd-386-12_0", "exp"), none},
		{b("freebsd-amd64-10_3@go1.12", "exp"), none},
		{b("freebsd-amd64-10_4@go1.12", "exp"), none},
		{b("freebsd-amd64-11_1@go1.12", "exp"), none},
		{b("freebsd-amd64-11_2", "exp"), none},
		{b("freebsd-amd64-12_0", "exp"), none},
		{b("openbsd-amd64-62", "exp"), none},
		{b("openbsd-amd64-64", "exp"), none},
		{b("js-wasm", "exp"), none},

		// exp is experimental; it doesn't test against release branches.
		{b("linux-amd64@go1.12", "exp"), none},

		// the build repo is only really useful for linux-amd64 (where we run it),
		// and darwin-amd64 and perhaps windows-amd64 (for stuff like gomote).
		// No need for any other operating systems to use it.
		{b("linux-amd64", "build"), both},
		{b("linux-amd64-longtest", "build"), onlyPost},
		{b("windows-amd64-2016", "build"), both},
		{b("darwin-amd64-10_12", "build"), none},
		{b("darwin-amd64-10_14", "build"), none},
		{b("darwin-amd64-10_15", "build"), onlyPost},
		{b("openbsd-amd64-64", "build"), none},
		{b("linux-amd64-fedora", "build"), none},
		{b("linux-amd64-clang", "build"), none},
		{b("linux-amd64-sid", "build"), none},
		{b("linux-amd64-nocgo", "build"), none},
		{b("linux-386-longtest", "build"), none},
		{b("freebsd-386-10_3", "build"), none},
		{b("freebsd-386-10_4", "build"), none},
		{b("freebsd-386-11_1", "build"), none},
		{b("js-wasm", "build"), none},
		{b("android-386-emu", "build"), none},
		{b("android-amd64-emu", "build"), none},

		// Only use latest macOS for subrepos, and only amd64:
		{b("darwin-amd64-10_12", "net"), onlyPost},
		{b("darwin-amd64-10_11", "net"), none},
		{b("darwin-amd64-10_11@go1.12", "net"), none},

		{b("darwin-amd64-10_15", "go"), onlyPost},
		{b("darwin-amd64-10_14", "go"), onlyPost},
		{b("darwin-amd64-10_12", "go"), onlyPost},
		{b("darwin-amd64-10_11", "go"), none},
		{b("darwin-amd64-10_11@go1.14", "go"), onlyPost}, // Go 1.14 is the last release that will run on macOS 10.11 El Capitan.
		{b("darwin-amd64-10_11@go1.15", "go"), none},     // Go 1.15 will require macOS 10.12 Sierra or later.
		{b("darwin-386-10_14", "go"), none},
		{b("darwin-386-10_14@go1.13", "go"), onlyPost},
		{b("darwin-386-10_14@go1.14", "go"), onlyPost}, // Go 1.14 is the last release that supports 32-bit on macOS.
		{b("darwin-386-10_14@go1.15", "go"), none},

		// plan9 only lived at master. We didn't support any past releases.
		// But it's off for now as it's always failing.
		{b("plan9-386", "go"), none},  // temporarily disabled
		{b("plan9-386", "net"), none}, // temporarily disabled
		{b("plan9-386", "exp"), none},
		{b("plan9-386", "mobile"), none},
		{b("plan9-386@go1.12", "go"), none},
		{b("plan9-386@go1.12", "net"), none},
		{b("plan9-amd64-9front", "go"), onlyPost},
		{b("plan9-amd64-9front", "exp"), none},
		{b("plan9-amd64-9front", "mobile"), none},
		{b("plan9-amd64-9front@go1.12", "go"), none},
		{b("plan9-amd64-9front", "net"), onlyPost},
		{b("plan9-amd64-9front@go1.12", "net"), none},
		{b("plan9-arm", "go"), onlyPost},
		{b("plan9-arm", "exp"), none},
		{b("plan9-arm", "mobile"), none},
		{b("plan9-arm@go1.12", "go"), none},
		{b("plan9-arm", "net"), onlyPost},
		{b("plan9-arm@go1.12", "net"), none},

		{b("dragonfly-amd64", "go"), onlyPost},
		{b("dragonfly-amd64", "net"), onlyPost},
		{b("dragonfly-amd64@go1.13", "net"), none}, // Dragonfly ABI changes only supported by Go 1.14+
		{b("dragonfly-amd64@go1.13", "go"), none},  // Dragonfly ABI changes only supported by Go 1.14+
		{b("dragonfly-amd64-5_8", "go"), onlyPost},
		{b("dragonfly-amd64-5_8", "net"), onlyPost},
		{b("dragonfly-amd64-5_8@go1.13", "net"), onlyPost},

		{b("linux-amd64-staticlockranking", "go"), onlyPost},
		{b("linux-amd64-staticlockranking@go1.15", "go"), onlyPost},
		{b("linux-amd64-staticlockranking@go1.14", "go"), none},
		{b("linux-amd64-staticlockranking", "net"), none},
	}
	for _, tt := range tests {
		t.Run(tt.br.testName, func(t *testing.T) {
			bc, ok := Builders[tt.br.builder]
			if !ok {
				t.Fatalf("unknown builder %q", tt.br.builder)
			}
			gotPost := bc.BuildsRepoPostSubmit(tt.br.repo, tt.br.branch, tt.br.goBranch)
			if tt.want&isBuilder != 0 && !gotPost {
				t.Errorf("not a post-submit builder, but expected")
			}
			if tt.want&notBuilder != 0 && gotPost {
				t.Errorf("unexpectedly a post-submit builder")
			}

			gotTry := bc.BuildsRepoTryBot(tt.br.repo, tt.br.branch, tt.br.goBranch)
			if tt.want&isTrybot != 0 && !gotTry {
				t.Errorf("not trybot, but expected")
			}
			if tt.want&notTrybot != 0 && gotTry {
				t.Errorf("unexpectedly a trybot")
			}

			if t.Failed() {
				t.Logf("For: %+v", tt.br)
			}
		})
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

// tests that goBranch is optional for repo == "go"
func TestBuildsRepoAtAllImplicitGoBranch(t *testing.T) {
	builder := Builders["android-amd64-emu"]
	got := builder.buildsRepoAtAll("go", "master", "")
	if !got {
		t.Error("got = false; want true")
	}
}

func TestShouldRunDistTest(t *testing.T) {
	type buildMode int
	const (
		tryMode    buildMode = 0
		postSubmit buildMode = 1
	)

	tests := []struct {
		builder string
		test    string
		mode    buildMode
		want    bool
	}{
		{"linux-amd64", "api", postSubmit, true},
		{"linux-amd64", "api", tryMode, true},
		{"freebsd-amd64-12_0", "api", postSubmit, true}, // freebsd-amd64-12_0 uses fasterTrybots policy, should still build.
		{"freebsd-amd64-12_0", "api", tryMode, false},   // freebsd-amd64-12_0 uses fasterTrybots policy, should skip in try mode.

		{"linux-amd64", "reboot", tryMode, true},
		{"linux-amd64-race", "reboot", tryMode, false},

		{"darwin-amd64-10_11", "test:foo", postSubmit, false},
		{"darwin-amd64-10_12", "test:foo", postSubmit, false},
		{"darwin-amd64-10_14", "test:foo", postSubmit, false},
		{"darwin-amd64-10_14", "reboot", postSubmit, false},
		{"darwin-amd64-10_14", "api", postSubmit, false},
		{"darwin-amd64-10_14", "codewalk", postSubmit, false},
		{"darwin-amd64-10_15", "test:foo", postSubmit, false},
	}
	for _, tt := range tests {
		bc, ok := Builders[tt.builder]
		if !ok {
			t.Errorf("unknown builder %q", tt.builder)
			continue
		}
		isTry := tt.mode == tryMode
		if isTry && !bc.BuildsRepoTryBot("go", "master", "master") {
			t.Errorf("builder %q is not a trybot, so can't run test %q in try mode", tt.builder, tt.test)
			continue
		}
		got := bc.ShouldRunDistTest(tt.test, isTry)
		if got != tt.want {
			t.Errorf("%q.ShouldRunDistTest(%q, try %v) = %v; want %v", tt.builder, tt.test, isTry, got, tt.want)
		}
	}
}

func TestShouldTestPackageInGOPATHMode(t *testing.T) {
	// This function doesn't change behavior depending on the builder
	// at this time, so just use a common one.
	bc, ok := Builders["linux-amd64"]
	if !ok {
		t.Fatal("unknown builder")
	}

	tests := []struct {
		importPath string
		want       bool
	}{
		{"golang.org/x/image/bmp", true},
		{"golang.org/x/tools/go/ast/astutil", true},
		{"golang.org/x/tools/go/packages", true},
		{"golang.org/x/tools", true}, // Three isn't a package there, but if there was, it should be tested.
		{"golang.org/x/tools/gopls", false},
		{"golang.org/x/tools/gopls/internal/foobar", false},
	}
	for _, tt := range tests {
		got := bc.ShouldTestPackageInGOPATHMode(tt.importPath)
		if got != tt.want {
			t.Errorf("ShouldTestPackageInGOPATHMode(%q) = %v; want %v", tt.importPath, got, tt.want)
		}
	}
}

func TestSlowBotAliases(t *testing.T) {
	for term, name := range slowBotAliases {
		if name == "" {
			// Empty string means known missing builder.
			continue
		}
		if _, ok := Builders[name]; !ok {
			t.Errorf("slowbot term %q references unknown builder %q", term, name)
		}
	}

	out, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "tool", "dist", "list").Output()
	if err != nil {
		t.Errorf("dist list: %v", err)
	}
	ports := strings.Fields(string(out))

	done := map[string]bool{}
	done["windows-arm"] = true // TODO(golang.org/issue/38607) disabled until builder is replaced

	var add bytes.Buffer
	check := func(term string, isArch bool) {
		if done[term] {
			return
		}
		done[term] = true
		_, isBuilderName := Builders[term]
		_, hasAlias := slowBotAliases[term]
		if !isBuilderName && !hasAlias {
			prefix := term
			if isArch {
				prefix = "linux-" + term
			}
			var matches []string
			for name := range Builders {
				if strings.HasPrefix(name, prefix) {
					matches = append(matches, name)
				}
			}
			sort.Strings(matches)
			t.Errorf("term %q has no match in slowBotAliases", term)
			if len(matches) == 1 {
				fmt.Fprintf(&add, "%q: %q,\n", term, matches[0])
			} else if len(matches) > 1 {
				t.Errorf("maybe add:  %q: %q,    (matches=%q)", term, matches[len(matches)-1], matches)
			}
		}
	}

	for _, port := range ports {
		slash := strings.IndexByte(port, '/')
		if slash == -1 {
			t.Fatalf("unexpected port %q", port)
		}
		goos, goarch := port[:slash], port[slash+1:]
		check(goos+"-"+goarch, false)
		check(goos, false)
		check(goarch, true)
	}

	if add.Len() > 0 {
		t.Errorf("Missing items from slowBotAliases:\n%s", add.String())
	}
}

func TestCrossCompileConfigs(t *testing.T) {
	// Verify that Builders.CrossCompileConfig have valid host types.
	for name, bc := range Builders {
		cc := bc.CrossCompileConfig
		if cc == nil {
			continue
		}
		if _, ok := Hosts[cc.CompileHostType]; !ok {
			t.Errorf("unknown host type %q for builder %q", cc.CompileHostType, name)
		}
	}
}

// TestTryBotsCompileAllPorts verifies that each port (go tool dist list) is covered by
// either a real trybot or a misc-compile trybot.
func TestTryBotsCompileAllPorts(t *testing.T) {
	out, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "tool", "dist", "list").Output()
	if err != nil {
		t.Errorf("dist list: %v", err)
	}
	ports := strings.Fields(string(out))

	done := map[string]bool{}
	done["nacl-386"] = true    // removed in Go 1.14
	done["nacl-arm"] = true    // removed in Go 1.14
	done["windows-arm"] = true // TODO(golang.org/issue/38607) disabled until builder is replaced
	check := func(goos, goarch string) {
		if goos == "android" {
			// TODO(golang.org/issue/25963): support
			// compilation-only Android trybots.
			// buildall.bash doesn't set the environment
			// up enough for e.g. compiling android-386
			// from linux-amd64. (Issue #35596 too)
			return
		}
		goosArch := goos + "-" + goarch
		if done[goosArch] {
			return
		}
		for _, conf := range Builders {
			os := conf.GOOS()
			arch := conf.GOARCH()

			if os == goos && arch == goarch && (conf.tryOnly || conf.tryBot != nil) {
				done[goosArch] = true
				break
			}

			if strings.HasPrefix(conf.Name, "misc-compile-") {
				re, err := regexp.Compile(conf.allScriptArgs[0])
				if err != nil {
					t.Errorf("Invalid misc-compile filtering pattern for builder %q: %q",
						conf.Name, conf.allScriptArgs[0])
				}

				if re.MatchString(goosArch) || re.MatchString(goos) {
					done[goosArch] = true
					break
				}
			}
		}
		if _, ok := done[goosArch]; !ok {
			t.Errorf("Missing trybot or misc-compile trybot: %q", goosArch)
		}
	}

	for _, port := range ports {
		slash := strings.IndexByte(port, '/')
		if slash == -1 {
			t.Fatalf("unexpected port %q", port)
		}
		check(port[:slash], port[slash+1:])
	}

}

// TestExpectedMacstadiumVMCount ensures that only 20 instances of macOS virtual machines
// are expected at MacStadium.
// TODO: remove once the scheduler allocates VMs based on demand https://golang.org/issue/35698
func TestExpectedMacstadiumVMCount(t *testing.T) {
	got := 0
	for host, config := range Hosts {
		if strings.HasPrefix(host, "host-darwin-10_") {
			got += config.ExpectNum
		}
	}
	if got != 20 {
		t.Fatalf("macstadium host count: got %d; want 20", got)
	}
}

// Test that we have a longtest builder and
// that its environment configuration is okay.
func TestLongTestBuilder(t *testing.T) {
	long, ok := Builders["linux-amd64-longtest"]
	if !ok {
		t.Fatal("we don't have a linux-amd64-longtest builder anymore, is that intentional?")
	}
	if !long.IsLongTest() {
		t.Error("the linux-amd64-longtest builder isn't a longtest builder, is that intentional?")
	}
	var shortDisabled bool
	for _, e := range long.Env() {
		if e == "GO_TEST_SHORT=0" {
			shortDisabled = true
		}
	}
	if !shortDisabled {
		t.Error("the linux-amd64-longtest builder doesn't set GO_TEST_SHORT=0, is that intentional?")
	}
}
