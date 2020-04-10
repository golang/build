// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"bytes"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/coordinator/pool"
	"golang.org/x/build/maintner/maintnerd/apipb"
)

type Seconds float64

func (s Seconds) Duration() time.Duration {
	return time.Duration(float64(s) * float64(time.Second))
}

var fixedTestDuration = map[string]Seconds{
	"go_test:a": 1,
	"go_test:b": 1.5,
	"go_test:c": 2,
	"go_test:d": 2.50,
	"go_test:e": 3,
	"go_test:f": 3.5,
	"go_test:g": 4,
	"go_test:h": 4.5,
	"go_test:i": 5,
	"go_test:j": 5.5,
	"go_test:k": 6.5,
}

func TestPartitionGoTests(t *testing.T) {
	var in []string
	for name := range fixedTestDuration {
		in = append(in, name)
	}
	testDuration := func(builder, testName string) time.Duration {
		if s, ok := fixedTestDuration[testName]; ok {
			return s.Duration()
		}
		return 3 * time.Second
	}
	sets := partitionGoTests(testDuration, "", in)
	want := [][]string{
		{"go_test:a", "go_test:b", "go_test:c", "go_test:d", "go_test:e"},
		{"go_test:f", "go_test:g"},
		{"go_test:h", "go_test:i"},
		{"go_test:j"},
		{"go_test:k"},
	}
	if !reflect.DeepEqual(sets, want) {
		t.Errorf(" got: %v\nwant: %v", sets, want)
	}
}

func TestTryStatusJSON(t *testing.T) {
	testCases := []struct {
		desc   string
		method string
		ts     *trySet
		tss    trySetState
		status int
		body   string
	}{
		{
			"pre-flight CORS header",
			"OPTIONS",
			nil,
			trySetState{},
			http.StatusOK,
			``,
		},
		{
			"nil trySet",
			"GET",
			nil,
			trySetState{},
			http.StatusNotFound,
			`{"success":false,"error":"TryBot result not found (already done, invalid, or not yet discovered from Gerrit). Check Gerrit for results."}` + "\n",
		},
		{"non-nil trySet",
			"GET",
			&trySet{
				tryKey: tryKey{
					Commit:   "deadbeef",
					ChangeID: "Ifoo",
				},
			},
			trySetState{
				builds: []*buildStatus{
					{
						BuilderRev: buildgo.BuilderRev{Name: "linux"},
						startTime:  time.Time{}.Add(24 * time.Hour),
						done:       time.Time{}.Add(48 * time.Hour),
						succeeded:  true,
					},
					{
						BuilderRev: buildgo.BuilderRev{Name: "macOS"},
						startTime:  time.Time{}.Add(24 * time.Hour),
					},
				},
			},
			http.StatusOK,
			`{"success":true,"payload":{"changeId":"Ifoo","commit":"deadbeef","builds":[{"name":"linux","startTime":"0001-01-02T00:00:00Z","done":true,"succeeded":true},{"name":"macOS","startTime":"0001-01-02T00:00:00Z","done":false,"succeeded":false}]}}` + "\n"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			w := httptest.NewRecorder()
			r, err := http.NewRequest(tc.method, "", nil)
			if err != nil {
				t.Fatalf("could not create http.Request: %v", err)
			}
			serveTryStatusJSON(w, r, tc.ts, tc.tss)
			resp := w.Result()
			hdr := "Access-Control-Allow-Origin"
			if got, want := resp.Header.Get(hdr), "*"; got != want {
				t.Errorf("unexpected %q header: got %q; want %q", hdr, got, want)
			}
			if got, want := resp.StatusCode, tc.status; got != want {
				t.Errorf("response status code: got %d; want %d", got, want)
			}
			defer resp.Body.Close()
			b, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ioutil.ReadAll: %v", err)
			}
			if got, want := string(b), tc.body; got != want {
				t.Errorf("body: got\n%v\nwant\n%v", got, want)
			}
		})
	}
}

func TestStagingClusterBuilders(t *testing.T) {
	// Just test that it doesn't panic:
	stagingClusterBuilders()
}

