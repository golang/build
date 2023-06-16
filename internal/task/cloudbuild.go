// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"

	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
)

type CloudBuildClient interface {
	// RunBuildTrigger runs an existing trigger in project with the given substitutions.
	RunBuildTrigger(ctx context.Context, project, trigger string, substitutions map[string]string) (buildID string, _ error)
	// Completed reports whether a build has finished, returning an error if it's failed.
	// It's suitable for use with AwaitCondition.
	Completed(ctx context.Context, project, buildID string) (detail string, completed bool, _ error)
}

type RealCloudBuildClient struct {
	Client *cloudbuild.Client
}

func (c *RealCloudBuildClient) RunBuildTrigger(ctx context.Context, project, trigger string, substitutions map[string]string) (string, error) {
	op, err := c.Client.RunBuildTrigger(ctx, &cloudbuildpb.RunBuildTriggerRequest{
		ProjectId: project,
		TriggerId: trigger,
		Source: &cloudbuildpb.RepoSource{
			Substitutions: substitutions,
		},
	})
	if err != nil {
		return "", err
	}
	if _, err = op.Poll(ctx); err != nil {
		return "", err
	}
	meta, err := op.Metadata()
	if err != nil {
		return "", err
	}
	return meta.Build.Id, nil
}

func (c *RealCloudBuildClient) Completed(ctx context.Context, project, buildID string) (string, bool, error) {
	build, err := c.Client.GetBuild(ctx, &cloudbuildpb.GetBuildRequest{
		ProjectId: project,
		Id:        buildID,
	})
	if err != nil {
		return "", false, err
	}
	if build.FinishTime == nil {
		return "", false, nil
	}
	if build.Status != cloudbuildpb.Build_SUCCESS {
		return "", false, fmt.Errorf("build %q failed: %v", buildID, build.FailureInfo)
	}
	return build.StatusDetail, true, nil
}
