// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/gerrit"
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
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("io.ReadAll: %v", err)
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
	cleanup := dashboard.TestingKnobForceEnableLinuxAMD64()
	defer func() { testingKnobSkipBuilds = false; cleanup() }()

	work := &apipb.GerritTryWorkItem{ // Based on what maintapi's GoFindTryWork does for x/net CL 258478.
		Project:   "net",
		Branch:    "release-branch.go1.15",
		ChangeId:  "I546597cedf3715e6617babcb3b62140bf1857a27",
		Commit:    "a5fa9d4b7c91aa1c3fecbeb6358ec1127b910dd6",
		GoCommit:  []string{"72ccabc99449b2cb5bb1438eb90244d55f7b02f5"},
		GoBranch:  []string{"release-branch.go1.15"},
		GoVersion: []*apipb.MajorMinor{{Major: 1, Minor: 15}},
	}
	ts := newTrySet(work)
	if len(ts.builds) == 0 {
		t.Fatal("no builders in try set, want at least 1")
	}
	for i, bs := range ts.builds {
		const go115Revision = "72ccabc99449b2cb5bb1438eb90244d55f7b02f5"
		if bs.BuilderRev.Rev != go115Revision {
			t.Errorf("build[%d]: %s: x/net on release-branch.go1.15 branch should be tested with Go 1.15, but isn't", i, bs.NameAndBranch())
		}
	}
}

// Test that trybot on release-branch.go1.N-{suffix} branch of a golang.org/x repo
// uses the Go revision from Go repository's release-branch.go1.N branch.
// See golang.org/issue/42127.
func TestIssue42127(t *testing.T) {
	testingKnobSkipBuilds = true
	cleanup := dashboard.TestingKnobForceEnableLinuxAMD64()
	defer func() { testingKnobSkipBuilds = false; cleanup() }()

	work := &apipb.GerritTryWorkItem{ // Based on what maintapi's GoFindTryWork does for x/net CL 264058.
		Project:   "net",
		Branch:    "release-branch.go1.15-bundle",
		ChangeId:  "I546597cedf3715e6617babcb3b62140bf1857a27",
		Commit:    "abf26a14a65b111d492067f407f32455c5b1048c",
		GoCommit:  []string{"72ccabc99449b2cb5bb1438eb90244d55f7b02f5"},
		GoBranch:  []string{"release-branch.go1.15"},
		GoVersion: []*apipb.MajorMinor{{Major: 1, Minor: 15}},
	}
	ts := newTrySet(work)
	if len(ts.builds) == 0 {
		t.Fatal("no builders in try set, want at least 1")
	}
	for i, bs := range ts.builds {
		const go115Revision = "72ccabc99449b2cb5bb1438eb90244d55f7b02f5"
		if bs.BuilderRev.Rev != go115Revision {
			t.Errorf("build[%d]: %s: x/net on release-branch.go1.15-bundle branch should be tested with Go 1.15, but isn't", i, bs.NameAndBranch())
		}
	}
}

// Tests that TryBots run on branches of the x/ repositories, other than
// "master" and "release-branch.go1.N". See golang.org/issue/37512.
func TestXRepoBranches(t *testing.T) {
	testingKnobSkipBuilds = true
	cleanup := dashboard.TestingKnobForceEnableLinuxAMD64()
	defer func() { testingKnobSkipBuilds = false; cleanup() }()

	work := &apipb.GerritTryWorkItem{ // Based on what maintapi's GoFindTryWork does for x/tools CL 227356.
		Project:   "tools",
		Branch:    "gopls-release-branch.0.4",
		ChangeId:  "Ica799fcf117bf607c0c59f41b08a78552339dc53",
		Commit:    "13af72af5ccdfe6f1e75b57b02cfde3bb0a77a76",
		GoCommit:  []string{"9995c6b50aa55c1cc1236d1d688929df512dad53"},
		GoBranch:  []string{"master"},
		GoVersion: []*apipb.MajorMinor{{Major: 1, Minor: 17}},
	}
	ts := newTrySet(work)
	for i, bs := range ts.builds {
		v := bs.NameAndBranch()
		t.Logf("build[%d]: %s", i, v)
	}
	if len(ts.builds) == 0 {
		t.Fatal("no builders in try set, want at least 1")
	}
}

