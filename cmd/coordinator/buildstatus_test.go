// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/internal/migration"
)

// TestParseOutputAndHeader tests header parsing by parseOutputAndHeader.
func TestParseOutputAndHeader(t *testing.T) {
	for _, tc := range []struct {
		name         string
		input        []byte
		wantMetadata string
		wantHeader   string
		wantOut      []byte
	}{
		{
			name: "standard",
			input: []byte(`
XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantMetadata: "",
			wantHeader:   "##### Testing packages.",
			wantOut: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "header only",
			input: []byte(`
XXXBANNERXXX:Testing packages.
`),
			wantMetadata: "",
			wantHeader:   "##### Testing packages.",
			wantOut:      []byte(``),
		},
		{
			name: "header only missing trailing newline",
			input: []byte(`
XXXBANNERXXX:Testing packages.`),
			wantMetadata: "",
			wantHeader:   "##### Testing packages.",
			wantOut:      []byte(``),
		},
		{
			name: "no banner",
			input: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantMetadata: "",
			wantHeader:   "",
			wantOut: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "no newline",
			input: []byte(`XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantMetadata: "",
			wantHeader:   "",
			wantOut: []byte(`XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "wrong banner",
			input: []byte(`
##### Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantMetadata: "",
			wantHeader:   "",
			wantOut: []byte(`
##### Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "metadata",
			input: []byte(`
XXXBANNERXXX:Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz

XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantMetadata: `##### Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz`,
			wantHeader: "##### Testing packages.",
			wantOut: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "metadata missing separator newline",
			input: []byte(`
XXXBANNERXXX:Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz
XXXBANNERXXX:Testing packages.
ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
			wantMetadata: `##### Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz`,
			wantHeader: "##### Testing packages.",
			wantOut: []byte(`ok	archive/tar	0.015s
ok	archive/zip	0.406s
ok	bufio	0.075s
`),
		},
		{
			name: "metadata missing second banner",
			input: []byte(`
XXXBANNERXXX:Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz
`),
			wantMetadata: "",
			wantHeader:   "",
			wantOut: []byte(`
XXXBANNERXXX:Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz
`),
		},
		{
			name: "metadata missing body",
			input: []byte(`
XXXBANNERXXX:Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz

XXXBANNERXXX:Testing packages.
`),
			wantMetadata: `##### Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz`,
			wantHeader: "##### Testing packages.",
			wantOut:    []byte(``),
		},
		{
			name: "metadata missing body and newline",
			input: []byte(`
XXXBANNERXXX:Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz

XXXBANNERXXX:Testing packages.`),
			wantMetadata: `##### Test execution environment.
# GOARCH: amd64
# CPU: Intel(R) Xeon(R) W-2135 CPU @ 3.70GHz`,
			wantHeader: "##### Testing packages.",
			wantOut:    []byte(``),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotMetadata, gotHeader, gotOut := parseOutputAndHeader(tc.input)
			if gotMetadata != tc.wantMetadata {
				t.Errorf("parseOutputAndBanner(%q) got metadata %q want metadata %q", string(tc.input), gotMetadata, tc.wantMetadata)
			}
			if gotHeader != tc.wantHeader {
				t.Errorf("parseOutputAndBanner(%q) got header %q want header %q", string(tc.input), gotHeader, tc.wantHeader)
			}
			if string(gotOut) != string(tc.wantOut) {
				t.Errorf("parseOutputAndBanner(%q) got out %q want out %q", string(tc.input), string(gotOut), string(tc.wantOut))
			}
		})
	}
}

func TestModulesEnv(t *testing.T) {
	// modulesEnv looks at pool.NewGCEConfiguration().BuildEnv().IsProd for
	// special behavior in dev mode. Temporarily override the environment
	// to force testing of the prod configuration.
	old := pool.NewGCEConfiguration().BuildEnv()
	defer pool.NewGCEConfiguration().SetBuildEnv(old)
	pool.NewGCEConfiguration().SetBuildEnv(&buildenv.Environment{
		IsProd: true,
	})

	// In testing we never initialize
	// pool.NewGCEConfiguration().GKENodeHostname(), so we get this odd
	// concatenation back.
	gkeModuleProxy := "http://:30157"
	if migration.StopInternalModuleProxy {
		// If the internal module proxy is stopped, expect the Go module mirror to be used instead.
		gkeModuleProxy = "https://proxy.golang.org"
	}

	testCases := []struct {
		desc string
		st   *buildStatus
		want []string
	}{
		{
			desc: "ec2-builder-repo-non-go",
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{SubName: "bar"},
				conf: &dashboard.BuildConfig{
					TestHostConf: &dashboard.HostConfig{
						IsReverse: false,
						IsEC2:     true,
					},
				},
			},
			want: []string{"GOPROXY=https://proxy.golang.org"},
		},
		{
			desc: "builder-repo-non-go",
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{SubName: "bar"},
				conf: &dashboard.BuildConfig{
					TestHostConf: &dashboard.HostConfig{
						IsReverse: false,
						IsEC2:     false,
					},
				},
			},
			want: []string{"GOPROXY=" + gkeModuleProxy},
		},
		{
			desc: "reverse-builder-repo-non-go",
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{SubName: "bar"},
				conf: &dashboard.BuildConfig{
					TestHostConf: &dashboard.HostConfig{
						IsReverse: true,
						IsEC2:     false,
					},
				},
			},
			want: []string{"GOPROXY=https://proxy.golang.org"},
		},
		{
			desc: "reverse-builder-repo-go",
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{SubName: ""}, // go
				conf: &dashboard.BuildConfig{
					TestHostConf: &dashboard.HostConfig{
						IsReverse: true,
						IsEC2:     false,
					},
				},
			},
			want: []string{"GOPROXY=off"},
		},
		{
			desc: "builder-repo-go",
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{SubName: ""}, // go
				conf: &dashboard.BuildConfig{
					TestHostConf: &dashboard.HostConfig{
						IsReverse: false,
						IsEC2:     false,
					},
				},
			},
			want: []string{"GOPROXY=off"},
		},
		{
			desc: "builder-repo-go-outbound-network-allowed",
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{SubName: ""}, // go
				conf: &dashboard.BuildConfig{
					Name: "test-longtest",
					TestHostConf: &dashboard.HostConfig{
						IsReverse: false,
						IsEC2:     false,
					},
				},
			},
			want: []string{"GOPROXY=" + gkeModuleProxy},
		},
		{
			desc: "reverse-builder-repo-go-outbound-network-allowed",
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{SubName: ""}, // go
				conf: &dashboard.BuildConfig{
					Name: "test-longtest",
					TestHostConf: &dashboard.HostConfig{
						IsReverse: true,
						IsEC2:     false,
					},
				},
			},
			want: []string{"GOPROXY=https://proxy.golang.org"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			want := tc.want
			if migration.StopInternalModuleProxy && slices.Contains(want, "GOPROXY=https://proxy.golang.org") {
				want = append(want, "GO_DISABLE_OUTBOUND_NETWORK=0")
			}
			got := tc.st.modulesEnv()
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("buildStatus.modulesEnv() mismatch (-want, +got)\n%s", diff)
			}
		})
	}
}

