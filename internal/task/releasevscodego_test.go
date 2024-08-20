// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"testing"

	"golang.org/x/build/internal/workflow"
)

func TestNextPrereleaseVersion(t *testing.T) {
	tests := []struct {
		name         string
		existingTags []string
		versionRule  string
		wantVersion  string
	}{
		{
			name:         "v0.44.0 have not released, have no release candidate",
			existingTags: []string{"v0.44.0", "v0.43.0", "v0.42.0"},
			versionRule:  "next minor",
			wantVersion:  "v0.46.0-rc.1",
		},
		{
			name:         "v0.44.0 have not released but already have two release candidate",
			existingTags: []string{"v0.44.0-rc.1", "v0.44.0-rc.2", "v0.43.0", "v0.42.0"},
			versionRule:  "next minor",
			wantVersion:  "v0.44.0-rc.3",
		},
		{
			name:         "v0.44.3 have not released, have no release candidate",
			existingTags: []string{"v0.44.2-rc.1", "v0.44.2", "v0.44.1", "v0.44.1-rc.1"},
			versionRule:  "next patch",
			wantVersion:  "v0.44.3-rc.1",
		},
		{
			name:         "v0.44.3 have not released but already have one release candidate",
			existingTags: []string{"v0.44.3-rc.1", "v0.44.2", "v0.44.2-rc.1", "v0.44.1", "v0.44.1-rc.1"},
			versionRule:  "next patch",
			wantVersion:  "v0.44.3-rc.2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vscodego := NewFakeRepo(t, "vscode-go")
			commit := vscodego.Commit(map[string]string{
				"go.mod": "module github.com/golang/vscode-go\n",
				"go.sum": "\n",
			})

			for _, tag := range tc.existingTags {
				vscodego.Tag(tag, commit)
			}

			gerrit := NewFakeGerrit(t, vscodego)

			tasks := &ReleaseVSCodeGoTasks{
				Gerrit: gerrit,
			}

			got, err := tasks.nextPrereleaseVersion(&workflow.TaskContext{Context: context.Background(), Logger: &testLogger{t, ""}}, tc.versionRule)
			if err != nil {
				t.Fatal(err)
			}

			want, ok := parseSemver(tc.wantVersion)
			if !ok {
				t.Fatalf("failed to parse the want version: %q", tc.wantVersion)
			}

			if want != got {
				t.Errorf("nextPrereleaseVersion(%q) = %v but want %v", tc.versionRule, got, want)
			}
		})
	}
}