// Test that when there are multiple SlowBot requests on the same patch set,
// the latest request is used. See golang.org/issue/42084.
func TestIssue42084(t *testing.T) {
	testingKnobSkipBuilds = true
	defer func() { testingKnobSkipBuilds = false }()

	work := &apipb.GerritTryWorkItem{ // Based on what maintapi's GoFindTryWork does for CL 324763. TryMessage is set later.
		Project:   "go",
		Branch:    "master",
		ChangeId:  "I023d5208374f867552ba68b45011f7990159868f",
		Commit:    "dd38fd80c3667f891dbe06bd1d8ed153c2e208da",
		Version:   1,
		GoCommit:  []string{"9995c6b50aa55c1cc1236d1d688929df512dad53"},
		GoBranch:  []string{"master"},
		GoVersion: []*apipb.MajorMinor{{Major: 1, Minor: 17}},
	}

	// First, determine builds without try messages. Our target SlowBot shouldn't be included.
	ts := newTrySet(work)
	hasLinuxArmBuilder := false
	for _, bs := range ts.builds {
		v := bs.NameAndBranch()
		if v == "linux-arm" {
			hasLinuxArmBuilder = true
		}
	}
	if hasLinuxArmBuilder {
		// This test relies on linux-arm builder not being a default
		// TryBot to provide coverage for issue 42084. If the build policy
		// changes, need to pick another builder to use in this test.
		t.Fatal("linux-arm builder was included even without TRY= message")
	}

	// Next, add try messages, and check that the SlowBot is now included.
	work.TryMessage = []*apipb.TryVoteMessage{
		{Message: "linux", AuthorId: 1234, Version: 1},
		{Message: "linux-arm", AuthorId: 1234, Version: 1},
	}
	ts = newTrySet(work)
	hasLinuxArmBuilder = false
	for i, bs := range ts.builds {
		v := bs.NameAndBranch()
		t.Logf("build[%d]: %s", i, v)
		if v == "linux-arm-aws" {
			hasLinuxArmBuilder = true
		}
	}
	if !hasLinuxArmBuilder {
		t.Error("linux-arm SlowBot was not included")
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

	addWorkTestHook = func(work buildgo.BuilderRev, d commitDetail) {
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
				Message: "arm64, darwin aix ",
			},
			{
				Version: 1,
				Message: "aix",
			},
		},
	}
	slowBots, invalidSlowBots := slowBotsFromComments(work)
	var got []string
	for _, bc := range slowBots {
		got = append(got, bc.Name)
	}
	want := []string{"aix-ppc64", "darwin-amd64-13", "linux-arm64"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch:\n got: %q\nwant: %q\n", got, want)
	}

	if len(invalidSlowBots) > 0 {
		t.Errorf("mismatch invalidSlowBots:\n got: %d\nwant: 0", len(invalidSlowBots))
	}
}

