// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux || darwin

package pool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/cloud"
	"golang.org/x/build/internal/coordinator/pool/queue"
	"golang.org/x/build/internal/spanlog"
)

func TestEC2BuildletGetBuildlet(t *testing.T) {
	host := "host-type-x"

	l := newLedger()
	l.UpdateInstanceTypes([]*cloud.InstanceType{
		// set to default gce type because there is no way to set the machine
		// type from outside of the buildenv package.
		{
			Type: "e2-standard-16",
			CPU:  16,
		},
	})
	l.SetCPULimit(20)

	bp := &EC2Buildlet{
		buildletClient: &fakeEC2BuildletClient{
			createVMRequestSuccess: true,
			VMCreated:              true,
			buildletCreated:        true,
		},
		buildEnv: &buildenv.Environment{},
		ledger:   l,
		hosts: map[string]*dashboard.HostConfig{
			host: {
				VMImage:        "ami-15",
				ContainerImage: "bar-arm64:latest",
				SSHUsername:    "foo",
			},
		},
	}
	_, err := bp.GetBuildlet(context.Background(), host, noopEventTimeLogger{}, new(queue.SchedItem))
	if err != nil {
		t.Errorf("EC2Buildlet.GetBuildlet(ctx, %q, %+v) = _, %s; want no error", host, noopEventTimeLogger{}, err)
	}
}

func TestEC2BuildletGetBuildletError(t *testing.T) {
	host := "host-type-x"
	testCases := []struct {
		desc           string
		hostType       string
		logger         Logger
		ledger         *ledger
		types          []*cloud.InstanceType
		buildletClient ec2BuildletClient
		hosts          map[string]*dashboard.HostConfig
	}{
		{
			desc:     "invalid-host-type",
			hostType: host,
			ledger:   newLedger(),
			types: []*cloud.InstanceType{
				{
					Type: "e2-highcpu-2",
					CPU:  4,
				},
			},
			hosts: map[string]*dashboard.HostConfig{
				"wrong-host-type": {},
			},
			logger: noopEventTimeLogger{},
			buildletClient: &fakeEC2BuildletClient{
				createVMRequestSuccess: true,
				VMCreated:              true,
			},
		},
		{
			desc:     "buildlet-client-failed-instance-created",
			hostType: host,
			ledger:   newLedger(),
			types: []*cloud.InstanceType{
				{
					Type: "e2-highcpu-2",
					CPU:  4,
				},
			},
			hosts: map[string]*dashboard.HostConfig{
				host: {},
			},
			logger: noopEventTimeLogger{},
			buildletClient: &fakeEC2BuildletClient{
				createVMRequestSuccess: false,
				VMCreated:              false,
			},
		},
		{
			desc:     "buildlet-client-failed-instance-not-created",
			hostType: host,
			ledger:   newLedger(),
			types: []*cloud.InstanceType{
				{
					Type: "e2-highcpu-2",
					CPU:  4,
				},
			},
			hosts: map[string]*dashboard.HostConfig{
				host: {},
			},
			logger: noopEventTimeLogger{},
			buildletClient: &fakeEC2BuildletClient{
				createVMRequestSuccess: true,
				VMCreated:              false,
			},
		},
	}
	for _, tt := range testCases {
		t.Run(tt.desc, func(t *testing.T) {
			bp := &EC2Buildlet{
				buildletClient: tt.buildletClient,
				buildEnv:       &buildenv.Environment{},
				ledger:         tt.ledger,
				hosts:          tt.hosts,
			}
			tt.ledger.UpdateInstanceTypes(tt.types)
			_, gotErr := bp.GetBuildlet(context.Background(), tt.hostType, tt.logger, new(queue.SchedItem))
			if gotErr == nil {
				t.Errorf("EC2Buildlet.GetBuildlet(ctx, %q, %+v) = _, %s", tt.hostType, tt.logger, gotErr)
			}
		})
	}
}

