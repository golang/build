// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

var flagRunVersionTest = flag.Bool("run-version-test", false, "run version test, which will submit CLs to go.googlesource.com/scratch. Must have a Gerrit cookie in gitcookies.")

func TestGetNextVersionLive(t *testing.T) {
	if !testing.Verbose() || flag.Lookup("test.run").Value.String() != "^TestGetNextVersionLive$" {
		t.Skip("not running a live test requiring manual verification if not explicitly requested with go test -v -run=^TestGetNextVersionLive$")
	}

	cl := gerrit.NewClient("https://go-review.googlesource.com", nil)
	tasks := &VersionTasks{
		Gerrit:    &RealGerritClient{Client: cl},
		GoProject: "go",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}

	var out strings.Builder
	currentMajor, _, err := tasks.GetCurrentMajor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range [...]struct {
		major int
		kind  ReleaseKind
	}{
		{currentMajor + 1, KindMajor}, // Next major release.
		{currentMajor + 1, KindRC},    // Next RC.
		{currentMajor + 1, KindBeta},  // Next beta.
		{currentMajor, KindMinor},     // Current minor only.
		{currentMajor - 1, KindMinor}, // Previous minor only.
	} {
		v, err := tasks.GetNextVersion(ctx, tc.major, tc.kind)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "tasks.GetNextVersion(ctx, %d, %#v) = %q\n", tc.major, tc.kind, v)
	}
	// It's hard to check correctness automatically.
	t.Logf("manually verify results:\n%s", &out)
}

func TestGetNextVersion(t *testing.T) {
	tasks := &VersionTasks{
		Gerrit: &versionsClient{
			tags: []string{
				"go1.3beta1", "go1.3beta2", "go1.3rc1", "go1.3", "go1.3.1", "go1.3.2", "go1.3.3",
				"go1.4beta1", "go1.4beta2", "go1.4rc1", "go1.4.0", "go1.4.1",
				"go1.5beta1", "go1.5rc1",
			},
		},
		GoProject: "go",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}
	for _, tc := range [...]struct {
		name  string
		major int
		kind  ReleaseKind
		want  string
	}{
		{major: 5, kind: KindBeta, want: "go1.5beta2", name: "next beta"},
		{major: 5, kind: KindRC, want: "go1.5rc2", name: "next RC"},
		{major: 5, kind: KindMajor, want: "go1.5.0", name: "next major"},
		{major: 4, kind: KindMinor, want: "go1.4.2", name: "next current minor"},
		{major: 3, kind: KindMinor, want: "go1.3.4", name: "next previous minor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tasks.GetNextVersion(ctx, tc.major, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetDevelVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test that uses internet in short mode")
	}

	cl := gerrit.NewClient("https://go-review.googlesource.com", nil)
	tasks := &VersionTasks{
		Gerrit:    &RealGerritClient{Client: cl},
		GoProject: "go",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}
	got, err := tasks.GetDevelVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got < 22 {
		t.Errorf("GetDevelVersion: got %d, want 22 or higher", got)
	}
}

type versionsClient struct {
	tags []string
	GerritClient
}

func (c *versionsClient) ListTags(ctx context.Context, project string) ([]string, error) {
	return c.tags, nil
}

func TestVersion(t *testing.T) {
	if !*flagRunVersionTest {
		t.Skip("Not enabled by flags")
	}
	cl := gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth())
	tasks := &VersionTasks{
		Gerrit:    &RealGerritClient{Client: cl},
		GoProject: "scratch",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}

	changeID, err := tasks.CreateAutoSubmitVersionCL(ctx, "master", "go1.2.3", nil, "VERSION file content")
	if err != nil {
		t.Fatal(err)
	}
	_, err = tasks.AwaitCL(ctx, changeID, "")
	if strings.Contains(err.Error(), "trybots failed") {
		t.Logf("Trybots failed, as they usually do: %v. Abandoning CL and ending test.", err)
		if err := cl.AbandonChange(ctx, changeID, "test is done"); err != nil {
			t.Fatal(err)
		}
		return
	}

	changeID, err = tasks.Gerrit.CreateAutoSubmitChange(ctx, gerrit.ChangeInput{
		Project: "scratch",
		Branch:  "master",
		Subject: "Clean up VERSION",
	}, nil, map[string]string{"VERSION": ""})
	if err != nil {
		t.Fatalf("cleaning up VERSION: %v", err)
	}
	if _, err := tasks.AwaitCL(ctx, changeID, ""); err != nil {
		t.Fatalf("cleaning up VERSION: %v", err)
	}
}
