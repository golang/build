// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dashboard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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
			want: []string{
				"freebsd-amd64-12_3",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-boringcrypto",
				"linux-amd64-nounified",
				"linux-amd64-race",
				"linux-amd64-unified",
				"linux-arm-aws",
				"linux-arm64",
				"openbsd-amd64-72",
				"windows-386-2008",
				"windows-386-2012",
				"windows-amd64-2016",

				"misc-compile-darwin",
				"misc-compile-freebsd",
				"misc-compile-windows-arm",
				"misc-compile-mips",
				"misc-compile-mipsle",
				"misc-compile-netbsd",
				"misc-compile-netbsd-arm",
				"misc-compile-openbsd",
				"misc-compile-openbsd-arm",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"misc-compile-solaris",
				"misc-compile-other-1",
				"misc-compile-other-2",
				"misc-compile-go1.20",
			},
		},
		{
			repo:   "go",
			branch: "release-branch.go1.20",
			want: []string{
				"freebsd-amd64-12_3",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-boringcrypto",
				"linux-amd64-race",
				"linux-arm-aws",
				"linux-arm64",
				"openbsd-amd64-72",
				"windows-386-2008",
				"windows-386-2012",
				"windows-amd64-2016",

				"misc-compile-darwin",
				"misc-compile-freebsd",
				"misc-compile-windows-arm",
				"misc-compile-mips",
				"misc-compile-mipsle",
				"misc-compile-netbsd",
				"misc-compile-netbsd-arm",
				"misc-compile-openbsd",
				"misc-compile-openbsd-arm",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"misc-compile-solaris",
				"misc-compile-other-1",
				"misc-compile-other-2",
				"misc-compile-go1.20",

				// Include longtest builders on Go repo release branches. See issue 37827.
				"linux-386-longtest",
				"linux-amd64-longtest",
				"linux-arm64-longtest",
				"windows-amd64-longtest",
			},
		},
		{
			repo:   "go",
			branch: "release-branch.go1.18",
			want: []string{
				"freebsd-amd64-12_3",
				"js-wasm",
				"linux-386",
				"linux-amd64",
				"linux-amd64-race",
				"linux-arm-aws",
				"linux-arm64",
				"openbsd-amd64-72",
				"windows-386-2008-oldcc",
				"windows-386-2012-oldcc",
				"windows-amd64-2016-oldcc",

				"misc-compile-darwin",
				"misc-compile-freebsd",
				"misc-compile-windows-arm",
				"misc-compile-mips",
				"misc-compile-mipsle",
				"misc-compile-netbsd",
				"misc-compile-netbsd-arm",
				"misc-compile-openbsd",
				"misc-compile-openbsd-arm",
				"misc-compile-plan9",
				"misc-compile-ppc",
				"misc-compile-solaris",
				"misc-compile-other-1",
				"misc-compile-other-2",

				// Include longtest builders on Go repo release branches. See issue 37827.
				"linux-386-longtest",
				"linux-amd64-longtest",
				"windows-amd64-longtest-oldcc",
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
				"freebsd-386-13_0",
				"freebsd-amd64-12_3",
				"freebsd-amd64-13_0",
				"linux-386",
				"linux-amd64",
				"linux-amd64-boringcrypto", // GoDeps will exclude, but not in test
				"linux-amd64-race",
				"linux-arm-aws",
				"linux-arm64",
				"netbsd-amd64-9_3",
				"openbsd-386-72",
				"openbsd-amd64-72",
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
			goBranch := tt.branch // hard-code the common case for now
			got := TryBuildersForProject(tt.repo, tt.branch, goBranch)
			checkBuildersForProject(t, got, tt.want)
		})
	}
}