func TestEC2BuildletGetBuildletLogger(t *testing.T) {
	host := "host-type-x"
	testCases := []struct {
		desc           string
		buildletClient ec2BuildletClient
		hostType       string
		hosts          map[string]*dashboard.HostConfig
		ledger         *ledger
		types          []*cloud.InstanceType
		wantLogs       []string
		wantSpans      []string
		wantSpansErr   []string
	}{
		{
			desc:     "buildlet-client-failed-instance-create-request-failed",
			hostType: host,
			ledger:   newLedger(),
			types: []*cloud.InstanceType{
				{
					Type: "e2-standard-8",
					CPU:  8,
				},
			},
			hosts: map[string]*dashboard.HostConfig{
				host: {},
			},
			buildletClient: &fakeEC2BuildletClient{
				createVMRequestSuccess: false,
				VMCreated:              false,
				buildletCreated:        false,
			},
			wantSpans:    []string{"create_ec2_instance", "awaiting_ec2_quota", "create_ec2_buildlet"},
			wantSpansErr: []string{"create_ec2_buildlet", "create_ec2_instance"},
		},
		{
			desc:     "buildlet-client-failed-instance-not-created",
			hostType: host,
			ledger:   newLedger(),
			types: []*cloud.InstanceType{
				{
					Type: "e2-standard-8",
					CPU:  8,
				},
			},
			hosts: map[string]*dashboard.HostConfig{
				host: {},
			},
			buildletClient: &fakeEC2BuildletClient{
				createVMRequestSuccess: true,
				VMCreated:              false,
				buildletCreated:        false,
			},
			wantSpans:    []string{"create_ec2_instance", "awaiting_ec2_quota", "create_ec2_buildlet"},
			wantSpansErr: []string{"create_ec2_buildlet", "create_ec2_instance"},
		},
		{
			desc:     "buildlet-client-failed-instance-created",
			hostType: host,
			ledger:   newLedger(),
			types: []*cloud.InstanceType{
				{
					Type: "e2-standard-8",
					CPU:  8,
				},
			},
			hosts: map[string]*dashboard.HostConfig{
				host: {},
			},
			buildletClient: &fakeEC2BuildletClient{
				createVMRequestSuccess: true,
				VMCreated:              true,
				buildletCreated:        false,
			},
			wantSpans:    []string{"create_ec2_instance", "awaiting_ec2_quota", "create_ec2_buildlet", "wait_buildlet_start"},
			wantSpansErr: []string{"create_ec2_buildlet", "wait_buildlet_start"},
		},
		{
			desc:     "success",
			hostType: host,
			ledger:   newLedger(),
			types: []*cloud.InstanceType{
				{
					Type: "e2-standard-8",
					CPU:  8,
				},
			},
			hosts: map[string]*dashboard.HostConfig{
				host: {},
			},
			buildletClient: &fakeEC2BuildletClient{
				createVMRequestSuccess: true,
				VMCreated:              true,
				buildletCreated:        true,
			},
			wantSpans:    []string{"create_ec2_instance", "create_ec2_buildlet", "awaiting_ec2_quota", "wait_buildlet_start"},
			wantSpansErr: []string{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			bp := &EC2Buildlet{
				buildletClient: tc.buildletClient,
				buildEnv:       &buildenv.Environment{},
				ledger:         tc.ledger,
				hosts:          tc.hosts,
			}
			bp.ledger.SetCPULimit(20)
			bp.ledger.UpdateInstanceTypes(tc.types)
			l := newTestLogger()
			_, _ = bp.GetBuildlet(context.Background(), tc.hostType, l, new(queue.SchedItem))
			if !cmp.Equal(l.spanEvents(), tc.wantSpans, cmp.Transformer("sort", func(in []string) []string {
				out := append([]string(nil), in...)
				sort.Strings(out)
				return out
			})) {
				t.Errorf("span events = %+v; want %+v", l.spanEvents(), tc.wantSpans)
			}
			for _, spanErr := range tc.wantSpansErr {
				s, ok := l.spans[spanErr]
				if !ok {
					t.Fatalf("log span %q does not exist", spanErr)
				}
				if s.err == nil {
					t.Fatalf("testLogger.span[%q].err is nil", spanErr)
				}
			}
		})
	}
}