func TestSubreposFromComments(t *testing.T) {
	work := &apipb.GerritTryWorkItem{
		Version: 2,
		TryMessage: []*apipb.TryVoteMessage{
			{
				Version: 2,
				Message: "x/build, x/sync x/tools, x/sync, x/tools@freebsd-amd64-race",
			},
		},
	}
	got := xReposFromComments(work)
	want := map[xRepoAndBuilder]bool{
		{"build", ""}:                   true,
		{"sync", ""}:                    true,
		{"tools", ""}:                   true,
		{"tools", "freebsd-amd64-race"}: true,
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
				commitDetail: commitDetail{
					RevBranch: "master",
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
				commitDetail: commitDetail{
					RevBranch: "release-branch.go1.15",
				},
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
				commitDetail: commitDetail{
					RevBranch: "master",
				},
			},
			want: "(x/tools) linux-amd64",
		},
		{
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{
					Name: "darwin-amd64-13",
				},
				commitDetail: commitDetail{
					RevBranch: "master",
				},
			},
			want: "darwin-amd64-13",
		},
		{
			st: &buildStatus{
				BuilderRev: buildgo.BuilderRev{
					Name: "darwin-amd64-13",
				},
				commitDetail: commitDetail{
					RevBranch: "release-branch.go1.15",
				},
			},
			want: "darwin-amd64-13 (Go 1.15.x)",
		},
	} {
		if got := tt.st.NameAndBranch(); got != tt.want {
			t.Errorf("%d: NameAndBranch = %q; want %q", i, got, tt.want)
		}
	}
}

// listPatchSetThreadsResponse is the response to
// https://go-review.googlesource.com/changes/go~master~I92400996cb051ab30e99bfffafd91ff32a1e7087/comments
var listPatchSetThreadsResponse = []byte(`)]}'
{"/PATCHSET_LEVEL":[{"author":{"_account_id":5976,"name":"Go Bot","email":"gobot@golang.org","tags":["SERVICE_USER"]},"tag":"autogenerated:trybots~beginning","change_message_id":"af128c803aa192eb5c191c1567713f5b123d3adb","unresolved":true,"patch_set":1,"id":"aaf7aa39_658707c2","updated":"2021-04-27 18:20:09.000000000","message":"SlowBots beginning. Status page: https://farmer.golang.org/try?commit\u003d39ad506d","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"},{"author":{"_account_id":5976,"name":"Go Bot","email":"gobot@golang.org","tags":["SERVICE_USER"]},"tag":"autogenerated:trybots~beginning","change_message_id":"a00ee30c652a61afeb5ba7657e5823b2d56159ac","unresolved":true,"patch_set":1,"id":"cb0c9011_d26d6550","updated":"2021-04-27 07:10:41.000000000","message":"SlowBots beginning. Status page: https://farmer.golang.org/try?commit\u003d39ad506d","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"},{"author":{"_account_id":6365,"name":"Bryan C. Mills","email":"bcmills@google.com"},"change_message_id":"c3dc9da7c814efe691876649d8b1acd086d34921","unresolved":false,"patch_set":1,"id":"da1249e7_bc007148","updated":"2021-04-27 07:10:22.000000000","message":"TRY\u003dlongtest","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"},{"author":{"_account_id":6365,"name":"Bryan C. Mills","email":"bcmills@google.com"},"change_message_id":"1908c5ae7cd3fa1adc7579bfb4fa0798a33dafd2","unresolved":false,"patch_set":1,"id":"043375d0_558208b0","in_reply_to":"50a54b3c_f95f3567","updated":"2021-04-27 18:32:56.000000000","message":"#41863","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"},{"author":{"_account_id":6365,"name":"Bryan C. Mills","email":"bcmills@google.com"},"change_message_id":"061f7a6231e09027e96b7c81093b32db9cd6a6f3","unresolved":false,"patch_set":1,"id":"49f73b27_b0261bc8","in_reply_to":"aaf7aa39_658707c2","updated":"2021-04-27 19:17:53.000000000","message":"Ack","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"},{"author":{"_account_id":5976,"name":"Go Bot","email":"gobot@golang.org","tags":["SERVICE_USER"]},"tag":"autogenerated:trybots~failed","change_message_id":"d7e2ff4f58b281bc3120ad79a9c5bf7c87c0ec1b","unresolved":true,"patch_set":1,"id":"50a54b3c_f95f3567","in_reply_to":"c3e462db_b5e1efca","updated":"2021-04-27 07:22:38.000000000","message":"1 of 26 SlowBots failed.\nFailed on linux-arm64-aws: https://storage.googleapis.com/go-build-log/39ad506d/linux-arm64-aws_5dc1efb9.log\n\nConsult https://build.golang.org/ to see whether they are new failures. Keep in mind that TryBots currently test *exactly* your git commit, without rebasing. If your commit\u0027s git parent is old, the failure might\u0027ve already been fixed.\n\nSlowBot builds that ran:\n* linux-amd64-longtest\n","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"},{"author":{"_account_id":5976,"name":"Go Bot","email":"gobot@golang.org","tags":["SERVICE_USER"]},"tag":"autogenerated:trybots~happy","change_message_id":"9ed90c9e9b43e3c2c4d2b6ab8e8a121686d5da88","unresolved":false,"patch_set":1,"id":"13677404_911c1149","in_reply_to":"c3e462db_b5e1efca","updated":"2021-04-27 18:46:54.000000000","message":"SlowBots are happy.\n\nSlowBot builds that ran:\n* linux-amd64-longtest\n","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"},{"author":{"_account_id":5976,"name":"Go Bot","email":"gobot@golang.org","tags":["SERVICE_USER"]},"tag":"autogenerated:trybots~progress","change_message_id":"ab963b29aa95907e30e9f6a51ef1f080807bc8ab","unresolved":true,"patch_set":1,"id":"c3e462db_b5e1efca","in_reply_to":"cb0c9011_d26d6550","updated":"2021-04-27 07:18:11.000000000","message":"Build is still in progress... Status page: https://farmer.golang.org/try?commit\u003d39ad506d\nFailed on linux-arm64-aws: https://storage.googleapis.com/go-build-log/39ad506d/linux-arm64-aws_5dc1efb9.log\nOther builds still in progress; subsequent failure notices suppressed until final report.\n\nConsult https://build.golang.org/ to see whether they are new failures. Keep in mind that TryBots currently test *exactly* your git commit, without rebasing. If your commit\u0027s git parent is old, the failure might\u0027ve already been fixed.\n","commit_id":"39ad506d874d4711015184f52585b4215c9b84cc"}]}
`)

