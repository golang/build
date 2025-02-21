// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task_test

import (
	"context"
	"flag"
	"runtime"
	"testing"

	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/task"
	wf "golang.org/x/build/internal/workflow"
)

func TestUpdateX509Bundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("doesn't work on Windows until a fix for https://github.com/rsc/rf/issues/30 lands")
	}
	if testing.Short() {
		t.Skip("not running test that uses internet in short mode")
	}
	t.Run("new content", func(t *testing.T) { testUpdateX509Bundle(t, true) })
	t.Run("old content", func(t *testing.T) { testUpdateX509Bundle(t, false) })
}

func testUpdateX509Bundle(t *testing.T, newContent bool) {
	var command string
	if newContent {
		command = "cp gen.go new.txt"
	} else {
		command = "echo no-op change"
	}
	crypto := task.NewFakeRepo(t, "crypto")
	crypto.Commit(map[string]string{
		"go.mod": "module golang.org/x/crypto\n",
		"x509roots/gen.go": `//go:build generate
//go:generate ` + command + `
package p`,
	})

	fakeGerrit := task.NewFakeGerrit(t, crypto)
	tasks := task.BundleNSSRootsTask{
		Gerrit:     fakeGerrit,
		CloudBuild: task.NewFakeCloudBuild(t, fakeGerrit, "", nil),
	}
	ctx := &wf.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}
	got, err := tasks.UpdateBundle(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var want string
	if newContent {
		want = "created a change at https://go.dev/cl/1"
	} else {
		want = "created no change, regenerating produced no diff"
	}
	if got != want {
		t.Errorf("UpdateBundle result is unexpected: got %q, want %q", got, want)
	}
}

var flagUpdateX509BundleProject = flag.String("update-x509-bundle-project", "", "GCP project for Cloud Build for TestUpdateX509BundleLive")

func TestUpdateX509BundleLive(t *testing.T) {
	if !testing.Verbose() || flag.Lookup("test.run").Value.String() != "^TestUpdateX509BundleLive$" {
		t.Skip("not running a live test requiring manual verification if not explicitly requested with go test -v -run=^TestUpdateX509BundleLive$")
	}
	if *flagUpdateX509BundleProject == "" {
		t.Fatalf("-update-x509-bundle-project flag must be set to a non-empty GCP project")
	}

	cbClient, err := cloudbuild.NewClient(context.Background())
	if err != nil {
		t.Fatalf("could not connect to Cloud Build: %v", err)
	}
	tasks := task.BundleNSSRootsTask{
		Gerrit: &task.RealGerritClient{
			Gitiles: "https://go.googlesource.com",
			Client:  gerrit.NewClient("https://go-review.googlesource.com", gerrit.NoAuth),
		},
		CloudBuild: &task.RealCloudBuildClient{
			BuildClient:   cbClient,
			ScriptProject: *flagUpdateX509BundleProject,
		},
	}
	ctx := &wf.TaskContext{
		Context: context.Background(),
		Logger:  &testLogger{t, ""},
	}
	result, err := tasks.UpdateBundle(ctx, nil)
	if err != nil {
		t.Fatal("UpdateBundle:", err)
	}
	t.Logf("successfully %s", result)
}
