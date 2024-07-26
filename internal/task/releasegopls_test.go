// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/internal/workflow"
)

func TestPossibleGoplsVersions(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want []string
	}{
		{
			name: "any one version tag should have three possible next versions",
			tags: []string{"gopls/v1.2.3"},
			want: []string{"v1.2.4", "v1.3.0", "v2.0.0"},
		},
		{
			name: "1.2.0 should be skipped because 1.2.3 already exist",
			tags: []string{"gopls/v1.2.3", "gopls/v1.1.0"},
			want: []string{"v1.1.1", "v1.2.4", "v1.3.0", "v2.0.0"},
		},
		{
			name: "2.0.0 should be skipped because 2.1.3 already exist",
			tags: []string{"gopls/v1.2.3", "gopls/v2.1.3"},
			want: []string{"v1.2.4", "v1.3.0", "v2.1.4", "v2.2.0", "v3.0.0"},
		},
		{
			name: "1.2.0 is still consider valid version because there is no 1.2.X",
			tags: []string{"gopls/v1.1.3", "gopls/v1.3.2", "gopls/v2.1.2"},
			want: []string{"v1.1.4", "v1.2.0", "v1.3.3", "v1.4.0", "v2.1.3", "v2.2.0", "v3.0.0"},
		},
		{
			name: "2.0.0 is still consider valid version because there is no 2.X.X",
			tags: []string{"gopls/v1.2.3", "gopls/v3.1.2"},
			want: []string{"v1.2.4", "v1.3.0", "v2.0.0", "v3.1.3", "v3.2.0", "v4.0.0"},
		},
		{
			name: "pre-release version tag should not have any effect on the next version",
			tags: []string{"gopls/v0.16.1-pre.1", "gopls/v0.16.1-pre.2", "gopls/v0.16.0"},
			want: []string{"v0.16.1", "v0.17.0", "v1.0.0"},
		},
		{
			name: "other unrelated tag should not have any effect on the next version",
			tags: []string{"v0.9.2", "v0.9.3", "v0.23.0", "gopls/v0.16.0"},
			want: []string{"v0.16.1", "v0.17.0", "v1.0.0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := NewFakeRepo(t, "tools")
			commit := tools.Commit(map[string]string{
				"go.mod": "module golang.org/x/tools\n",
				"go.sum": "\n",
			})

			for _, tag := range tc.tags {
				tools.Tag(tag, commit)
			}

			gerrit := NewFakeGerrit(t, tools)

			tasks := &ReleaseGoplsTasks{
				Gerrit: gerrit,
			}

			got, err := tasks.possibleGoplsVersions(&workflow.TaskContext{Context: context.Background()})
			if err != nil {
				t.Fatalf("possibleGoplsVersions() should not return error, but return %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("possibleGoplsVersions() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
