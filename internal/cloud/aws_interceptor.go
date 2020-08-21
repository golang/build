// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cloud

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/ec2"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// rateLimiter is an interface mainly used for testing.
type rateLimiter interface {
	Wait(ctx context.Context) (err error)
	WaitN(ctx context.Context, n int) (err error)
}

// DefaultEC2LimitConfig sets limits defined in
// https://docs.aws.amazon.com/AWSEC2/latest/APIReference/throttling.html
var DefaultEC2LimitConfig = &EC2LimitConfig{
	MutatingRate:                    5,
	MutatingRateBucket:              200,
	NonMutatingRate:                 20,
	NonMutatingRateBucket:           100,
	RunInstanceRate:                 2,
	RunInstanceRateBucket:           5,
	RunInstanceResource:             2,
	RunInstanceResourceBucket:       1000,
	TerminateInstanceResource:       20,
	TerminateInstanceResourceBucket: 1000,
}

// EC2LimitConfig contains the desired rate and resource rate limit configurations.
type EC2LimitConfig struct {
	// MutatingRate sets the refill rate for mutating requests.
	MutatingRate float64
	// MutatingRateBucket sets the bucket size for mutating requests.
	MutatingRateBucket int
	// NonMutatingRate sets the refill rate for non-mutating requests.
	NonMutatingRate float64
	// NonMutatingRateBucket sets the bucket size for non-mutating requests.
	NonMutatingRateBucket int
	// RunInstanceRate sets the refill rate for run instance rate requests.
	RunInstanceRate float64
	// RunInstanceRateBucket sets the bucket size for run instance rate requests.
	RunInstanceRateBucket int
	// RunInstanceResource sets the refill rate for run instance rate resources.
	RunInstanceResource float64
	// RunInstanceResourceBucket sets the bucket size for run instance rate resources.
	RunInstanceResourceBucket int
	// TerminateInstanceResource sets the refill rate for terminate instance rate resources.
	TerminateInstanceResource float64
	// TerminateInstanceResourceBucket sets the bucket size for terminate instance resources.
	TerminateInstanceResourceBucket int
}

// WithRateLimiter adds a rate limiter to the AWSClient.
func WithRateLimiter(config *EC2LimitConfig) AWSOpt {
	return func(c *AWSClient) {
		c.ec2Client = &EC2RateLimitInterceptor{
			next:                      c.ec2Client,
			mutatingRate:              rate.NewLimiter(rate.Limit(config.MutatingRate), config.MutatingRateBucket),
			nonMutatingRate:           rate.NewLimiter(rate.Limit(config.NonMutatingRate), config.NonMutatingRateBucket),
			runInstancesRate:          rate.NewLimiter(rate.Limit(config.RunInstanceRate), config.RunInstanceRateBucket),
			runInstancesResource:      rate.NewLimiter(rate.Limit(config.RunInstanceResource), config.RunInstanceResourceBucket),
			terminateInstanceResource: rate.NewLimiter(rate.Limit(config.TerminateInstanceResource), config.TerminateInstanceResourceBucket),
		}
	}
}

var _ vmClient = (*EC2RateLimitInterceptor)(nil)

// EC2RateLimitInterceptor implements an interceptor that will rate limit requests
// to the AWS API and allow calls to the appropriate clients to proceed.
type EC2RateLimitInterceptor struct {
	// next is the client called after the rate limiting.
	next vmClient
	// mutatingRate is the rate limiter for mutating requests.
	mutatingRate rateLimiter
	// 	nonMutatingRate is the rate limiter for non-mutating requests.
	nonMutatingRate rateLimiter
	// runInstancesRate is the rate limiter for run instances requests.
	runInstancesRate rateLimiter
	// runInstancesResource is the rate limiter for run instance resources.
	runInstancesResource rateLimiter
	// terminateInstanceResource is the rate limiter for terminate instance resources.
	terminateInstanceResource rateLimiter
}

// DescribeInstancesPagesWithContext rate limits calls. The rate limiter will return an error if the request exceeds the bucket size, the Context is canceled, or the expected wait time exceeds the Context's Deadline.
func (i *EC2RateLimitInterceptor) DescribeInstancesPagesWithContext(ctx context.Context, in *ec2.DescribeInstancesInput, fn func(*ec2.DescribeInstancesOutput, bool) bool, opts ...request.Option) error {
	if err := i.nonMutatingRate.Wait(ctx); err != nil {
		return err
	}
	return i.next.DescribeInstancesPagesWithContext(ctx, in, fn, opts...)
}

// DescribeInstancesWithContext rate limits calls. The rate limiter will return an error if the request exceeds the bucket size, the Context is canceled, or the expected wait time exceeds the Context's Deadline.
func (i *EC2RateLimitInterceptor) DescribeInstancesWithContext(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...request.Option) (*ec2.DescribeInstancesOutput, error) {
	if err := i.nonMutatingRate.Wait(ctx); err != nil {
		return nil, err
	}
	return i.next.DescribeInstancesWithContext(ctx, in, opts...)
}

// RunInstancesWithContext rate limits calls. The rate limiter will return an error if the request exceeds the bucket size, the Context is canceled, or the expected wait time exceeds the Context's Deadline. An error is returned if either the rate or resource limiter returns an error.
func (i *EC2RateLimitInterceptor) RunInstancesWithContext(ctx context.Context, in *ec2.RunInstancesInput, opts ...request.Option) (*ec2.Reservation, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return i.runInstancesRate.Wait(ctx)
	})
	g.Go(func() error {
		numInst := aws.Int64Value(in.MaxCount)
		c := int(numInst)
		if int64(c) != numInst {
			return errors.New("unable to convert max count to int")
		}
		return i.runInstancesResource.WaitN(ctx, c)
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return i.next.RunInstancesWithContext(ctx, in, opts...)
}

// TerminateInstancesWithContext rate limits calls. The rate limiter will return an error if the request exceeds the bucket size, the Context is canceled, or the expected wait time exceeds the Context's Deadline. An error is returned if either the rate or resource limiter returns an error.
func (i *EC2RateLimitInterceptor) TerminateInstancesWithContext(ctx context.Context, in *ec2.TerminateInstancesInput, opts ...request.Option) (*ec2.TerminateInstancesOutput, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return i.mutatingRate.Wait(ctx)
	})
	g.Go(func() error {
		c := len(in.InstanceIds)
		return i.terminateInstanceResource.WaitN(ctx, c)
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return i.next.TerminateInstancesWithContext(ctx, in, opts...)
}

// WaitUntilInstanceRunningWithContext rate limits calls. The rate limiter will return an error if the request exceeds the bucket size, the Context is canceled, or the expected wait time exceeds the Context's Deadline.
func (i *EC2RateLimitInterceptor) WaitUntilInstanceRunningWithContext(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...request.WaiterOption) error {
	if err := i.nonMutatingRate.Wait(ctx); err != nil {
		return err
	}
	return i.next.WaitUntilInstanceRunningWithContext(ctx, in, opts...)
}

// DescribeInstanceTypesPagesWithContext rate limits calls. The rate limiter will return an error if the request exceeds the bucket size, the Context is canceled, or the expected wait time exceeds the Context's Deadline.
func (i *EC2RateLimitInterceptor) DescribeInstanceTypesPagesWithContext(ctx context.Context, in *ec2.DescribeInstanceTypesInput, fn func(*ec2.DescribeInstanceTypesOutput, bool) bool, opts ...request.Option) error {
	if err := i.nonMutatingRate.Wait(ctx); err != nil {
		return err
	}
	return i.next.DescribeInstanceTypesPagesWithContext(ctx, in, fn, opts...)
}