func checkBuildersForProject(t *testing.T, gotBuilders []*BuildConfig, want []string) {
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
			want: []string{
				"linux-amd64",
				"linux-amd64-longtest",
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

		{b("linux-loong64-3a5000", "go"), onlyPost},
		{b("linux-loong64-3a5000@go1.99", "go"), onlyPost},
		{b("linux-loong64-3a5000@go1.18", "go"), none}, // Go 1.18 doesn't support this port.
		{b("linux-loong64-3a5000", "sys"), onlyPost},
		{b("linux-loong64-3a5000@go1.99", "sys"), onlyPost},
		{b("linux-loong64-3a5000@go1.18", "sys"), none},
		{b("linux-loong64-3a5000", "net"), onlyPost},

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

		// Only test tip for js/wasm, and only for some repos:
		{b("js-wasm", "go"), both},
		{b("js-wasm", "arch"), onlyPost},
		{b("js-wasm", "crypto"), onlyPost},
		{b("js-wasm", "sys"), onlyPost},
		{b("js-wasm", "net"), onlyPost},
		{b("js-wasm", "benchmarks"), none},
		{b("js-wasm", "debug"), none},
		{b("js-wasm", "mobile"), none},
		{b("js-wasm", "perf"), none},
		{b("js-wasm", "talks"), none},
		{b("js-wasm", "tools"), none},
		{b("js-wasm", "tour"), none},
		{b("js-wasm", "website"), none},

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
		{b("windows-386-2008", "exp"), both},
		{b("windows-amd64-2016", "exp"), both},
		{b("darwin-amd64-10_14", "exp"), onlyPost},
		{b("darwin-amd64-10_15", "exp"), onlyPost},
		// ... but not on most others:
		{b("freebsd-386-12_3", "exp"), none},
		{b("freebsd-amd64-12_3", "exp"), none},
		{b("js-wasm", "exp"), none},

		// exp is experimental; it doesn't test against release branches.
		{b("linux-amd64@go1.99", "exp"), none},

		// the build repo is only really useful for linux-amd64 (where we run it),
		// and darwin-amd64 and perhaps windows-amd64 (for stuff like gomote).
		// No need for any other operating systems to use it.
		{b("linux-amd64", "build"), both},
		{b("linux-amd64-longtest", "build"), onlyPost},
		{b("windows-amd64-2016", "build"), both},
		{b("darwin-amd64-10_14", "build"), none},
		{b("darwin-amd64-10_15", "build"), onlyPost},
		{b("linux-amd64-fedora", "build"), none},
		{b("linux-amd64-clang", "build"), none},
		{b("linux-amd64-sid", "build"), none},
		{b("linux-amd64-bullseye", "build"), none},
		{b("linux-amd64-nocgo", "build"), none},
		{b("linux-386-longtest", "build"), none},

		{b("linux-amd64", "vulndb"), both},
		{b("linux-amd64-longtest", "vulndb"), onlyPost},

		{b("js-wasm", "build"), none},
		{b("android-386-emu", "build"), none},
		{b("android-amd64-emu", "build"), none},

		// Only use latest macOS for subrepos, and only amd64:
		{b("darwin-amd64-10_14", "net"), onlyPost},

		{b("darwin-amd64-10_15", "go"), onlyPost},
		{b("darwin-amd64-10_14", "go"), onlyPost},

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
		{b("linux-amd64-staticlockranking@go1.19", "go"), onlyPost},
		{b("linux-amd64-staticlockranking", "net"), none},

		{b("linux-amd64-unified", "go"), both},
		{b("linux-amd64-unified", "tools"), onlyPost},
		{b("linux-amd64-unified", "net"), none},
		{b("linux-amd64-unified@dev.unified", "go"), both},
		{b("linux-amd64-unified@dev.unified", "tools"), onlyPost},
		{b("linux-amd64-unified@dev.unified", "net"), none},

		{b("linux-amd64-nounified", "go"), both},
		{b("linux-amd64-nounified", "tools"), both},
		{b("linux-amd64-nounified", "net"), none},
		{b("linux-amd64-nounified@dev.unified", "go"), both},
		{b("linux-amd64-nounified@dev.unified", "tools"), both},
		{b("linux-amd64-nounified@dev.unified", "net"), none},
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

// TestTryBotsCompileAllPorts verifies that each port (go tool dist list)
// is covered by either a real TryBot or a misc-compile TryBot.
//
// The special pseudo-port 'linux-arm-arm5' is tested in TestMiscCompileLinuxGOARM5.
func TestTryBotsCompileAllPorts(t *testing.T) {
	out, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "tool", "dist", "list").Output()
	if err != nil {
		t.Errorf("dist list: %v", err)
	}
	ports := strings.Fields(string(out))

	// knownMissing tracks Go ports that that are known to be
	// completely missing TryBot (pre-submit) test coverage.
	//
	// All completed ports should have either a real TryBot or at least a misc-compile TryBot,
	// so this map is meant to be used to temporarily fix tests
	// when the work of adding a new port is actively underway.
	knownMissing := map[string]bool{}

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
				re, err := regexp.Compile(conf.allScriptArgs[0])
				if err != nil {
					t.Fatalf("invalid misc-compile filtering pattern for builder %q: %q",
						conf.Name, conf.allScriptArgs[0])
				}
				if re.MatchString(goosArch) {
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
		slash := strings.IndexByte(port, '/')
		if slash == -1 {
			t.Fatalf("unexpected port %q", port)
		}
		check(port[:slash], port[slash+1:])
	}
}

// The 'linux-arm-arm5' pseduo-port is supported by src/buildall.bash
// and tests linux/arm with GOARM=5 set. Since it's not a normal port,
// the TestTryBotsCompileAllPorts wouldn't report if the misc-compile
// TryBot that covers is is accidentally removed. Check it explicitly.
func TestMiscCompileLinuxGOARM5(t *testing.T) {
	var ok bool
	for _, b := range Builders {
		if !strings.HasPrefix(b.Name, "misc-compile-") {
			continue
		}
		re, err := regexp.Compile(b.allScriptArgs[0])
		if err != nil {
			t.Fatalf("invalid misc-compile filtering pattern for builder %q: %q",
				b.Name, b.allScriptArgs[0])
		}
		if re.MatchString("linux-arm-arm5") {
			ok = true
			break
		}
	}
	if !ok {
		// We get here if the linux-arm-arm5 port is no longer checked by
		// a misc-compile TryBot. Report it as a failure in case the coverage
		// was removed accidentally (e.g., as part of a refactor).
		t.Errorf("no misc-compile TryBot coverage for the special 'linux-arm-arm5' pseudo-port")
	}
}

// TestExpectedMacstadiumVMCount ensures that the right number of
// instances of macOS virtual machines are expected at MacStadium.
//
// TODO(go.dev/issue/35698): remove once the scheduler allocates VMs based on demand.
func TestExpectedMacstadiumVMCount(t *testing.T) {
	t.Skip("MacStadium turndown")
	got := 0
	for host, config := range Hosts {
		if strings.HasPrefix(host, "host-darwin-amd64-") && !strings.HasSuffix(host, "-aws") {
			got += config.ExpectNum
		}
	}
	if got != 16 {
		t.Fatalf("macstadium host count: got %d; want 16", got)
	}
}

// Test that we have longtest builders and
// that their environment configurations are okay.
func TestLongTestBuilder(t *testing.T) {
	for _, name := range []string{"linux-amd64-longtest", "linux-amd64-longtest-race"} {
		name := name
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
		name := name
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
				isEC2:          false,
			},
			want: true,
		},
		{
			desc: "non-ec2-container",
			config: &HostConfig{
				VMImage:        "",
				ContainerImage: "container-image-x",
				isEC2:          false,
			},
			want: false,
		},
		{
			desc: "ec2-container",
			config: &HostConfig{
				VMImage:        "image-x",
				ContainerImage: "container-image-x",
				isEC2:          true,
			},
			want: false,
		},
		{
			desc: "ec2-vm",
			config: &HostConfig{
				VMImage:        "image-x",
				ContainerImage: "",
				isEC2:          true,
			},
			want: true,
		},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf(tc.desc), func(t *testing.T) {
			if got := tc.config.IsVM(); got != tc.want {
				t.Errorf("HostConfig.IsVM() = %t; want %t", got, tc.want)
			}
		})
	}
}