// Test that trybot on release-branch.go1.N branch of a golang.org/x repo
// uses the Go revision from Go repository's release-branch.go1.N branch.
// See golang.org/issue/28891.
func TestIssue28891(t *testing.T) {
	testingKnobSkipBuilds = true

	work := &apipb.GerritTryWorkItem{ // Roughly based on https://go-review.googlesource.com/c/tools/+/150577/1.
		Project:   "tools",
		Branch:    "release-branch.go1.11",
		ChangeId:  "Ice719ab807ce3922b885a800ac873cdbf165a8f7",
		Commit:    "9d66f1bfdbed72f546df963194a19d56180c4ce7",
		GoCommit:  []string{"a2e79571a9d3dbe3cf10dcaeb1f9c01732219869", "e39e43d7349555501080133bb426f1ead4b3ef97", "f5ff72d62301c4e9d0a78167fab5914ca12919bd"},
		GoBranch:  []string{"master", "release-branch.go1.11", "release-branch.go1.10"},
		GoVersion: []*apipb.MajorMinor{{1, 12}, {1, 11}, {1, 10}},
	}
	ts := newTrySet(work)
	if len(ts.builds) == 0 {
		t.Fatal("no builders in try set, want at least 1")
	}
	for i, bs := range ts.builds {
		const go111Revision = "e39e43d7349555501080133bb426f1ead4b3ef97"
		if bs.BuilderRev.Rev != go111Revision {
			t.Errorf("build[%d]: %s: x/tools on release-branch.go1.11 branch should be tested with Go 1.11, but isn't", i, bs.NameAndBranch())
		}
	}
}

// tests that we don't test Go 1.10 for the build repo
func TestNewTrySetBuildRepoGo110(t *testing.T) {
	testingKnobSkipBuilds = true

	work := &apipb.GerritTryWorkItem{
		Project:   "build",
		Branch:    "master",
		ChangeId:  "I6f05da2186b38dc8056081252563a82c50f0ce05",
		Commit:    "a62e6a3ab11cc9cc2d9e22a50025dd33fc35d22f",
		GoCommit:  []string{"a2e79571a9d3dbe3cf10dcaeb1f9c01732219869", "e39e43d7349555501080133bb426f1ead4b3ef97", "f5ff72d62301c4e9d0a78167fab5914ca12919bd"},
		GoBranch:  []string{"master", "release-branch.go1.11", "release-branch.go1.10"},
		GoVersion: []*apipb.MajorMinor{{1, 12}, {1, 11}, {1, 10}},
	}
	ts := newTrySet(work)
	for i, bs := range ts.builds {
		v := bs.NameAndBranch()
		if strings.Contains(v, "Go 1.10.x") {
			t.Errorf("unexpected builder: %v", v)
		}
		t.Logf("build[%d]: %s", i, v)
	}
}

// Tests that TryBots run on branches of the x/ repositories, other than
// "master" and "release-branch.go1.N". See golang.org/issue/37512.
func TestXRepoBranches(t *testing.T) {
	testingKnobSkipBuilds = true

	work := &apipb.GerritTryWorkItem{
		Project:   "tools",
		Branch:    "gopls-release-branch.0.4",
		ChangeId:  "Ica799fcf117bf607c0c59f41b08a78552339dc53",
		Commit:    "6af4ce83c61d0f3e616b410b53b51982798c4d73",
		GoVersion: []*apipb.MajorMinor{{1, 15}},
		GoCommit:  []string{"74d6de03fd7db2c6faa7794620a9bcf0c4f018f2"},
		GoBranch:  []string{"master"},
	}
	ts := newTrySet(work)
	for i, bs := range ts.builds {
		v := bs.NameAndBranch()
		t.Logf("build[%d]: %s", i, v)
	}
	if len(ts.builds) < 3 {
		t.Fatalf("expected at least 3 builders, got %v", len(ts.builds))
	}
}