func TestEC2BuildletString(t *testing.T) {
	testCases := []struct {
		desc      string
		instCount int64
		cpuCount  int64
		cpuLimit  int64
	}{
		{"default", 0, 0, 0},
		{"non-default", 2, 2, 3},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			es := make([]*entry, tc.instCount)
			entries := make(map[string]*entry)
			for i, e := range es {
				entries[fmt.Sprintf("%d", i)] = e
			}
			l := newLedger()
			eb := &EC2Buildlet{ledger: l}
			l.entries = entries
			eb.ledger.cpuQueue.UpdateQuotas(int(tc.cpuCount), int(tc.cpuLimit))
			want := fmt.Sprintf("EC2 pool capacity: %d instances; %d/%d CPUs", tc.instCount, tc.cpuCount, tc.cpuLimit)
			got := eb.String()
			if got != want {
				t.Errorf("EC2Buildlet.String() = %s; want %s", got, want)
			}
		})
	}
}

func TestEC2BuildletCapacityString(t *testing.T) {
	testCases := []struct {
		desc      string
		instCount int64
		cpuCount  int64
		cpuLimit  int64
	}{
		{"defaults", 0, 0, 0},
		{"non-default", 2, 2, 3},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			es := make([]*entry, tc.instCount)
			entries := make(map[string]*entry)
			for i, e := range es {
				entries[fmt.Sprintf("%d", i)] = e
			}
			l := newLedger()
			l.entries = entries
			eb := &EC2Buildlet{ledger: l}
			eb.ledger.cpuQueue.UpdateQuotas(int(tc.cpuCount), int(tc.cpuLimit))
			want := fmt.Sprintf("%d instances; %d/%d CPUs", tc.instCount, tc.cpuCount, tc.cpuLimit)
			got := eb.capacityString()
			if got != want {
				t.Errorf("EC2Buildlet.capacityString() = %s; want %s", got, want)
			}
		})
	}
}

func TestEC2BuildletbuildletDone(t *testing.T) {
	t.Run("done-successful", func(t *testing.T) {
		instName := "instance-name-x"

		awsC := cloud.NewFakeAWSClient()
		inst, err := awsC.CreateInstance(context.Background(), &cloud.EC2VMConfiguration{
			Description: "test instance",
			ImageID:     "image-x",
			Name:        instName,
			SSHKeyID:    "key-14",
			Tags:        map[string]string{},
			Type:        "type-x",
			Zone:        "zone-1",
		})
		if err != nil {
			t.Errorf("unable to create instance: %s", err)
		}

		l := newLedger()
		pool := &EC2Buildlet{
			awsClient: awsC,
			ledger:    l,
		}
		l.entries = map[string]*entry{
			instName: {
				createdAt:    time.Now(),
				instanceID:   inst.ID,
				instanceName: instName,
				vCPUCount:    5,
				quota:        new(queue.Item),
			},
		}
		pool.buildletDone(instName)
		if gotID := pool.ledger.InstanceID(instName); gotID != "" {
			t.Errorf("ledger.instanceID = %q; want %q", gotID, "")
		}
		gotInsts, err := awsC.RunningInstances(context.Background())
		if err != nil || len(gotInsts) != 0 {
			t.Errorf("awsClient.RunningInstances(ctx) = %+v, %s; want [], nil", gotInsts, err)
		}
	})
	t.Run("instance-not-in-ledger", func(t *testing.T) {
		instName := "instance-name-x"

		awsC := cloud.NewFakeAWSClient()
		inst, err := awsC.CreateInstance(context.Background(), &cloud.EC2VMConfiguration{
			Description: "test instance",
			ImageID:     "image-x",
			Name:        instName,
			SSHKeyID:    "key-14",
			Tags:        map[string]string{},
			Type:        "type-x",
			Zone:        "zone-1",
		})
		if err != nil {
			t.Errorf("unable to create instance: %s", err)
		}

		pool := &EC2Buildlet{
			awsClient: awsC,
			ledger:    newLedger(),
		}
		pool.buildletDone(inst.Name)
		gotInsts, err := awsC.RunningInstances(context.Background())
		if err != nil || len(gotInsts) != 1 {
			t.Errorf("awsClient.RunningInstances(ctx) = %+v, %s; want 1 instance, nil", gotInsts, err)
		}
	})
	t.Run("instance-not-in-ec2", func(t *testing.T) {
		instName := "instance-name-x"
		l := newLedger()
		pool := &EC2Buildlet{
			awsClient: cloud.NewFakeAWSClient(),
			ledger:    l,
		}
		l.entries = map[string]*entry{
			instName: {
				createdAt:    time.Now(),
				instanceID:   "instance-id-14",
				instanceName: instName,
				vCPUCount:    5,
				quota:        new(queue.Item),
			},
		}
		pool.buildletDone(instName)
		if gotID := pool.ledger.InstanceID(instName); gotID != "" {
			t.Errorf("ledger.instanceID = %q; want %q", gotID, "")
		}
	})
}

