// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/build/cmd/coordinator/internal/lucipoll"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/maintner/maintnerd/apipb"
	"golang.org/x/build/types"
	"google.golang.org/grpc"
)

type fakeMaintner struct {
	resp *apipb.DashboardResponse
}

func (f *fakeMaintner) GetDashboard(ctx context.Context, in *apipb.DashboardRequest, opts ...grpc.CallOption) (*apipb.DashboardResponse, error) {
	return f.resp, nil
}

func TestHandlerServeHTTP(t *testing.T) {
	fm := &fakeMaintner{
		resp: &apipb.DashboardResponse{Commits: []*apipb.DashCommit{
			{
				Title:         "x/build/cmd/coordinator: implement dashboard",
				Commit:        "752029e171d535b0dd4ff7bbad5ad0275a3969a8",
				CommitTimeSec: 1257894000,
				AuthorName:    "Gopherbot",
				AuthorEmail:   "gopherbot@example.com",
			},
		}},
	}
	dh := &Handler{
		Maintner: fm,
		memoryResults: map[string][]string{
			"752029e171d535b0dd4ff7bbad5ad0275a3969a8": {"linux-amd64-longtest|true|SomeLog|752029e171d535b0dd4ff7bbad5ad0275a3969a8"},
		},
	}
	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()

	dh.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHandlerCommits(t *testing.T) {
	fm := &fakeMaintner{
		resp: &apipb.DashboardResponse{Commits: []*apipb.DashCommit{
			{
				Title:         "x/build/cmd/coordinator: implement dashboard",
				Commit:        "752029e171d535b0dd4ff7bbad5ad0275a3969a8",
				CommitTimeSec: 1257894000,
				AuthorName:    "Gopherbot",
				AuthorEmail:   "gopherbot@example.com",
			},
		}},
	}
	dh := &Handler{
		Maintner: fm,
		memoryResults: map[string][]string{
			"752029e171d535b0dd4ff7bbad5ad0275a3969a8": {"test-builder|true|SomeLog|752029e171d535b0dd4ff7bbad5ad0275a3969a8"},
		},
	}
	want := []*commit{
		{
			Desc:       "x/build/cmd/coordinator: implement dashboard",
			Hash:       "752029e171d535b0dd4ff7bbad5ad0275a3969a8",
			Time:       time.Unix(1257894000, 0).Format("02 Jan 15:04"),
			User:       "Gopherbot <gopherbot@example.com>",
			ResultData: []string{"test-builder|true|SomeLog|752029e171d535b0dd4ff7bbad5ad0275a3969a8"},
		},
	}
	got := dh.commits(context.Background(), lucipoll.Snapshot{})

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("dh.Commits() mismatch (-want +got):\n%s", diff)
	}
}

func TestHandlerGetBuilders(t *testing.T) {
	dh := &Handler{}
	builders := map[string]*dashboard.BuildConfig{
		"linux-amd64-testfile": {
			Name:             "linux-amd64-testfile",
			HostType:         "this-is-a-test-file",
			Notes:            "",
			MinimumGoVersion: types.MajorMinor{},
		},
		"linux-386-testfile": {
			Name:             "linux-386-testfile",
			HostType:         "this-is-a-test-file",
			Notes:            "",
			MinimumGoVersion: types.MajorMinor{},
		},
		"darwin-amd64-testfile": {
			Name:             "darwin-amd64-testfile",
			HostType:         "this-is-a-test-file",
			Notes:            "",
			MinimumGoVersion: types.MajorMinor{},
		},
		"android-386-testfile": {
			Name:             "android-386-testfile",
			HostType:         "this-is-a-test-file",
			Notes:            "",
			MinimumGoVersion: types.MajorMinor{},
		},
	}
	want := []*builder{
		{
			OS: "darwin",
			Archs: []*arch{
				{
					os: "darwin", Arch: "amd64",
					Name: "darwin-amd64-testfile",
					Tag:  "testfile",
				},
			},
		},
		{
			OS: "linux",
			Archs: []*arch{
				{
					os: "linux", Arch: "386",
					Name: "linux-386-testfile",
					Tag:  "testfile",
				},
				{
					os: "linux", Arch: "amd64",
					Name: "linux-amd64-testfile",
					Tag:  "testfile",
				},
			},
		},
		{
			OS: "android",
			Archs: []*arch{
				{
					os: "android", Arch: "386",
					Name: "android-386-testfile",
					Tag:  "testfile",
				},
			},
		},
	}
	got := dh.getBuilders(builders, lucipoll.Snapshot{})

	if diff := cmp.Diff(want, got, cmpopts.EquateComparable(arch{})); diff != "" {
		t.Errorf("dh.getBuilders() mismatch (-want +got):\n%s", diff)
	}
}

func TestArchFirstClass(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{
			name: "linux-amd64-longtest",
			want: true,
		},
		{
			name: "linux-buzz-longtest",
			want: false,
		},
		{
			name: "linux-amd64",
			want: true,
		},
		{
			name: "linux",
			want: false,
		},
	}
	for _, c := range cases {
		segs := strings.SplitN(c.name, "-", 3)
		if len(segs) == 1 {
			segs = append(segs, "")
		}
		a := &arch{os: segs[0], Arch: segs[1], Name: c.name}
		if a.FirstClass() != c.want {
			t.Errorf("%+v.FirstClass() = %v, wanted %v", a, a.FirstClass(), c.want)
		}
	}
}

func TestCommitResultForBuilder(t *testing.T) {
	c := &commit{
		Desc:       "x/build/cmd/coordinator: implement dashboard",
		Hash:       "752029e171d535b0dd4ff7bbad5ad0275a3969a8",
		Time:       "10 Nov 18:00",
		User:       "Gopherbot <gopherbot@example.com>",
		ResultData: []string{"test-builder|true|SomeLog|752029e171d535b0dd4ff7bbad5ad0275a3969a8"},
	}
	want := &result{
		OK:      true,
		LogHash: "SomeLog",
	}
	got := c.ResultForBuilder("test-builder")

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("c.ResultForBuilder(%q) mismatch (-want +got):\n%s", "test-builder", diff)
	}
}