func TestFindWork(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	gce := pool.NewGCEConfiguration()
	buildEnv := gce.BuildEnv()
	defer func(old *buildenv.Environment) { gce.SetBuildEnv(old) }(buildEnv)
	gce.SetBuildEnv(buildenv.Production)
	defer func() { buildgo.TestHookSnapshotExists = nil }()
	buildgo.TestHookSnapshotExists = func(br *buildgo.BuilderRev) bool {
		if strings.Contains(br.Name, "android") {
			log.Printf("snapshot check for %+v", br)
		}
		return false
	}

	addWorkTestHook = func(work buildgo.BuilderRev, d *commitDetail) {
		t.Logf("Got: %v, %+v", work, d)
	}
	defer func() { addWorkTestHook = nil }()

	err := findWork()
	if err != nil {
		t.Error(err)
	}
}

func TestBuildersJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	handleBuilders(rec, httptest.NewRequest("GET", "https://farmer.tld/builders?mode=json", nil))
	res := rec.Result()
	if res.Header.Get("Content-Type") != "application/json" || res.StatusCode != 200 {
		var buf bytes.Buffer
		res.Write(&buf)
		t.Error(buf.String())
	}
}

func mustConf(t *testing.T, name string) *dashboard.BuildConfig {
	conf, ok := dashboard.Builders[name]
	if !ok {
		t.Fatalf("unknown builder %q", name)
	}
	return conf
}

func TestSlowBotsFromComments(t *testing.T) {
	work := &apipb.GerritTryWorkItem{
		Version: 2,
		TryMessage: []*apipb.TryVoteMessage{
			{
				Version: 1,
				Message: "ios",
			},
			{
				Version: 2,
				Message: "arm64, mac aix ",
			},
			{
				Version: 1,
				Message: "aix",
			},
		},
	}
	slowBots := slowBotsFromComments(work)
	var got []string
	for _, bc := range slowBots {
		got = append(got, bc.Name)
	}
	want := []string{"aix-ppc64", "darwin-amd64-10_14", "linux-arm64-packet"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch:\n got: %q\nwant: %q\n", got, want)
	}
}

func TestSubreposFromComments(t *testing.T) {
	work := &apipb.GerritTryWorkItem{
		Version: 2,
		TryMessage: []*apipb.TryVoteMessage{
			{
				Version: 2,
				Message: "x/build, x/sync x/tools, x/sync",
			},
		},
	}
	got := xReposFromComments(work)
	want := map[string]bool{
		"build": true,
		"sync":  true,
		"tools": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch:\n got: %v\nwant: %v\n", got, want)
	}
}

func TestBuildStatusFormat(t *testing.T) {
	for i, tt := range []struct {
		st   *buildStatus
		want string
	}{
		{
			st: &buildStatus{
				trySet: &trySet{
					tryKey: tryKey{
						Project: "go",
					},
				},
				BuilderRev: buildgo.BuilderRev{
					Name:    "linux-amd64",
					SubName: "tools",
				},
			},
			want: "(x/tools) linux-amd64",
		},
		{
			st: &buildStatus{
				trySet: &trySet{
					tryKey: tryKey{
						Project: "tools",
					},
				},
				BuilderRev: buildgo.BuilderRev{
					Name:    "linux-amd64",
					SubName: "tools",
				},
				goBranch: "release-branch.go1.15",
			},
			want: "linux-amd64 (Go 1.15.x)",
		},
		{
			st: &buildStatus{
				trySet: &trySet{
					tryKey: tryKey{
						Project: "go",
					},
				},
				BuilderRev: buildgo.BuilderRev{
					Name:    "linux-amd64",
					SubName: "tools",
				},
			},
			want: "(x/tools) linux-amd64",
		},
		{
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{
					Name: "darwin-amd64-10_14",
				},
			},
			want: "darwin-amd64-10_14",
		},
		{
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{
					Name: "darwin-amd64-10_14",
				},
				goBranch: "release-branch.go1.15",
			},
			want: "darwin-amd64-10_14 (Go 1.15.x)",
		},
	} {
		if got := tt.st.NameAndBranch(); got != tt.want {
			t.Errorf("%d: NameAndBranch = %q; want %q", i, got, tt.want)
		}
	}
}
