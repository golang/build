// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cloud

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/ec2"
)

var _ rateLimiter = (*fakeRateLimiter)(nil)

var rateExceededErr = errors.New("rate limit exceeded")

type fakeRateLimiter struct {
	waitCalledCount int64
	waitCallLimit   int64
}

func newFakeRateLimiter(limit int64) *fakeRateLimiter {
	return &fakeRateLimiter{waitCallLimit: limit}
}

func (frl *fakeRateLimiter) Wait(ctx context.Context) (err error) {
	return frl.WaitN(ctx, 1)
}

func (frl *fakeRateLimiter) WaitN(ctx context.Context, n int) (err error) {
	count := atomic.AddInt64(&frl.waitCalledCount, int64(n))
	if count > frl.waitCallLimit {
		return rateExceededErr
	}
	return nil
}

func (frl *fakeRateLimiter) called() bool {
	if atomic.LoadInt64(&frl.waitCalledCount) > 0 {
		return true
	}
	return false
}

type noopEC2Client struct {
	t *testing.T
}

func (f *noopEC2Client) DescribeInstancesPagesWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, fn func(*ec2.DescribeInstancesOutput, bool) bool, opt ...request.Option) error {
	if ctx == nil || input == nil || fn == nil || len(opt) != 1 {
		f.t.Fatal("DescribeInstancesPagesWithContext params not passed down")
	}
	return nil
}

func (f *noopEC2Client) DescribeInstancesWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, opt ...request.Option) (*ec2.DescribeInstancesOutput, error) {
	if ctx == nil || input == nil || len(opt) != 1 {
		f.t.Fatal("DescribeInstancesWithContext params not passed down")
	}
	return nil, nil
}

func (f *noopEC2Client) RunInstancesWithContext(ctx context.Context, input *ec2.RunInstancesInput, opts ...request.Option) (*ec2.Reservation, error) {
	if ctx == nil || input == nil || len(opts) != 1 {
		f.t.Fatal("RunInstancesWithContext params not passed down")
	}
	return nil, nil
}

func (f *noopEC2Client) TerminateInstancesWithContext(ctx context.Context, input *ec2.TerminateInstancesInput, opts ...request.Option) (*ec2.TerminateInstancesOutput, error) {
	if ctx == nil || input == nil || len(opts) != 1 {
		f.t.Fatal("TerminateInstancesWithContext params not passed down")
	}
	return nil, nil
}

func (f *noopEC2Client) WaitUntilInstanceRunningWithContext(ctx context.Context, input *ec2.DescribeInstancesInput, opt ...request.WaiterOption) error {
	if ctx == nil || input == nil || len(opt) != 1 {
		f.t.Fatal("WaitUntilInstanceRunningWithContext params not passed down")
	}
	return nil
}

func (f *noopEC2Client) DescribeInstanceTypesPagesWithContext(ctx context.Context, input *ec2.DescribeInstanceTypesInput, fn func(*ec2.DescribeInstanceTypesOutput, bool) bool, opt ...request.Option) error {
	if ctx == nil || input == nil || fn == nil || len(opt) != 1 {
		f.t.Fatal("DescribeInstancesPagesWithContext params not passed down")
	}
	return nil
}

func TestEC2RateLimitInterceptorDescribeInstancesPagesWithContext(t *testing.T) {
	rate := newFakeRateLimiter(1)
	i := &EC2RateLimitInterceptor{
		next:            &noopEC2Client{t: t},
		nonMutatingRate: rate,
	}
	fn := func() error {
		return i.DescribeInstancesPagesWithContext(context.Background(), &ec2.DescribeInstancesInput{}, func(*ec2.DescribeInstancesOutput, bool) bool { return true }, request.WithAppendUserAgent("test-agent"))
	}
	if err := fn(); err != nil {
		t.Fatalf("DescribeInstancesPagesWithContext(...) = nil, %s; want no error", err)
	}
	if !rate.called() {
		t.Error("rateLimiter.Wait() was never called")
	}
	if err := fn(); err != rateExceededErr {
		t.Errorf("DescribeInstancesPagesWithContext(...) = %s; want %s", err, rateExceededErr)
	}
}