// Test that go120DistTestNames remaps both 1.20 (old) and 1.21 (new)
// dist test names to old, and doesn't forget the original name (raw).
func TestGo120DistTestNames(t *testing.T) {
	for _, tc := range [...]struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "old to old",
			in:   "go_test:archive/tar go_test:cmd/go api reboot test:0_2 test:1_2",
			want: "go_test:archive/tar go_test:cmd/go api reboot test:0_2 test:1_2",
		},
		{
			name: "new to old",
			in:   "        archive/tar         cmd/go cmd/api:check cmd/internal/bootstrap_test cmd/internal/testdir:0_2 cmd/internal/testdir:1_2",
			want: "go_test:archive/tar go_test:cmd/go     api                  reboot                        test:0_2                 test:1_2",
		},
		{
			name: "more special cases",
			in:   "crypto/x509:nolibgcc fmt:moved_goroot flag:race net:race os:race",
			want: "nolibgcc:crypto/x509     moved_goroot      race     race    race",
		},
		{
			name: "unhandled special case",
			in:   "something:something",
			want: "something:something",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := go120DistTestNames(strings.Fields(tc.in))
			var want []distTestName
			for _, old := range strings.Fields(tc.want) {
				want = append(want, distTestName{Old: old})
			}
			for i, raw := range strings.Fields(tc.in) {
				want[i].Raw = raw
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("go120DistTestNames mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
