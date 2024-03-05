// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/build/internal/workflow"
)

func TestVSCodeGoReleaseTask_buildVSCGO(t *testing.T) {
	mustHaveShell(t)

	tests := []struct {
		name           string
		revision       string
		wantBuildError bool
	}{
		{
			name:     "success",
			revision: "release",
		},
		{
			name:           "broken build",
			revision:       "master", // broken
			wantBuildError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := NewFakeRepo(t, "vscode-go")
			t1 := repo.Commit(map[string]string{
				"go.mod":        "module github.com/golang/vscode-go\n",
				"go.sum":        "\n",
				"vscgo/main.go": "package main\nfunc main() { println(\"v1\") }\n",
			})
			repo.Branch("release", t1)

			_ = repo.Commit(map[string]string{
				"vscgo/broken.go": "package broken\n", // broken at master
			})

			gerrit := NewFakeGerrit(t, repo)
			scratchDir := t.TempDir()

			// fakeGo will record the invocation command in the fake output.
			const fakeGo = `#!/bin/bash -eu

function find_output_value() {
	# returns -o flag value.
	while [[ $# -gt 0 ]]; do
	  case "$1" in
		-o)
		  if [[ $# -gt 1 ]]; then
			echo "$2"
			shift 2
		  else
			echo "Error: No argument provided after '-o'"
			exit 1
		  fi
		  ;;
		*)
		  shift
		  ;;
	  esac
	done
}

case "$1" in
"build")
    if [[ -f "vscgo/broken.go" ]]; then
	  echo build broken
	  exit 1
	fi
    out=$(find_output_value "$@")
	echo "GOOS=${GOOS} GOARCH=${GOARCH} $0 $@" > "${out}"
	exit 0
	;;
*)
	echo unexpected command $@
	;;
esac
`

			releaseTask := &VSCodeGoReleaseTask{
				CloudBuild: NewFakeCloudBuild(t, gerrit, "vscode-go", nil, fakeGo),
				ScratchFS:  &ScratchFS{BaseURL: "file://" + scratchDir},
				Revision:   test.revision,
			}

			wd := releaseTask.NewDefinition()
			w, err := workflow.Start(wd, map[string]interface{}{
				vscgoVersionParam.Name: "v0.0.0",
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			outputs, err := w.Run(ctx, &verboseListener{t: t, onStall: cancel})
			if err != nil {
				if test.wantBuildError {
					return // expected build error.
				}
				t.Fatal(err)
			}
			buildArtifacts, ok := outputs["build artifacts"].([]goBuildArtifact)
			if !ok {
				t.Fatalf("unexpected build artifacts: %T (%+v): ", outputs, outputs)
			}
			var want []goBuildArtifact
			for _, p := range vscgoPlatforms {
				want = append(want, goBuildArtifact{
					Platform: p.Platform,
				})
			}
			sortSlice := cmpopts.SortSlices(func(x, y goBuildArtifact) bool { return x.Platform < y.Platform })
			ignoreFilename := cmpopts.IgnoreFields(goBuildArtifact{}, "Filename")
			if diff := cmp.Diff(buildArtifacts, want, sortSlice, ignoreFilename); diff != "" {
				t.Fatal(diff)
			}
			// HACK: Reading directly from the scratch filesystem.
			// XXX: is there a better way??
			envMap := make(map[string][]string)
			for _, p := range vscgoPlatforms {
				envMap[p.Platform] = p.Env
			}
			for _, a := range buildArtifacts {
				data, err := os.ReadFile(filepath.Join(scratchDir, w.ID.String(), a.Filename))
				if err != nil {
					t.Errorf("%v: %v", a.Platform, err)
				}
				envs := envMap[a.Platform]
				if !bytes.Contains(data, []byte(strings.Join(envs, " "))) {
					t.Errorf("%v: unexpected contents: %s", a.Platform, data)
				}

			}
		})
	}
}