func TestEC2BuildletClose(t *testing.T) {
	cancelled := false
	pool := &EC2Buildlet{
		cancelPoll: func() { cancelled = true },
	}
	pool.Close()
	if !cancelled {
		t.Error("EC2Buildlet.pollCancel not called")
	}
}

func TestEC2BuildletRetrieveAndSetQuota(t *testing.T) {
	pool := &EC2Buildlet{
		awsClient: cloud.NewFakeAWSClient(),
		ledger:    newLedger(),
	}
	err := pool.retrieveAndSetQuota(context.Background())
	if err != nil {
		t.Errorf("EC2Buildlet.retrieveAndSetQuota(ctx) = %s; want nil", err)
	}
	usage := pool.ledger.cpuQueue.Quotas()
	if usage.Limit == 0 {
		t.Errorf("ledger.cpuLimit = %d; want non-zero", usage.Limit)
	}
}

func TestEC2BuildletRetrieveAndSetInstanceTypes(t *testing.T) {
	pool := &EC2Buildlet{
		awsClient: cloud.NewFakeAWSClient(),
		ledger:    newLedger(),
	}
	err := pool.retrieveAndSetInstanceTypes()
	if err != nil {
		t.Errorf("EC2Buildlet.retrieveAndSetInstanceTypes() = %s; want nil", err)
	}
	if len(pool.ledger.types) == 0 {
		t.Errorf("len(pool.ledger.types) = %d; want non-zero", len(pool.ledger.types))
	}
}

func TestEC2BuildeletDestroyUntrackedInstances(t *testing.T) {
	awsC := cloud.NewFakeAWSClient()
	create := func(name string) *cloud.Instance {
		inst, err := awsC.CreateInstance(context.Background(), &cloud.EC2VMConfiguration{
			Description: "test instance",
			ImageID:     "image-x",
			Name:        name,
			SSHKeyID:    "key-14",
			Tags:        map[string]string{},
			Type:        "type-x",
			Zone:        "zone-1",
		})
		if err != nil {
			t.Errorf("unable to create instance: %s", err)
		}
		return inst
	}
	// create untracked instances
	for it := 0; it < 10; it++ {
		_ = create(instanceName("host-test-type", 10))
	}
	wantTrackedInst := create(instanceName("host-test-type", 10))
	wantRemoteInst := create(instanceName("host-test-type", 10))
	_ = create("debug-tiger-host-14") // non buildlet instance

	l := newLedger()
	pool := &EC2Buildlet{
		awsClient: awsC,
		isRemoteBuildlet: func(name string) bool {
			if name == wantRemoteInst.Name {
				return true
			}
			return false
		},
		ledger: l,
	}
	l.entries = map[string]*entry{
		wantTrackedInst.Name: {
			createdAt:    time.Now(),
			instanceID:   wantTrackedInst.ID,
			instanceName: wantTrackedInst.Name,
			vCPUCount:    4,
		},
	}
	pool.destroyUntrackedInstances(context.Background())
	wantInstCount := 3
	gotInsts, err := awsC.RunningInstances(context.Background())
	if err != nil || len(gotInsts) != wantInstCount {
		t.Errorf("awsClient.RunningInstances(ctx) = %+v, %s; want %d instances and no error", gotInsts, err, wantInstCount)
	}
}

