// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux

package main

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/maintner/maintnerd/apipb"
)

func TestPartitionGoTests(t *testing.T) {
	var in []string
	for name := range fixedTestDuration {
		in = append(in, name)
	}
	sets := partitionGoTests("", in)
	for i, set := range sets {
		t.Logf("set %d = \"-run=^(%s)$\"", i, strings.Join(set, "|"))
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