func TestModulesEnv(t *testing.T) {
	testCases := []struct {
		desc        string
		buildConfig *BuildConfig
		repo        string
		want        []string
	}{
		{
			desc: "ec2-builder-repo-non-go",
			buildConfig: &BuildConfig{
				testHostConf: &HostConfig{
					IsReverse: false,
					isEC2:     true,
				},
			},
			repo: "bar",
			want: []string{"GOPROXY=https://proxy.golang.org"},
		},
		{
			desc: "reverse-builder-repo-non-go",
			buildConfig: &BuildConfig{
				testHostConf: &HostConfig{
					IsReverse: true,
					isEC2:     false,
				},
			},
			repo: "bar",
			want: []string{"GOPROXY=https://proxy.golang.org"},
		},
		{
			desc: "reverse-builder-repo-go",
			buildConfig: &BuildConfig{
				testHostConf: &HostConfig{
					IsReverse: true,
					isEC2:     false,
				},
			},
			repo: "go",
			want: []string{"GOPROXY=off"},
		},
		{
			desc: "builder-repo-go",
			buildConfig: &BuildConfig{
				testHostConf: &HostConfig{
					IsReverse: false,
					isEC2:     false,
				},
			},
			repo: "go",
			want: []string{"GOPROXY=off"},
		},
		{
			desc: "builder-repo-go-outbound-network-allowed",
			buildConfig: &BuildConfig{
				Name: "test-longtest",
				testHostConf: &HostConfig{
					IsReverse: false,
					isEC2:     false,
				},
			},
			repo: "go",
			want: nil,
		},
		{
			desc: "builder-repo-special-case",
			buildConfig: &BuildConfig{
				testHostConf: &HostConfig{
					IsReverse: false,
					isEC2:     false,
				},
			},
			repo: "build",
			want: []string{"GO111MODULE=on"},
		},
		{
			desc: "reverse-builder-repo-special-case",
			buildConfig: &BuildConfig{
				testHostConf: &HostConfig{
					IsReverse: true,
					isEC2:     false,
				},
			},
			repo: "build",
			want: []string{"GOPROXY=https://proxy.golang.org", "GO111MODULE=on"},
		},
		{
			desc: "builder-repo-non-special-case",
			buildConfig: &BuildConfig{
				testHostConf: &HostConfig{
					IsReverse: false,
					isEC2:     false,
				},
			},
			repo: "bar",
			want: nil,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			got := tc.buildConfig.ModulesEnv(tc.repo)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildConfig.ModulesEnv(%q) mismatch (-want, +got)\n%s", tc.repo, diff)
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

func TestWindowsCCSetup(t *testing.T) {
	// For 1.18 + 1.19 we want to see the "-oldcc" variants,
	// where for 1.20 and main branch we want the non-oldcc variants,
	// both for trybots and post-submit testing.
	tests := []struct {
		repo   string // "go", "net", etc
		branch string // of repo
		want   string
	}{
		{
			repo:   "go",
			branch: "master",
			want:   "newcc",
		},
		{
			repo:   "tools",
			branch: "master",
			want:   "newcc",
		},
		{
			repo:   "go",
			branch: "go1.20",
			want:   "newcc",
		},
		{
			repo:   "go",
			branch: "go1.19",
			want:   "oldcc",
		},
		{
			repo:   "build",
			branch: "go1.19",
			want:   "oldcc",
		},
	}

	checkWindowsBuilders := func(got []*BuildConfig, want string, repo, branch string) {
		t.Helper()
		for _, b := range got {
			bname := b.Name
			if !strings.HasPrefix(bname, "windows-amd64") &&
				!strings.HasPrefix(bname, "windows-386") {
				continue
			}
			hasOldCC := strings.Contains(bname, "oldcc")
			if want == "newcc" && hasOldCC {
				t.Errorf("got unexpected oldcc builder %q repo %s branch %s",
					bname, repo, branch)
			} else if want == "oldcc" && !hasOldCC {
				t.Errorf("got unexpected newcc builder %q repo %s branch %s",
					bname, repo, branch)
			}
		}
	}

	for i, tt := range tests {
		if tt.branch == "" || tt.repo == "" {
			t.Errorf("incomplete test entry %d", i)
			return
		}
		if tt.want != "newcc" && tt.want != "oldcc" {
			t.Errorf("incorrect 'want' field in test entry %d", i)
			return
		}
		t.Run(fmt.Sprintf("%s/%s", tt.repo, tt.branch), func(t *testing.T) {
			goBranch := tt.branch // hard-code the common case for now
			got := TryBuildersForProject(tt.repo, tt.branch, goBranch)
			checkWindowsBuilders(got, tt.want, tt.repo, tt.branch)
		})
		t.Run(fmt.Sprintf("%s/%s", tt.repo, tt.branch), func(t *testing.T) {
			goBranch := tt.branch // hard-code the common case for now
			got := buildersForProject(tt.repo, tt.branch, goBranch, (*BuildConfig).BuildsRepoPostSubmit)
			checkWindowsBuilders(got, tt.want, tt.repo, tt.branch)
		})

	}
}