// fakeEC2BuildletClient is the client used to create buildlets on EC2.
type fakeEC2BuildletClient struct {
	createVMRequestSuccess bool
	VMCreated              bool
	buildletCreated        bool
}

// StartNewVM boots a new VM on EC2, waits until the client is accepting connections
// on the configured port and returns a buildlet client configured communicate with it.
func (f *fakeEC2BuildletClient) StartNewVM(ctx context.Context, buildEnv *buildenv.Environment, hconf *dashboard.HostConfig, vmName, hostType string, opts *buildlet.VMOpts) (buildlet.Client, error) {
	// check required params
	if opts == nil || opts.TLS.IsZero() {
		return nil, errors.New("TLS keypair is not set")
	}
	if buildEnv == nil {
		return nil, errors.New("invalid build environment")
	}
	if hconf == nil {
		return nil, errors.New("invalid host configuration")
	}
	if vmName == "" || hostType == "" {
		return nil, fmt.Errorf("invalid vmName: %q and hostType: %q", vmName, hostType)
	}
	if opts.DeleteIn == 0 {
		// Note: This implements a short default in the rare case the caller doesn't care.
		opts.DeleteIn = 30 * time.Minute
	}
	if !f.createVMRequestSuccess {
		return nil, fmt.Errorf("unable to create instance %s: creation disabled", vmName)
	}
	condRun := func(fn func()) {
		if fn != nil {
			fn()
		}
	}
	condRun(opts.OnInstanceRequested)
	if !f.VMCreated {
		return nil, errors.New("error waiting for instance to exist: vm existence disabled")
	}

	condRun(opts.OnInstanceCreated)

	if !f.buildletCreated {
		return nil, errors.New("error waiting for buildlet: buildlet creation disabled")
	}

	if opts.OnGotEC2InstanceInfo != nil {
		opts.OnGotEC2InstanceInfo(&cloud.Instance{
			CPUCount:          4,
			CreatedAt:         time.Time{},
			Description:       "sample vm",
			ID:                "id-" + instanceName("random", 4),
			IPAddressExternal: "127.0.0.1",
			IPAddressInternal: "127.0.0.1",
			ImageID:           "image-x",
			Name:              vmName,
			SSHKeyID:          "key-15",
			SecurityGroups:    nil,
			State:             "running",
			Tags: map[string]string{
				"foo": "bar",
			},
			Type: "yy.large",
			Zone: "zone-a",
		})
	}
	return &buildlet.FakeClient{}, nil
}

type testLogger struct {
	eventTimes []eventTime
	spans      map[string]*span
}

type eventTime struct {
	event string
	opt   []string
}

type span struct {
	event      string
	opt        []string
	err        error
	calledDone bool
}

func (s *span) Done(err error) error {
	s.err = err
	s.calledDone = true
	return nil
}

func newTestLogger() *testLogger {
	return &testLogger{
		eventTimes: make([]eventTime, 0, 5),
		spans:      make(map[string]*span),
	}
}

func (l *testLogger) LogEventTime(event string, optText ...string) {
	l.eventTimes = append(l.eventTimes, eventTime{
		event: event,
		opt:   optText,
	})
}

func (l *testLogger) CreateSpan(event string, optText ...string) spanlog.Span {
	s := &span{
		event: event,
		opt:   optText,
	}
	l.spans[event] = s
	return s
}

func (l *testLogger) spanEvents() []string {
	se := make([]string, 0, len(l.spans))
	for k, s := range l.spans {
		if !s.calledDone {
			continue
		}
		se = append(se, k)
	}
	return se
}

type noopEventTimeLogger struct{}

func (l noopEventTimeLogger) LogEventTime(event string, optText ...string) {}
func (l noopEventTimeLogger) CreateSpan(event string, optText ...string) spanlog.Span {
	return noopSpan{}
}

type noopSpan struct{}

func (s noopSpan) Done(err error) error { return nil }