func TestEC2RateLimitInterceptorDescribeInstancesWithContext(t *testing.T) {
	rate := newFakeRateLimiter(1)
	i := &EC2RateLimitInterceptor{
		next:            &noopEC2Client{t: t},
		nonMutatingRate: rate,
	}
	fn := func() error {
		_, err := i.DescribeInstancesWithContext(context.Background(), &ec2.DescribeInstancesInput{}, request.WithAppendUserAgent("test-agent"))
		return err
	}
	if err := fn(); err != nil {
		t.Fatalf("DescribeInstancesWithContext(...) = nil, %s; want no error", err)
	}
	if !rate.called() {
		t.Errorf("rateLimiter.Wait() was never called")
	}
	if err := fn(); err != rateExceededErr {
		t.Errorf("DescribeInstancesWithContext(...) = nil, %s; want nil, %s", err, rateExceededErr)
	}
}

func TestEC2RateLimitInterceptorRunInstancesWithContext(t *testing.T) {
	rate := newFakeRateLimiter(1)
	resource := newFakeRateLimiter(1)
	i := &EC2RateLimitInterceptor{
		next:                 &noopEC2Client{t: t},
		runInstancesRate:     rate,
		runInstancesResource: resource,
	}
	fn := func() error {
		_, err := i.RunInstancesWithContext(context.Background(), &ec2.RunInstancesInput{
			MaxCount: aws.Int64(1),
		}, request.WithAppendUserAgent("test-agent"))
		return err
	}
	if err := fn(); err != nil {
		t.Fatalf("RunInstancesWithContext(...) = nil, %s; want no error", err)
	}
	if !rate.called() || !resource.called() {
		t.Errorf("rateLimiter.Wait() was never called; rate=%t, resource=%t", rate.called(), resource.called())
	}
	if err := fn(); err != rateExceededErr {
		t.Errorf("RunInstancesWithContext(...) = nil, %s; want nil, %s", err, rateExceededErr)
	}
}

func TestEC2RateLimitInterceptorTerminateInstancesWithContext(t *testing.T) {
	rate := newFakeRateLimiter(1)
	resource := newFakeRateLimiter(1)
	i := &EC2RateLimitInterceptor{
		next:                      &noopEC2Client{t: t},
		mutatingRate:              rate,
		terminateInstanceResource: resource,
	}
	fn := func() error {
		_, err := i.TerminateInstancesWithContext(context.Background(), &ec2.TerminateInstancesInput{
			InstanceIds: []*string{aws.String("foo")},
		}, request.WithAppendUserAgent("test-agent"))
		return err
	}
	if err := fn(); err != nil {
		t.Fatalf("TerminateInstancesWithContext(...) = nil, %s; want no error", err)
	}
	if !rate.called() || !resource.called() {
		t.Errorf("rateLimiter.Wait() was never called; rate=%t, resource=%t", rate.called(), resource.called())
	}
	if err := fn(); err != rateExceededErr {
		t.Errorf("TerminateInstancesWithContext(...) = nil, %s; want nil, %s", err, rateExceededErr)
	}
}

func TestEC2RateLimitInterceptorWaitUntilInstanceRunningWithContext(t *testing.T) {
	rate := newFakeRateLimiter(1)
	i := &EC2RateLimitInterceptor{
		next:            &noopEC2Client{t: t},
		nonMutatingRate: rate,
	}
	fn := func() error {
		return i.WaitUntilInstanceRunningWithContext(context.Background(), &ec2.DescribeInstancesInput{}, request.WithWaiterMaxAttempts(1))
	}
	if err := fn(); err != nil {
		t.Fatalf("WaitUntilInstanceRunningWithContext(...) = nil, %s; want no error", err)
	}
	if !rate.called() {
		t.Errorf("rateLimiter.Wait() was never called")
	}
	if err := fn(); err != rateExceededErr {
		t.Errorf("WaitUntilInstanceRunningWithContext(...) = nil, %s; want nil, %s", err, rateExceededErr)
	}
}

func TestEC2RateLimitInterceptorDescribeInstanceTypesPagesWithContext(t *testing.T) {
	rate := newFakeRateLimiter(1)
	i := &EC2RateLimitInterceptor{
		next:            &noopEC2Client{t: t},
		nonMutatingRate: rate,
	}
	fn := func() error {
		return i.DescribeInstanceTypesPagesWithContext(context.Background(), &ec2.DescribeInstanceTypesInput{}, func(*ec2.DescribeInstanceTypesOutput, bool) bool { return true }, request.WithAppendUserAgent("test-agent"))
	}
	if err := fn(); err != nil {
		t.Fatalf("DescribeInstanceTypesPagesWithContext(...) = nil, %s; want no error", err)
	}
	if !rate.called() {
		t.Errorf("rateLimiter.Wait() was never called")
	}
	if err := fn(); err != rateExceededErr {
		t.Errorf("DescribeInstanceTypesPagesWithContext(...) = nil, %s; want nil, %s", err, rateExceededErr)
	}
}
