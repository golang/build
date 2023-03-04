// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

var flagRunVersionTest = flag.Bool("run-version-test", false, "run version test, which will submit CLs to go.googlesource.com/scratch. Must have a Gerrit cookie in gitcookies.")

func TestGetNextVersionLive(t *testing.T) {
	if !*flagRunVersionTest {
		t.Skip("Not enabled by flags")
	}

	cl := gerrit.NewClient("https://go-review.googlesource.com", gerrit.GitCookiesAuth())
	tasks := &VersionTasks{
		Gerrit:    &RealGerritClient{Client: cl},
		GoProject: "go",
	}
	ctx := &workflow.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}

	versions := map[ReleaseKind]string{}
	for kind := ReleaseKind(0); kind <= KindPrevMinor; kind++ {
		var err error
		versions[kind], err = tasks.GetNextVersion(ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
	}
	// It's hard to check correctness automatically.
	t.Errorf("manually verify results: %#v", versions)
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
	versions := map[ReleaseKind]string{}
	for kind := ReleaseKind(1); kind <= KindPrevMinor; kind++ {
		var err error
		versions[kind], err = tasks.GetNextVersion(ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
	}
	want := map[ReleaseKind]string{
		KindBeta:         "go1.5beta2",
		KindRC:           "go1.5rc2",
		KindMajor:        "go1.5.0",
		KindCurrentMinor: "go1.4.2",
		KindPrevMinor:    "go1.3.4",
	}
	if diff := cmp.Diff(want, versions); diff != "" {
		t.Fatalf("GetNextVersions mismatch (-want +got):\n%s", diff)
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
