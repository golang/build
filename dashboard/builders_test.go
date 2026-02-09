// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dashboard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/build/internal/migration"
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
				TestHostConf: &HostConfig{},
			},
			20 * time.Minute,
		},
		{
			&BuildConfig{
				env:          []string{"GO_TEST_TIMEOUT_SCALE=2"},
				TestHostConf: &HostConfig{},
			},
			40 * time.Minute,
		},
		{
			&BuildConfig{
				env: []string{},
				TestHostConf: &HostConfig{
					env: []string{"GO_TEST_TIMEOUT_SCALE=3"},
				},
			},
			60 * time.Minute,
		},
		// BuildConfig's env takes precedence:
		{
			&BuildConfig{
				env: []string{"GO_TEST_TIMEOUT_SCALE=2"},
				TestHostConf: &HostConfig{
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

// TestTrybots tests that a given repo & its branch yields the provided complete
// set of builders. See also: TestPostSubmit, which tests only post-submit
// builders, and TestBuilderConfig, which tests both trybots and post-submit
// builders, both at arbitrary branches.
func TestTrybots(t *testing.T) {
	tests := []struct {
		repo   string // "go", "net", etc
		branch string // of repo
		want   []string
	}{
		{
			repo:   "go",
			branch: "master",
			want:   []string{
				// Stopped.
				//"freebsd-amd64-12_3",
				//"linux-386",
				//"linux-amd64",
				//"linux-amd64-boringcrypto",
				//"linux-amd64-newinliner",
				//"linux-amd64-race",
				//"linux-arm64",
				//"openbsd-amd64-72",
				//"windows-386-2016",
				//"windows-amd64-2016",
			},
		},
		{
			repo:   "go",
			branch: "release-branch.go1.22",
			want:   []string{
				// Stopped.
				//"freebsd-amd64-12_3",
				//"linux-386",
				//"linux-amd64",
				//"linux-amd64-boringcrypto",
				//"linux-amd64-race",
				//"linux-arm64",
				//"openbsd-amd64-72",
				//"windows-386-2016",
				//"windows-amd64-2016",

				// Include longtest builders on Go repo release branches. See issue 37827.
				// Stopped.
				//"linux-386-longtest",
				//"linux-amd64-longtest",
				//"linux-arm64-longtest",
				//"windows-amd64-longtest",
			},
		},
		{
			repo:   "go",
			branch: "release-branch.go1.21",
			want:   []string{
				// Stopped.
				//"freebsd-amd64-12_3",
				//"linux-386",
				//"linux-amd64",
				//"linux-amd64-boringcrypto",
				//"linux-amd64-race",
				//"linux-arm64",
				//"openbsd-amd64-72",
				//"windows-386-2016",
				//"windows-amd64-2016",

				// Include longtest builders on Go repo release branches. See issue 37827.
				// Stopped.
				//"linux-386-longtest",
				//"linux-amd64-longtest",
				//"linux-arm64-longtest",
				//"windows-amd64-longtest",
			},
		},
		{
			repo:   "mobile",
			branch: "master",
			want: []string{
				"android-amd64-emu",
				"linux-amd64-androidemu",
				// Stopped.
				//"linux-amd64",
				//"linux-amd64-race",
			},
		},
		{
			repo:   "sys",
			branch: "master",
			want: []string{
				"freebsd-386-13_0",
				// Stopped.
				//"freebsd-amd64-12_3",
				//"freebsd-amd64-13_0",
				//"linux-386",
				//"linux-amd64",
				//"linux-amd64-boringcrypto", // GoDeps will exclude, but not in test
				//"linux-amd64-race",
				//"linux-arm64",
				"netbsd-amd64-9_3",
				"openbsd-386-72",
				// Stopped.
				//"openbsd-amd64-72",
				//"windows-386-2016",
				//"windows-amd64-2016",
			},
		},
		{
			repo:   "exp",
			branch: "master",
			want:   []string{
				// Stopped.
				//"linux-amd64",
				//"linux-amd64-race",
				//"windows-amd64-2016",
			},
		},
		{
			repo:   "vulndb",
			branch: "master",
			want:   []string{
				// Stopped.
				//"linux-amd64",
				//"linux-amd64-race",
			},
		},
		{
			repo:   "website",
			branch: "master",
			want:   []string{
				// Stopped.
				//"linux-amd64",
				//"linux-amd64-race",
			},
		},
	}
	for i, tt := range tests {
		if tt.branch == "" || tt.repo == "" {
			t.Errorf("incomplete test entry %d", i)
			return
		}
		t.Run(fmt.Sprintf("%s/%s", tt.repo, tt.branch), func(t *testing.T) {
			goBranch := tt.branch // hard-code the common case for now
			got := TryBuildersForProject(tt.repo, tt.branch, goBranch)
			checkBuildersForProject(t, got, tt.want)
		})
	}
}

func checkBuildersForProject(t *testing.T, gotBuilders []*BuildConfig, want []string) {
	t.Helper()

	var got []string
	for _, bc := range gotBuilders {
		got = append(got, bc.Name)
	}
	m := map[string]bool{}
	for _, b := range want {
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
}

// TestPostSubmit tests that a given repo & its branch yields the provided
// complete set of post-submit builders. See also: TestTrybots, which tests only
// trybots, and TestBuilderConfig, which tests both trybots and post-submit
// builders, both at arbitrary branches.
func TestPostSubmit(t *testing.T) {
	tests := []struct {
		repo   string // "go", "net", etc
		branch string // of repo
		want   []string
	}{
		{
			repo:   "vulndb",
			branch: "master",
			want:   []string{
				// Stopped.
				//"linux-amd64",
				//"linux-amd64-longtest",
				//"linux-amd64-race",
				//"linux-amd64-longtest-race",
			},
		},
		{
			repo:   "website",
			branch: "master",
			want:   []string{
				// Stopped.
				//"linux-amd64",
				//"linux-amd64-longtest",
				//"linux-amd64-race",
				//"linux-amd64-longtest-race",
			},
		},
	}
	for i, tt := range tests {
		if tt.branch == "" || tt.repo == "" {
			t.Fatalf("incomplete test entry %d", i)
		}
		t.Run(fmt.Sprintf("%s/%s", tt.repo, tt.branch), func(t *testing.T) {
			goBranch := tt.branch // hard-code the common case for now
			got := buildersForProject(tt.repo, tt.branch, goBranch, (*BuildConfig).BuildsRepoPostSubmit)
			checkBuildersForProject(t, got, tt.want)
		})
	}
}

// TestBuilderConfig tests whether a given builder and repo at different branches is
// completely disabled ("none"),
// a TryBot and a post-submit builder ("both"), or
// a post-submit only builder ("onlyPost").
func TestBuilderConfig(t *testing.T) {
	// want is a bitmask of 4 different things to assert are wanted:
	// - being a post-submit builder
	// - NOT being a post-submit builder
	// - being a trybot builder
	// - NOT being a post-submit builder
	// Note: a builder cannot be configured as a TryBot without also being a post-submit builder.
	type want uint8
	const (
		isTrybot want = 1 << iota
		notTrybot
		isBuilder  // post-submit
		notBuilder // not post-submit

		// Available combinations:
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
		want want // none, both, or onlyPost.
	}{
		{b("linux-amd64", "go"), both},
		{b("linux-amd64", "net"), both},
		{b("linux-amd64", "sys"), both},
		{b("linux-amd64", "website"), both},

		// Don't test all subrepos on all the builders.
		{b("linux-amd64-ssacheck", "net"), none},
		{b("linux-amd64-ssacheck@go1.99", "net"), none},
		{b("linux-386-softfloat", "crypto"), onlyPost},
		{b("linux-386-softfloat@go1.99", "crypto"), onlyPost},

		{b("android-amd64-emu", "go"), onlyPost},
		{b("android-amd64-emu", "mobile"), both},
		{b("android-amd64-emu", "crypto"), onlyPost},
		{b("android-amd64-emu", "net"), onlyPost},
		{b("android-amd64-emu", "sync"), onlyPost},
		{b("android-amd64-emu", "sys"), onlyPost},
		{b("android-amd64-emu", "text"), onlyPost},
		{b("android-amd64-emu", "time"), onlyPost},
		{b("android-amd64-emu", "tools"), onlyPost},
		{b("android-amd64-emu", "website"), none},

		{b("android-386-emu", "go"), onlyPost},
		{b("android-386-emu", "mobile"), onlyPost},
		{b("android-386-emu", "crypto"), onlyPost},

		{b("linux-amd64", "net"), both},

		// Builders for linux/loong64 are fully ported to LUCI and stopped in the coordinator.
		{b("linux-loong64-3a5000", "go"), none},
		{b("linux-loong64-3a5000@go1.99", "go"), none},
		{b("linux-loong64-3a5000", "sys"), none},
		{b("linux-loong64-3a5000@go1.99", "sys"), none},
		{b("linux-loong64-3a5000", "net"), none},

		// OpenBSD 7.2.
		{b("openbsd-amd64-72", "go"), both},
		{b("openbsd-amd64-72@go1.99", "go"), both},

		// FreeBSD 13.0
		{b("freebsd-amd64-13_0", "go"), onlyPost},
		{b("freebsd-amd64-13_0", "net"), onlyPost},
		{b("freebsd-amd64-13_0", "mobile"), none},
		{b("freebsd-386-13_0", "go"), onlyPost},
		{b("freebsd-386-13_0", "net"), onlyPost},
		{b("freebsd-386-13_0", "mobile"), none},

		// FreeBSD 12.3
		{b("freebsd-amd64-12_3", "go"), both},
		{b("freebsd-amd64-12_3", "net"), both},
		{b("freebsd-amd64-12_3", "mobile"), none},
		{b("freebsd-386-12_3", "go"), onlyPost},
		{b("freebsd-386-12_3", "net"), onlyPost},
		{b("freebsd-386-12_3", "mobile"), none},

		// NetBSD
		{b("netbsd-amd64-9_3", "go"), onlyPost},
		{b("netbsd-amd64-9_3", "net"), onlyPost},
		{b("netbsd-amd64-9_3", "sys"), both},
		{b("netbsd-386-9_3", "go"), onlyPost},
		{b("netbsd-386-9_3", "net"), onlyPost},

		// AIX
		{b("aix-ppc64", "go"), onlyPost},
		{b("aix-ppc64", "net"), onlyPost},
		{b("aix-ppc64", "mobile"), none},
		{b("aix-ppc64", "exp"), none},
		{b("aix-ppc64", "term"), onlyPost},

		{b("linux-amd64-nocgo", "mobile"), none},

		// Virtual mobiledevices
		{b("ios-arm64-corellium", "go"), onlyPost},
		{b("android-arm64-corellium", "go"), onlyPost},
		{b("android-arm-corellium", "go"), onlyPost},

		// Mobile builders that run with GOOS=linux/ios and have
		// a device attached.
		{b("linux-amd64-androidemu", "mobile"), both},

		// The Android emulator builders can test all repos.
		{b("android-amd64-emu", "mobile"), both},
		{b("android-386-emu", "mobile"), onlyPost},
		{b("android-amd64-emu", "net"), onlyPost},
		{b("android-386-emu", "net"), onlyPost},
		{b("android-amd64-emu", "go"), onlyPost},
		{b("android-386-emu", "go"), onlyPost},

		// Builders for js/wasm are fully ported to LUCI and stopped in the coordinator.
		{b("js-wasm-node18", "go"), none},
		{b("js-wasm-node18@go1.21", "go"), none},
		{b("js-wasm-node18@go1.20", "go"), none},
		{b("js-wasm-node18", "arch"), none},
		{b("js-wasm-node18", "crypto"), none},
		{b("js-wasm-node18", "sys"), none},
		{b("js-wasm-node18", "net"), none},
		{b("js-wasm-node18", "benchmarks"), none},
		{b("js-wasm-node18", "debug"), none},
		{b("js-wasm-node18", "mobile"), none},
		{b("js-wasm-node18", "perf"), none},
		{b("js-wasm-node18", "talks"), none},
		{b("js-wasm-node18", "tools"), none},
		{b("js-wasm-node18", "tour"), none},
		{b("js-wasm-node18", "website"), none},

		// Builders for wasip1-wasm are fully ported to LUCI and stopped in the coordinator.
		{b("wasip1-wasm-wazero", "go"), none},
		{b("wasip1-wasm-wazero@go1.21", "go"), none},
		{b("wasip1-wasm-wazero@go1.20", "go"), none},
		{b("wasip1-wasm-wasmtime", "go"), none},
		{b("wasip1-wasm-wasmtime@go1.21", "go"), none},
		{b("wasip1-wasm-wasmtime@go1.20", "go"), none},
		{b("wasip1-wasm-wasmer", "go"), none},
		{b("wasip1-wasm-wasmer@go1.21", "go"), none},
		{b("wasip1-wasm-wasmer@go1.20", "go"), none},
		{b("wasip1-wasm-wasmedge", "go"), none},
		{b("wasip1-wasm-wasmedge@go1.21", "go"), none},
		{b("wasip1-wasm-wasmedge@go1.20", "go"), none},
		{b("wasip1-wasm-wazero", "arch"), none},
		{b("wasip1-wasm-wazero", "crypto"), none},
		{b("wasip1-wasm-wazero", "sys"), none},
		{b("wasip1-wasm-wazero", "net"), none},
		{b("wasip1-wasm-wazero", "benchmarks"), none},
		{b("wasip1-wasm-wazero", "debug"), none},
		{b("wasip1-wasm-wazero", "mobile"), none},
		{b("wasip1-wasm-wazero", "perf"), none},
		{b("wasip1-wasm-wazero", "talks"), none},
		{b("wasip1-wasm-wazero", "tools"), none},
		{b("wasip1-wasm-wazero", "tour"), none},
		{b("wasip1-wasm-wazero", "website"), none},
		{b("wasip1-wasm-wasmtime", "arch"), none},
		{b("wasip1-wasm-wasmtime", "crypto"), none},
		{b("wasip1-wasm-wasmtime", "sys"), none},
		{b("wasip1-wasm-wasmtime", "net"), none},
		{b("wasip1-wasm-wasmtime", "benchmarks"), none},
		{b("wasip1-wasm-wasmtime", "debug"), none},
		{b("wasip1-wasm-wasmtime", "mobile"), none},
		{b("wasip1-wasm-wasmtime", "perf"), none},
		{b("wasip1-wasm-wasmtime", "talks"), none},
		{b("wasip1-wasm-wasmtime", "tools"), none},
		{b("wasip1-wasm-wasmtime", "tour"), none},
		{b("wasip1-wasm-wasmtime", "website"), none},
		{b("wasip1-wasm-wasmer", "arch"), none},
		{b("wasip1-wasm-wasmer", "crypto"), none},
		{b("wasip1-wasm-wasmer", "sys"), none},
		{b("wasip1-wasm-wasmer", "net"), none},
		{b("wasip1-wasm-wasmer", "benchmarks"), none},
		{b("wasip1-wasm-wasmer", "debug"), none},
		{b("wasip1-wasm-wasmer", "mobile"), none},
		{b("wasip1-wasm-wasmer", "perf"), none},
		{b("wasip1-wasm-wasmer", "talks"), none},
		{b("wasip1-wasm-wasmer", "tools"), none},
		{b("wasip1-wasm-wasmer", "tour"), none},
		{b("wasip1-wasm-wasmer", "website"), none},
		{b("wasip1-wasm-wasmedge", "arch"), none},
		{b("wasip1-wasm-wasmedge", "crypto"), none},
		{b("wasip1-wasm-wasmedge", "sys"), none},
		{b("wasip1-wasm-wasmedge", "net"), none},
		{b("wasip1-wasm-wasmedge", "benchmarks"), none},
		{b("wasip1-wasm-wasmedge", "debug"), none},
		{b("wasip1-wasm-wasmedge", "mobile"), none},
		{b("wasip1-wasm-wasmedge", "perf"), none},
		{b("wasip1-wasm-wasmedge", "talks"), none},
		{b("wasip1-wasm-wasmedge", "tools"), none},
		{b("wasip1-wasm-wasmedge", "tour"), none},
		{b("wasip1-wasm-wasmedge", "website"), none},

		// Race builders. Linux for all, GCE builders for
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
		{b("linux-amd64-longtest@go1.99", "go"), both},
		{b("linux-amd64-longtest@go1.99", "net"), none},
		{b("darwin-amd64-longtest", "go"), onlyPost},
		{b("darwin-amd64-longtest", "net"), onlyPost},
		{b("darwin-amd64-longtest@go1.99", "go"), onlyPost},
		{b("darwin-amd64-longtest@go1.99", "net"), none},
		{b("windows-amd64-longtest", "go"), onlyPost},
		{b("windows-amd64-longtest@go1.99", "go"), both},
		{b("windows-amd64-longtest", "net"), onlyPost},
		{b("windows-amd64-longtest", "exp"), onlyPost},
		{b("windows-amd64-longtest", "mobile"), none},
		{b("linux-386-longtest", "go"), onlyPost},
		{b("linux-386-longtest", "net"), onlyPost},
		{b("linux-386-longtest", "exp"), none},
		{b("linux-386-longtest", "mobile"), none},

		// Experimental exp repo runs in very few places.
		{b("linux-amd64", "exp"), both},
		{b("linux-amd64-race", "exp"), both},
		{b("linux-amd64-longtest", "exp"), onlyPost},
		{b("windows-386-2016", "exp"), none},
		{b("windows-amd64-2016", "exp"), both},
		{b("darwin-amd64-10_15", "exp"), none},
		{b("darwin-amd64-11_0", "exp"), onlyPost},
		// ... but not on most others:
		{b("freebsd-386-12_3", "exp"), none},
		{b("freebsd-amd64-12_3", "exp"), none},
		{b("js-wasm-node18", "exp"), none},
		{b("wasip1-wasm-wazero", "exp"), none},
		{b("wasip1-wasm-wasmtime", "exp"), none},
		{b("wasip1-wasm-wasmer", "exp"), none},
		{b("wasip1-wasm-wasmedge", "exp"), none},

		// exp is experimental; it doesn't test against release branches.
		{b("linux-amd64@go1.99", "exp"), none},

		// the build repo is only really useful for linux-amd64 (where we run it),
		// and darwin-amd64 and perhaps windows-amd64 (for stuff like gomote).
		// No need for any other operating systems to use it.
		{b("linux-amd64", "build"), both},
		{b("linux-amd64-longtest", "build"), onlyPost},
		{b("windows-amd64-2016", "build"), both},
		{b("darwin-amd64-10_15", "build"), none},
		{b("darwin-amd64-11_0", "build"), onlyPost},
		{b("linux-amd64-fedora", "build"), none},
		{b("linux-amd64-clang", "build"), none},
		{b("linux-amd64-sid", "build"), none},
		{b("linux-amd64-bullseye", "build"), none},
		{b("linux-amd64-bookworm", "build"), none},
		{b("linux-amd64-nocgo", "build"), none},
		{b("linux-386-longtest", "build"), none},

		{b("linux-amd64", "vulndb"), both},
		{b("linux-amd64-longtest", "vulndb"), onlyPost},

		{b("linux-amd64-sid@go1.22", "pkgsite"), none},
		{b("freebsd-amd64-13_0@go1.22", "pkgsite"), none},
		{b("linux-amd64@go1.20", "pkgsite-metrics"), both},

		{b("js-wasm-node18", "build"), none},
		{b("wasip1-wasm-wazero", "build"), none},
		{b("wasip1-wasm-wasmtime", "build"), none},
		{b("wasip1-wasm-wasmer", "build"), none},
		{b("wasip1-wasm-wasmedge", "build"), none},
		{b("android-386-emu", "build"), none},
		{b("android-amd64-emu", "build"), none},

		{b("darwin-amd64-11_0", "go"), onlyPost},
		// Go 1.22 is the last release with macOS 10.15 support:
		{b("darwin-amd64-10_15", "go"), none},
		{b("darwin-amd64-10_15@go1.23", "go"), none},
		{b("darwin-amd64-10_15@go1.22", "go"), onlyPost},
		{b("darwin-amd64-10_15", "net"), none},
		{b("darwin-amd64-10_15@go1.23", "net"), none},
		{b("darwin-amd64-10_15@go1.22", "net"), onlyPost},

		// The darwin longtest builder added during the Go 1.21 dev cycle:
		{b("darwin-amd64-longtest@go1.21", "go"), onlyPost},
		{b("darwin-amd64-longtest@go1.20", "go"), none},

		// plan9 only lived at master. We didn't support any past releases.
		// But it's off for now as it's always failing.
		{b("plan9-386", "go"), none},  // temporarily disabled
		{b("plan9-386", "net"), none}, // temporarily disabled
		{b("plan9-386", "exp"), none},
		{b("plan9-386", "mobile"), none},
		{b("plan9-386@go1.99", "go"), none},
		{b("plan9-386@go1.99", "net"), none},
		{b("plan9-amd64-0intro", "go"), onlyPost},
		{b("plan9-amd64-0intro", "exp"), none},
		{b("plan9-amd64-0intro", "mobile"), none},
		{b("plan9-amd64-0intro@go1.99", "go"), none},
		{b("plan9-amd64-0intro", "net"), onlyPost},
		{b("plan9-amd64-0intro@go1.99", "net"), none},
		{b("plan9-arm", "go"), onlyPost},
		{b("plan9-arm", "exp"), none},
		{b("plan9-arm", "mobile"), none},
		{b("plan9-amd64-0intro@go1.99", "go"), none},
		{b("plan9-amd64-0intro", "net"), onlyPost},
		{b("plan9-amd64-0intro@go1.99", "net"), none},
		{b("dragonfly-amd64-622", "go"), onlyPost},
		{b("dragonfly-amd64-622", "net"), onlyPost},

		{b("linux-amd64-staticlockranking", "go"), onlyPost},
		{b("linux-amd64-staticlockranking", "net"), none},

		{b("linux-amd64-newinliner", "go"), both},
		{b("linux-amd64-newinliner", "tools"), none},
		{b("linux-amd64-newinliner@go1.22", "go"), none},
	}
	for _, tt := range tests {
		t.Run(tt.br.testName, func(t *testing.T) {
			// Require a want value that asserts both dimensions: try or not, post or not.
			switch tt.want {
			case none, both, onlyPost:
				// OK.
			default:
				t.Fatalf("tt.want must be one of: none, both, or onlyPost")
			}

			bc, ok := Builders[tt.br.builder]
			if !ok {
				t.Fatalf("unknown builder %q", tt.br.builder)
			}
			gotPost := bc.BuildsRepoPostSubmit(tt.br.repo, tt.br.branch, tt.br.goBranch)
			if tt.want&isBuilder != 0 && !gotPost {
				if stopped := migration.BuildersPortedToLUCI[bc.Name] && migration.StopPortedBuilder; stopped {
					t.Logf("not a post-submit builder because it's intentionally stopped")
				} else {
					t.Errorf("not a post-submit builder, but expected")
				}
			}
			if tt.want&notBuilder != 0 && gotPost {
				t.Errorf("unexpectedly a post-submit builder")
			}

			gotTry := bc.BuildsRepoTryBot(tt.br.repo, tt.br.branch, tt.br.goBranch)
			if tt.want&isTrybot != 0 && !gotTry {
				if stopped := migration.BuildersPortedToLUCI[bc.Name] && migration.StopPortedBuilder; stopped {
					t.Logf("not a trybot builder because it's intentionally stopped")
				} else {
					t.Errorf("not trybot, but expected")
				}
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
	knownUnused := map[string]bool{}

	used := make(map[string]bool)
	for _, conf := range Builders {
		used[conf.HostType] = true
	}
	for hostType := range Hosts {
		if !used[hostType] && !knownUnused[hostType] {
			t.Errorf("host type %q is not referenced from any build config", hostType)
		}
		if used[hostType] && knownUnused[hostType] {
			t.Errorf("host type %q should not be listed in knownUnused since it's in use", hostType)
		}
	}
}

// Test that all specified builder owners are non-nil.
func TestBuilderOwners(t *testing.T) {
	for host, config := range Hosts {
		for i, p := range config.Owners {
			if p == nil {
				t.Errorf("dashboard.Hosts[%q].Owners[%d] is nil, want non-nil", host, i)
			}
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
	if stopped := migration.BuildersPortedToLUCI["linux-amd64"] && migration.StopPortedBuilder; stopped {
		t.Skip("test can't be used because linux builders are stopped")
	}

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
		{"freebsd-amd64-12_3", "api", postSubmit, true}, // freebsd-amd64-12_3 uses fasterTrybots policy, should still build.
		{"freebsd-amd64-12_3", "api", tryMode, false},   // freebsd-amd64-12_3 uses fasterTrybots policy, should skip in try mode.

		{"linux-amd64", "reboot", tryMode, true},
		{"linux-amd64-race", "reboot", tryMode, false},

		{"darwin-amd64-13", "test:foo", postSubmit, false},
		{"darwin-amd64-13", "reboot", postSubmit, false},
		{"darwin-amd64-13", "api", postSubmit, false},
		{"darwin-amd64-13", "codewalk", postSubmit, false},
		{"darwin-amd64-12_0", "test:foo", postSubmit, false},
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

	ports, err := listPorts()
	if err != nil {
		t.Fatal("listPorts:", err)
	}

	done := map[string]bool{}

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
		goos, goarch, ok := strings.Cut(port, "/")
		if !ok {
			t.Fatalf("unexpected port %q", port)
		}
		check(goos+"-"+goarch, false)
		check(goos, false)
		check(goarch, true)
	}

	if add.Len() > 0 {
		t.Errorf("Missing items from slowBotAliases:\n%s", add.String())
	}
}

// TestCrossCompileOnlyBuilders checks to make sure that only misc-compile
// builders and the linux-s390x-crosscompile builder have IsCrossCompileOnly
// return true.
func TestCrossCompileOnlyBuilders(t *testing.T) {
	for _, conf := range Builders {
		isMiscCompile := strings.HasPrefix(conf.Name, "misc-compile") || conf.Name == "linux-s390x-crosscompile"
		if ccOnly := conf.IsCrossCompileOnly(); isMiscCompile != ccOnly {
			t.Errorf("builder %q has unexpected IsCrossCompileOnly state (want %t, got %t)", conf.Name, isMiscCompile, ccOnly)
		}
	}
}

// TestTryBotsCompileAllPorts verifies that each port (go tool dist list)
// is covered by either a real TryBot or a misc-compile TryBot.
//
// The special pseudo-port 'linux-arm-arm5' is tested in TestMiscCompileLinuxGOARM5.
func TestTryBotsCompileAllPorts(t *testing.T) {
	if migration.StopLegacyMiscCompileTryBots {
		t.Log("nothing to test since legacy misc-compile trybots are stopped")
		return
	}

	ports, err := listPorts()
	if err != nil {
		t.Fatal("listPorts:", err)
	}

	// knownMissing tracks Go ports that that are known to be
	// completely missing TryBot (pre-submit) test coverage.
	//
	// All completed ports should have either a real TryBot or at least a misc-compile TryBot,
	// so this map is meant to be used to temporarily fix tests
	// when the work of adding a new port is actively underway.
	knownMissing := map[string]bool{
		"openbsd-mips64": true, // go.dev/issue/58110

		"js-wasm":     true, // Fully ported to LUCI and stopped in the coordinator.
		"wasip1-wasm": true, // Fully ported to LUCI and stopped in the coordinator.
	}

	var done = make(map[string]bool)
	check := func(goos, goarch string) {
		if goos == "android" || goos == "ios" {
			// TODO(golang.org/issue/25963): support
			// compilation-only Android and iOS trybots.
			// buildall.bash doesn't set the environment
			// up enough for e.g. compiling android-386
			// from linux-amd64. (Issue #35596 too)
			// iOS likely needs to be built on macOS
			// with Xcode available.
			return
		}
		goosArch := goos + "-" + goarch
		if done[goosArch] {
			return
		}
		for _, conf := range Builders {
			if conf.GOOS() == goos && conf.GOARCH() == goarch &&
				conf.BuildsRepoTryBot("go", "master", "master") {

				// There's a real TryBot for this GOOS/GOARCH pair.
				done[goosArch] = true
				break
			}

			if strings.HasPrefix(conf.Name, "misc-compile-") {
				var cGoos, cGoarch string
				for _, v := range conf.env {
					if strings.HasPrefix(v, "GOOS=") {
						cGoos = v[len("GOOS="):]
					}
					if strings.HasPrefix(v, "GOARCH=") {
						cGoarch = v[len("GOARCH="):]
					}
				}
				if cGoos == "" {
					t.Errorf("missing GOOS env var for misc-compile builder %q", conf.Name)
				}
				if cGoarch == "" {
					t.Errorf("missing GOARCH env var for misc-compile builder %q", conf.Name)
				}
				cGoosArch := cGoos + "-" + cGoarch
				if goosArch == cGoosArch {
					// There's a misc-compile TryBot for this GOOS/GOARCH pair.
					done[goosArch] = true
					break
				}
			}
		}
		if knownMissing[goosArch] && done[goosArch] {
			// Make it visible when a builder is added but the old
			// knownMissing entry isn't removed by failing the test.
			t.Errorf("knownMissing[%q] is true, but a corresponding TryBot (real or misc-compile) exists", goosArch)
		} else if _, ok := done[goosArch]; !ok && !knownMissing[goosArch] {
			t.Errorf("missing real TryBot or misc-compile TryBot for %q", goosArch)
		}
	}

	for _, port := range ports {
		goos, goarch, ok := strings.Cut(port, "/")
		if !ok {
			t.Fatalf("unexpected port %q", port)
		}
		check(goos, goarch)
	}
}

// The 'linux-arm-arm5' pseduo-port is supported by src/buildall.bash
// and tests linux/arm with GOARM=5 set. Since it's not a normal port,
// the TestTryBotsCompileAllPorts wouldn't report if the misc-compile
// TryBot that covers is accidentally removed. Check it explicitly.
func TestMiscCompileLinuxGOARM5(t *testing.T) {
	if migration.StopLegacyMiscCompileTryBots {
		t.Log("nothing to test since legacy misc-compile trybots are stopped")
		return
	}

	for _, b := range Builders {
		if !strings.HasPrefix(b.Name, "misc-compile-") {
			continue
		}
		var hasGOOS, hasGOARCH, hasGOARM bool
		for _, v := range b.env {
			if v == "GOOS=linux" {
				hasGOOS = true
				continue
			}
			if v == "GOARCH=arm" {
				hasGOARCH = true
				continue
			}
			if v == "GOARM=5" {
				hasGOARM = true
				continue
			}
		}
		if hasGOOS && hasGOARCH && hasGOARM {
			// Found it. Nothing left to do.
			return
		}
	}
	// We get here if the linux-arm-arm5 port is no longer checked by
	// a misc-compile TryBot. Report it as a failure in case the coverage
	// was removed accidentally (e.g., as part of a refactor).
	t.Errorf("no misc-compile TryBot coverage for the special 'linux-arm-arm5' pseudo-port")
}

// Test that we have longtest builders and
// that their environment configurations are okay.
func TestLongTestBuilder(t *testing.T) {
	for _, name := range []string{"linux-amd64-longtest", "linux-amd64-longtest-race"} {
		t.Run(name, func(t *testing.T) {
			long, ok := Builders[name]
			if !ok {
				t.Fatalf("we don't have a %s builder anymore, is that intentional?", name)
			}
			if !long.IsLongTest() {
				t.Errorf("the %s builder isn't a longtest builder, is that intentional?", name)
			}
			var shortDisabled bool
			for _, e := range long.Env() {
				if e == "GO_TEST_SHORT=0" {
					shortDisabled = true
				}
			}
			if !shortDisabled {
				t.Errorf("the %s builder doesn't set GO_TEST_SHORT=0, is that intentional?", name)
			}
		})
	}
}

// Test that we have race builders and
// that their environment configurations are okay.
func TestRaceBuilder(t *testing.T) {
	for _, name := range []string{"linux-amd64-race", "linux-amd64-longtest-race"} {
		t.Run(name, func(t *testing.T) {
			race, ok := Builders[name]
			if !ok {
				t.Fatalf("we don't have a %s builder anymore, is that intentional?", name)
			}
			if !race.IsRace() {
				t.Errorf("the %s builder isn't a race builder, is that intentional?", name)
			}
			if script := race.AllScript(); !strings.Contains(script, "race") {
				t.Errorf("the %s builder doesn't use race.bash or race.bat, it uses %s instead, is that intentional?", name, script)
			}
		})
	}
}

func TestHostConfigIsVM(t *testing.T) {
	testCases := []struct {
		desc   string
		config *HostConfig
		want   bool
	}{
		{
			desc: "non-ec2-vm",
			config: &HostConfig{
				VMImage:        "image-x",
				ContainerImage: "",
				IsEC2:          false,
			},
			want: true,
		},
		{
			desc: "non-ec2-container",
			config: &HostConfig{
				VMImage:        "",
				ContainerImage: "container-image-x",
				IsEC2:          false,
			},
			want: false,
		},
		{
			desc: "ec2-container",
			config: &HostConfig{
				VMImage:        "image-x",
				ContainerImage: "container-image-x",
				IsEC2:          true,
			},
			want: false,
		},
		{
			desc: "ec2-vm",
			config: &HostConfig{
				VMImage:        "image-x",
				ContainerImage: "",
				IsEC2:          true,
			},
			want: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := tc.config.IsVM(); got != tc.want {
				t.Errorf("HostConfig.IsVM() = %t; want %t", got, tc.want)
			}
		})
	}
}

func TestDefaultPlusExpBuild(t *testing.T) {
	for _, tc := range []struct {
		repo string
		want bool
	}{
		{"exp", true},
		{"build", true},
		{"anything", true},
		{"vulndb", false},
	} {
		got := defaultPlusExpBuild(tc.repo, "", "")
		if got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.repo, got, tc.want)
		}
	}
}

func TestHostsSort(t *testing.T) {
	data, err := os.ReadFile("builders.go")
	if err != nil {
		t.Fatal(err)
	}
	table := regexp.MustCompile(`(?s)\nvar Hosts =.*?\n}\n`).FindString(string(data))
	if table == "" {
		t.Fatal("cannot find Hosts table in builders.go")
	}
	m := regexp.MustCompile(`\n\t"([^"]+)":`).FindAllStringSubmatch(table, -1)
	if len(m) < 10 {
		t.Fatalf("cannot find host keys in table")
	}
	var last string
	for _, sub := range m {
		key := sub[1]
		if last > key {
			t.Errorf("Host table unsorted: %s before %s", last, key)
		}
		last = key
	}
}

func TestBuildersPortedToLUCI(t *testing.T) {
	// Check that map keys refer to builder names that exist,
	// otherwise the entry is a no-op. Mostly to catch typos.
	for name := range migration.BuildersPortedToLUCI {
		if _, ok := Builders[name]; !ok {
			t.Errorf("BuildersPortedToLUCI contains an unknown legacy builder name %v", name)
		}
	}
}

func TestHostConfigCosArchitecture(t *testing.T) {
	testCases := []struct {
		desc       string
		hostConfig HostConfig
		want       CosArch
	}{
		{"default", HostConfig{}, CosArchAMD64},
		{"amd64", HostConfig{cosArchitecture: CosArchAMD64}, CosArchAMD64},
		{"arm64", HostConfig{cosArchitecture: CosArchARM64}, CosArchARM64},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := tc.hostConfig.CosArchitecture(); got != tc.want {
				t.Errorf("HostConfig.CosArchitecture() = %+v; want %+v", got, tc.want)
			}
		})
	}
}

// listPorts lists supported Go ports
// found by running go tool dist list.
func listPorts() ([]string, error) {
	cmd := exec.Command("go", "tool", "dist", "list")
	out, err := cmd.Output()
	if err != nil {
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			out = append(out, ee.Stderr...)
		}
		return nil, fmt.Errorf("%q failed: %s\n%s", cmd, err, out)
	}
	return strings.Fields(string(out)), nil
}
