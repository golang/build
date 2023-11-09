// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"

	pb "go.chromium.org/luci/buildbucket/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type BuildBucketClient interface {
	// ListBuilders lists all the builder names in bucket.
	ListBuilders(ctx context.Context, bucket string) ([]string, error)
	// RunBuild runs a builder at commit with properties and returns its ID.
	RunBuild(ctx context.Context, bucket, builder string, commit *pb.GitilesCommit, properties map[string]*structpb.Value) (int64, error)
	// Completed reports whether a build has finished, returning an error if
	// it's failed. It's suitable for use with AwaitCondition.
	Completed(ctx context.Context, id int64) (string, bool, error)
}

type RealBuildBucketClient struct {
	BuildersClient pb.BuildersClient
	BuildsClient   pb.BuildsClient
}

func (c *RealBuildBucketClient) ListBuilders(ctx context.Context, bucket string) ([]string, error) {
	resp, err := c.BuildersClient.ListBuilders(ctx, &pb.ListBuildersRequest{
		Project:  "golang",
		Bucket:   bucket,
		PageSize: 1000,
	})
	if err != nil {
		return nil, err
	}
	if resp.NextPageToken != "" {
		return nil, fmt.Errorf("page size to ListBuilders insufficient")
	}
	var builders []string
	for _, b := range resp.Builders {
		builders = append(builders, b.Id.Builder)
	}
	return builders, nil
}

func (c *RealBuildBucketClient) RunBuild(ctx context.Context, bucket, builder string, commit *pb.GitilesCommit, properties map[string]*structpb.Value) (int64, error) {
	req := &pb.ScheduleBuildRequest{
		Builder:  &pb.BuilderID{Project: "golang", Bucket: bucket, Builder: builder},
		Priority: 20,
		Properties: &structpb.Struct{
			Fields: properties,
		},
		GitilesCommit: commit,
	}
	build, err := c.BuildsClient.ScheduleBuild(ctx, req)
	if err != nil {
		return 0, err
	}
	return build.Id, err
}

func (c *RealBuildBucketClient) Completed(ctx context.Context, id int64) (string, bool, error) {
	build, err := c.BuildsClient.GetBuildStatus(ctx, &pb.GetBuildStatusRequest{
		Id: id,
	})
	if err != nil {
		return "", false, err
	}
	if build.Status&pb.Status_ENDED_MASK == 0 {
		return "", false, nil
	}
	if build.Status&pb.Status_ENDED_MASK != 0 && build.Status != pb.Status_SUCCESS {
		return "", true, fmt.Errorf("build failed with status %v: %v", build.Status, build.SummaryMarkdown)
	}
	return build.SummaryMarkdown, true, nil
}
