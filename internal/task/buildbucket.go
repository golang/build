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
	// ListBuilders lists all the builders in bucket, keyed by their builder names.
	ListBuilders(ctx context.Context, bucket string) (map[string]*pb.BuilderConfig, error)
	// RunBuild runs a builder at commit with properties and returns its ID.
	RunBuild(ctx context.Context, bucket, builder string, commit *pb.GitilesCommit, properties map[string]*structpb.Value) (int64, error)
	// Completed reports whether a build has finished, returning an error if
	// it's failed. It's suitable for use with AwaitCondition.
	Completed(ctx context.Context, id int64) (string, bool, error)
	// SearchBuilds searches for builds matching pred and returns their IDs.
	SearchBuilds(ctx context.Context, pred *pb.BuildPredicate) ([]int64, error)
}

type RealBuildBucketClient struct {
	BuildersClient pb.BuildersClient
	BuildsClient   pb.BuildsClient
}

func (c *RealBuildBucketClient) ListBuilders(ctx context.Context, bucket string) (map[string]*pb.BuilderConfig, error) {
	var pageToken string
	builders := map[string]*pb.BuilderConfig{}
nextPage:
	resp, err := c.BuildersClient.ListBuilders(ctx, &pb.ListBuildersRequest{
		Project:   "golang",
		Bucket:    bucket,
		PageSize:  1000,
		PageToken: pageToken,
	})
	if err != nil {
		return nil, err
	}
	for _, b := range resp.Builders {
		builders[b.Id.Builder] = b.Config
	}
	if resp.NextPageToken != "" {
		pageToken = resp.NextPageToken
		goto nextPage
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
		return "", true, fmt.Errorf("build failed with status %v, see https://ci.chromium.org/b/%v: %v", build.Status, id, build.SummaryMarkdown)
	}
	return build.SummaryMarkdown, true, nil
}

func (c *RealBuildBucketClient) SearchBuilds(ctx context.Context, pred *pb.BuildPredicate) ([]int64, error) {
	resp, err := c.BuildsClient.SearchBuilds(ctx, &pb.SearchBuildsRequest{
		Predicate: pred,
	})
	if err != nil {
		return nil, err
	}
	if resp.NextPageToken != "" {
		return nil, fmt.Errorf("page size to SearchBuilds insufficient")
	}
	var results []int64
	for _, b := range resp.Builds {
		results = append(results, b.Id)
	}
	return results, nil
}