func TestListPatchSetThreads(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(200)
		w.Write(listPatchSetThreadsResponse)
	}))
	defer s.Close()
	gerritClient := gerrit.NewClient(s.URL, gerrit.NoAuth)
	threads, err := listPatchSetThreads(gerritClient, "go~master~I92400996cb051ab30e99bfffafd91ff32a1e7087")
	if err != nil {
		t.Fatal(err)
	}
	var mostRecentTryBotThread string
	for _, tr := range threads {
		if tr.unresolved {
			t.Errorf("thread %s is unresolved", tr.root.ID)
		}
		if tr.root.Tag == tryBotsTag("beginning") {
			mostRecentTryBotThread = tr.root.ID
		}
		if tr.root != tr.thread[0] {
			t.Errorf("the root is not the first comment in thread")
		}
	}
	if mostRecentTryBotThread != "aaf7aa39_658707c2" {
		t.Errorf("wrong most recent TryBot thread: got %s, want %s", mostRecentTryBotThread, "aaf7aa39_658707c2")
	}
}

func TestInvalidSlowBots(t *testing.T) {
	work := &apipb.GerritTryWorkItem{
		Version: 2,
		TryMessage: []*apipb.TryVoteMessage{
			{
				Version: 1,
				Message: "aix, linux-mipps, amd64, freeebsd",
			},
		},
	}
	slowBots, invalidSlowBots := slowBotsFromComments(work)
	var got []string
	for _, bc := range slowBots {
		got = append(got, bc.Name)
	}
	want := []string{"aix-ppc64", "linux-amd64"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch:\n got: %q\nwant: %q\n", got, want)
	}

	wantInvalid := []string{"linux-mipps", "freeebsd"}
	if !reflect.DeepEqual(invalidSlowBots, wantInvalid) {
		t.Errorf("mismatch:\n got: %q\nwant: %q\n", invalidSlowBots, wantInvalid)
	}
}
