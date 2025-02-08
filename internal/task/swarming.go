// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"
	"strings"

	"go.chromium.org/luci/swarming/client/swarming"
	apipb "go.chromium.org/luci/swarming/proto/api_v2"
)

type SwarmingClient interface {
	// RunTask runs script on a machine running port with env set.
	// The script will have the latest version of Go and some version of gsutil
	// on $PATH. To facilitate Windows/Unix compatibility, . will be at the end
	// of $PATH.
	RunTask(ctx context.Context, dims map[string]string, script string, env map[string]string) (string, error)
	// Completed reports whether a build has finished, returning an error if
	// it's failed. It's suitable for use with AwaitCondition.
	Completed(ctx context.Context, id string) (string, bool, error)
}

type RealSwarmingClient struct {
	SwarmingClient                           swarming.Client
	SwarmingURL, ServiceAccount, Realm, Pool string
}

func (c *RealSwarmingClient) RunTask(ctx context.Context, dims map[string]string, script string, env map[string]string) (string, error) {
	cipdPlatform, ok := dims["cipd_platform"]
	if !ok {
		return "", fmt.Errorf("must specify cipd_platform in dims: %v", dims)
	}
	shell := []string{"bash", "-eux"}
	if strings.HasPrefix(cipdPlatform, "windows") {
		shell = []string{"cmd", "/c"}
	}

	req := &apipb.NewTaskRequest{
		Name:           "relui task",
		Priority:       20,
		User:           "relui",
		ServiceAccount: c.ServiceAccount,
		Realm:          c.Realm,

		TaskSlices: []*apipb.TaskSlice{
			{
				Properties: &apipb.TaskProperties{
					EnvPrefixes: []*apipb.StringListPair{
						{Key: "PATH", Value: []string{"tools/bin"}},
					},
					Env:     []*apipb.StringPair{},
					Command: append(append([]string{"luci-auth", "context"}, shell...), script),
					CipdInput: &apipb.CipdInput{
						Packages: []*apipb.CipdPackage{
							{Path: "tools/bin", PackageName: "infra/tools/luci-auth/" + cipdPlatform, Version: "latest"},
							{Path: "tools", PackageName: "golang/bootstrap-go/" + cipdPlatform, Version: "latest"},
							{Path: "tools", PackageName: "infra/3pp/tools/gcloud/" + cipdPlatform, Version: "latest"},
							{Path: "tools", PackageName: "infra/3pp/tools/cpython3/" + cipdPlatform, Version: "latest"},
						},
					},
					Dimensions: []*apipb.StringPair{
						{Key: "pool", Value: c.Pool},
					},
					ExecutionTimeoutSecs: 600,
				},
				ExpirationSecs: 3 * 60 * 60,
			},
		},
	}
	for k, v := range dims {
		req.TaskSlices[0].Properties.Dimensions = append(req.TaskSlices[0].Properties.Dimensions, &apipb.StringPair{Key: k, Value: v})
	}
	for k, v := range env {
		req.TaskSlices[0].Properties.Env = append(req.TaskSlices[0].Properties.Env, &apipb.StringPair{Key: k, Value: v})
	}
	task, err := c.SwarmingClient.NewTask(ctx, req)
	if err != nil {
		return "", err
	}
	return task.TaskId, nil
}

func (c *RealSwarmingClient) Completed(ctx context.Context, id string) (string, bool, error) {
	result, err := c.SwarmingClient.TaskResult(ctx, id, &swarming.TaskResultFields{WithPerf: false})
	if err != nil {
		return "", false, err
	}
	if result.State == apipb.TaskState_RUNNING || result.State == apipb.TaskState_PENDING {
		return "", false, nil
	}
	if result.State != apipb.TaskState_COMPLETED || result.ExitCode != 0 {
		return "", true, fmt.Errorf("build failed with state %v and exit code %v, see %v/task?id=%v", result.State, result.ExitCode, c.SwarmingURL, id)
	}
	return "", true, nil
}
