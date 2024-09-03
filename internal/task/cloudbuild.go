// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand"
	"strings"

	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/storage"
	"golang.org/x/build/internal/gcsfs"
)

type CloudBuildClient interface {
	// RunBuildTrigger runs an existing trigger in project with the given
	// substitutions.
	RunBuildTrigger(ctx context.Context, project, trigger string, substitutions map[string]string) (CloudBuild, error)
	// RunScript runs the given script under bash -eux -o pipefail in
	// ScriptProject. Outputs are collected into the build's ResultURL,
	// readable with ResultFS. The script will have the latest version of Go
	// and some version of gsutil on $PATH.
	// If gerritProject is provided, the script operates within a checkout of the
	// latest commit on the default branch of that repository.
	RunScript(ctx context.Context, script string, gerritProject string, outputs []string) (CloudBuild, error)
	// RunCustomSteps is a low-level API that provides direct control over
	// individual Cloud Build steps. It creates a random result directory
	// and provides that as a parameter to the steps function, so that it
	// may write output to it with 'gsutil cp' for accessing via ResultFS.
	// Prefer RunScript for simpler scenarios.
	// Reference: https://cloud.google.com/build/docs/build-config-file-schema
	RunCustomSteps(ctx context.Context, steps func(resultURL string) []*cloudbuildpb.BuildStep) (CloudBuild, error)
	// Completed reports whether a build has finished, returning an error if
	// it's failed. It's suitable for use with AwaitCondition.
	Completed(ctx context.Context, build CloudBuild) (detail string, completed bool, _ error)
	// ResultFS returns an FS that contains the results of the given build.
	ResultFS(ctx context.Context, build CloudBuild) (fs.FS, error)
}

type RealCloudBuildClient struct {
	BuildClient   *cloudbuild.Client
	StorageClient *storage.Client
	ScriptProject string
	ScriptAccount string
	ScratchURL    string
}

// CloudBuild represents a Cloud Build that can be queried with the status
// methods on CloudBuildClient.
type CloudBuild struct {
	Project, ID string
	ResultURL   string
}

func (c *RealCloudBuildClient) RunBuildTrigger(ctx context.Context, project, trigger string, substitutions map[string]string) (CloudBuild, error) {
	op, err := c.BuildClient.RunBuildTrigger(ctx, &cloudbuildpb.RunBuildTriggerRequest{
		ProjectId: project,
		TriggerId: trigger,
		Source: &cloudbuildpb.RepoSource{
			Substitutions: substitutions,
		},
	})
	if err != nil {
		return CloudBuild{}, err
	}
	if _, err = op.Poll(ctx); err != nil {
		return CloudBuild{}, err
	}
	meta, err := op.Metadata()
	if err != nil {
		return CloudBuild{}, err
	}
	return CloudBuild{Project: project, ID: meta.Build.Id}, nil
}

func (c *RealCloudBuildClient) RunScript(ctx context.Context, script string, gerritProject string, outputs []string) (CloudBuild, error) {
	const downloadGoScript = `#!/usr/bin/env bash
set -eux
archive=$(wget -qO - 'https://go.dev/dl/?mode=json' | grep -Eo 'go.*linux-amd64.tar.gz' | head -n 1)
wget -qO - https://go.dev/dl/${archive} | tar -xz
mv go /workspace/released_go
`
	const scriptPrefix = `#!/usr/bin/env bash
set -eux
set -o pipefail
export PATH=/workspace/released_go/bin:$PATH
`

	steps := func(resultURL string) []*cloudbuildpb.BuildStep {
		// Cloud build loses directory structure when it saves artifacts, which is
		// a problem since (e.g.) we have multiple files named go.mod in the
		// tagging tasks. It's not very complicated, so reimplement it ourselves.
		var saveOutputsScript strings.Builder
		saveOutputsScript.WriteString(scriptPrefix)
		for _, out := range outputs {
			saveOutputsScript.WriteString(fmt.Sprintf("gsutil cp %q %q\n", out, resultURL+"/"+strings.TrimPrefix(out, "./")))
		}

		var steps []*cloudbuildpb.BuildStep
		var dir string
		if gerritProject != "" {
			steps = append(steps, &cloudbuildpb.BuildStep{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"clone", "https://go.googlesource.com/" + gerritProject, "checkout"},
			})
			dir = "checkout"
		}
		steps = append(steps,
			&cloudbuildpb.BuildStep{
				Name:   "bash",
				Script: downloadGoScript,
			},
			&cloudbuildpb.BuildStep{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: scriptPrefix + script,
				Dir:    dir,
			},
			&cloudbuildpb.BuildStep{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: saveOutputsScript.String(),
				Dir:    dir,
			},
		)

		return steps
	}

	return c.RunCustomSteps(ctx, steps)
}

func (c *RealCloudBuildClient) RunCustomSteps(ctx context.Context, steps func(resultURL string) []*cloudbuildpb.BuildStep) (CloudBuild, error) {
	build := &cloudbuildpb.Build{
		Steps: steps(fmt.Sprintf("%v/script-build-%v", c.ScratchURL, rand.Int63())),
		Options: &cloudbuildpb.BuildOptions{
			MachineType: cloudbuildpb.BuildOptions_E2_HIGHCPU_8,
			Logging:     cloudbuildpb.BuildOptions_CLOUD_LOGGING_ONLY,
		},
		ServiceAccount: c.ScriptAccount,
	}
	op, err := c.BuildClient.CreateBuild(ctx, &cloudbuildpb.CreateBuildRequest{
		ProjectId: c.ScriptProject,
		Build:     build,
	})
	if err != nil {
		return CloudBuild{}, fmt.Errorf("creating build: %w", err)
	}
	if _, err = op.Poll(ctx); err != nil {
		return CloudBuild{}, fmt.Errorf("polling: %w", err)
	}
	meta, err := op.Metadata()
	if err != nil {
		return CloudBuild{}, fmt.Errorf("reading metadata: %w", err)
	}
	return CloudBuild{Project: c.ScriptProject, ID: meta.Build.Id}, nil
}

func (c *RealCloudBuildClient) Completed(ctx context.Context, build CloudBuild) (string, bool, error) {
	b, err := c.BuildClient.GetBuild(ctx, &cloudbuildpb.GetBuildRequest{
		ProjectId: build.Project,
		Id:        build.ID,
	})
	if err != nil {
		return "", false, err
	}
	if b.FinishTime == nil {
		return "", false, nil
	}
	if b.Status != cloudbuildpb.Build_SUCCESS {
		return "", false, fmt.Errorf("build %q failed, see %v: %v", build.ID, build.ResultURL, b.FailureInfo)
	}
	return b.StatusDetail, true, nil
}

func (c *RealCloudBuildClient) ResultFS(ctx context.Context, build CloudBuild) (fs.FS, error) {
	return gcsfs.FromURL(ctx, c.StorageClient, build.ResultURL)
}
