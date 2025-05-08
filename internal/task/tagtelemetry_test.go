// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"testing"

	"golang.org/x/build/internal/workflow"
)

func TestTagTelemetry(t *testing.T) {
	if testing.Short() {
		t.Skip("not running test that uses internet in short mode")
	}
	mustHaveShell(t)

	tests := []struct {
		label           string
		tags            []string
		initialConfig   string
		masterConfig    string
		generatedConfig string
		wantCommit      bool
		wantTag         string
	}{
		{
			label:           "no existing tag",
			tags:            []string{"v0.1.0"}, // note: not a tag of the config submodule
			initialConfig:   "{}",
			masterConfig:    "{}",
			generatedConfig: "{  }",
			wantCommit:      true, // we should commit, even if we don't tag
			wantTag:         "",   // only start tagging once the config module has been tagged at least once
		},
		{
			label:           "generated tag",
			tags:            []string{"v0.6.0", "config/v0.0.1"},
			initialConfig:   "{}",
			masterConfig:    "{}",
			generatedConfig: "{  }",
			wantCommit:      true,
			wantTag:         "config/v0.1.0",
		},
		{
			label:           "master tag",
			tags:            []string{"config/v0.1.0", "config/v0.2.0", "config/v0.2.1"},
			initialConfig:   "{}",
			masterConfig:    `{  }`,
			generatedConfig: `{  }`,
			wantCommit:      false, // no change since master
			wantTag:         "config/v0.3.0",
		},
	}

	for _, test := range tests {
		t.Run(test.label, func(t *testing.T) {
			// Gerrit setup: create an initial commit with the initialConfig
			// contents, all tags at that initial commit, and then a master commit
			// with the masterConfig contents.
			telemetry := NewFakeRepo(t, "telemetry")
			t1 := telemetry.Commit(map[string]string{
				"go.mod":                     "module golang.org/x/telemetry\n",
				"go.sum":                     "\n",
				"config/go.mod":              "module golang.org/x/telemetry/config\n",
				"config/go.sum":              "\n",
				"config/config.json":         test.initialConfig,
				"internal/configgen/main.go": "//go:generate cp gen.out ../../config/config.json\npackage main\n",
				"internal/configgen/gen.out": test.generatedConfig,
			})
			for _, tag := range test.tags {
				telemetry.Tag(tag, t1)
			}
			t2 := telemetry.Commit(map[string]string{
				"a/a.go":             "package a", // an arbitrary change to ensure the commit is nonempty
				"config/config.json": test.masterConfig,
			})
			gerrit := NewFakeGerrit(t, telemetry)

			tasks := &TagTelemetryTasks{
				Gerrit:     gerrit,
				CloudBuild: NewFakeCloudBuild(t, gerrit, "", nil),
			}

			wd := tasks.NewDefinition()
			w, err := workflow.Start(wd, map[string]interface{}{
				reviewersParam.Name: []string(nil),
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			outputs, err := w.Run(ctx, &verboseListener{t: t})
			if err != nil {
				t.Fatal(err)
			}

			// Verify that the master branch was updated as expected.
			gotMaster, err := gerrit.ReadBranchHead(ctx, "telemetry", "master")
			if err != nil {
				t.Fatal(err)
			}
			if test.wantCommit != (gotMaster != t2) {
				t.Errorf("telemetry@master = %s (from %s), but want commit: %t", gotMaster, t2, test.wantCommit)
			}

			// Verify that we created the expected tag.
			if got := outputs["tag"]; got != test.wantTag {
				t.Errorf("Output: got \"tag\" %q, want %q", got, test.wantTag)
			}
			finalConfig, err := gerrit.ReadFile(ctx, "telemetry", gotMaster, "config/config.json")
			if err != nil {
				t.Fatal(err)
			}

			// Finally, check that the resulting config state in master is correct.
			// No matter what, the final state of master should match the generated
			// state.
			if got, want := string(finalConfig), test.generatedConfig; got != want {
				t.Errorf("Final config = %q, want %q", got, want)
			}
		})
	}
}
