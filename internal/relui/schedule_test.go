// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/robfig/cron/v3"
	"golang.org/x/build/internal/relui/db"
)

func mustParseSpec(t *testing.T, spec string) cron.Schedule {
	t.Helper()
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		t.Fatalf("cron.ParseStandard(%q) = %q, wanted no error", spec, err)
	}
	return sched
}

func TestSchedulerCreate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		desc         string
		sched        Schedule
		workflowName string
		params       map[string]any
		want         db.Schedule
		wantEntries  []ScheduleEntry
		wantErr      bool
	}{
		{
			desc:         "success: once",
			sched:        Schedule{Once: now.AddDate(1, 0, 0), Type: ScheduleOnce},
			workflowName: "echo",
			params:       map[string]any{"greeting": "hello", "farewell": "bye"},
			want: db.Schedule{
				WorkflowName: "echo",
				WorkflowParams: sql.NullString{
					String: `{"farewell": "bye", "greeting": "hello"}`,
					Valid:  true,
				},
				Once:      now.AddDate(1, 0, 0),
				CreatedAt: now,
				UpdatedAt: now,
			},
			wantEntries: []ScheduleEntry{
				{
					Schedule: &RunOnce{next: now.UTC().AddDate(1, 0, 0)},
					Next:     now.UTC().AddDate(1, 0, 0),
					Job: &WorkflowSchedule{
						Schedule: db.Schedule{
							WorkflowName: "echo",
							WorkflowParams: sql.NullString{
								String: `{"farewell": "bye", "greeting": "hello"}`,
								Valid:  true,
							},
							Once:      now.UTC().AddDate(1, 0, 0),
							CreatedAt: now,
							UpdatedAt: now,
						},
						Params: map[string]any{"greeting": "hello", "farewell": "bye"},
					},
				},
			},
		},
		{
			desc:         "success: cron",
			sched:        Schedule{Cron: "* * * * *", Type: ScheduleCron},
			workflowName: "echo",
			params:       map[string]any{"greeting": "hello", "farewell": "bye"},
			want: db.Schedule{
				WorkflowName: "echo",
				WorkflowParams: sql.NullString{
					String: `{"farewell": "bye", "greeting": "hello"}`,
					Valid:  true,
				},
				Spec:      "* * * * *",
				CreatedAt: now,
				UpdatedAt: now,
			},
			wantEntries: []ScheduleEntry{
				{
					Schedule: mustParseSpec(t, "* * * * *"),
					Next:     now.Add(time.Minute),
					Job: &WorkflowSchedule{
						Schedule: db.Schedule{
							WorkflowName: "echo",
							WorkflowParams: sql.NullString{
								String: `{"farewell": "bye", "greeting": "hello"}`,
								Valid:  true,
							},
							Spec:      "* * * * *",
							CreatedAt: now,
							UpdatedAt: now,
						},
						Params: map[string]any{"greeting": "hello", "farewell": "bye"},
					},
				},
			},
		},
		{
			desc:         "error: invalid Schedule",
			sched:        Schedule{Type: ScheduleImmediate},
			workflowName: "echo",
			params:       map[string]any{"greeting": "hello", "farewell": "bye"},
			wantErr:      true,
			wantEntries:  []ScheduleEntry{},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := testDB(ctx, t)
			s := NewScheduler(p, NewWorker(NewDefinitionHolder(), p, &PGListener{p}))
			row, err := s.Create(ctx, c.sched, c.workflowName, c.params)
			if (err != nil) != c.wantErr {
				t.Fatalf("s.Create(_, %v, %q, %v) = %v, %v, wantErr: %t", c.sched, c.workflowName, c.params, row, err, c.wantErr)
			}
			if diff := cmp.Diff(c.want, row, cmpopts.EquateApproxTime(time.Minute), cmpopts.IgnoreFields(db.Schedule{}, "ID")); diff != "" {
				t.Fatalf("s.Create() mismatch (-want +got):\n%s", diff)
			}
			got := s.Entries()

			diffOpts := []cmp.Option{
				cmpopts.EquateApproxTime(time.Minute),
				cmpopts.IgnoreFields(db.Schedule{}, "ID"),
				cmpopts.IgnoreUnexported(RunOnce{}, WorkflowSchedule{}, time.Location{}),
				cmpopts.IgnoreFields(ScheduleEntry{}, "ID", "WrappedJob"),
			}
			if diff := cmp.Diff(c.wantEntries, got, diffOpts...); diff != "" {
				t.Fatalf("s.Entries() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSchedulerResume(t *testing.T) {
	now := time.Now()
	cases := []struct {
		desc       string
		scheds     []db.CreateScheduleParams
		want       []ScheduleEntry
		wantParams map[string]any
		wantErr    bool
	}{
		{
			desc: "success: once",
			scheds: []db.CreateScheduleParams{
				{
					WorkflowName: "echo",
					WorkflowParams: sql.NullString{
						String: `{"farewell": "bye", "greeting": "hello"}`,
						Valid:  true,
					},
					Once:      now.UTC().AddDate(1, 0, 0),
					CreatedAt: now,
					UpdatedAt: now,
				},
			},
			want: []ScheduleEntry{
				{
					Schedule: &RunOnce{next: now.UTC().AddDate(1, 0, 0)},
					Next:     now.UTC().AddDate(1, 0, 0),
					Job: &WorkflowSchedule{
						Schedule: db.Schedule{
							WorkflowName: "echo",
							WorkflowParams: sql.NullString{
								String: `{"farewell": "bye", "greeting": "hello"}`,
								Valid:  true,
							},
							Once:      now.UTC().AddDate(1, 0, 0),
							CreatedAt: now,
							UpdatedAt: now,
						},
						Params: map[string]any{"greeting": "hello", "farewell": "bye"},
					},
				},
			},
		},
		{
			desc: "success: cron",
			scheds: []db.CreateScheduleParams{
				{
					WorkflowName: "echo",
					WorkflowParams: sql.NullString{
						String: `{"farewell": "bye", "greeting": "hello"}`,
						Valid:  true,
					},
					Spec:      "* * * * *",
					CreatedAt: now,
					UpdatedAt: now,
				},
			},
			want: []ScheduleEntry{
				{
					Schedule: mustParseSpec(t, "* * * * *"),
					Next:     now.Add(time.Minute),
					Job: &WorkflowSchedule{
						Schedule: db.Schedule{
							WorkflowName: "echo",
							WorkflowParams: sql.NullString{
								String: `{"farewell": "bye", "greeting": "hello"}`,
								Valid:  true,
							},
							Spec:      "* * * * *",
							CreatedAt: now,
							UpdatedAt: now,
						},
						Params: map[string]any{"greeting": "hello", "farewell": "bye"},
					},
				},
			},
		},
		{
			desc: "skip past RunOnce schedules",
			scheds: []db.CreateScheduleParams{
				{
					WorkflowName: "echo",
					WorkflowParams: sql.NullString{
						String: `{"farewell": "bye", "greeting": "hello"}`,
						Valid:  true,
					},
					Once:      time.Now().AddDate(-1, 0, 0),
					CreatedAt: now,
					UpdatedAt: now,
				},
			},
			want: []ScheduleEntry{},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := testDB(ctx, t)
			q := db.New(p)
			s := NewScheduler(p, NewWorker(NewDefinitionHolder(), p, &PGListener{p}))

			for _, csp := range c.scheds {
				if _, err := q.CreateSchedule(ctx, csp); err != nil {
					t.Fatalf("q.CreateSchedule(_, %#v) = _, %v, wanted no error", csp, err)
				}
			}
			if err := s.Resume(ctx); err != nil {
				t.Errorf("s.Resume() = %v, wanted no error", err)
			}
			got := s.Entries()

			diffOpts := []cmp.Option{
				cmpopts.EquateApproxTime(time.Minute),
				cmpopts.IgnoreFields(db.Schedule{}, "ID"),
				cmpopts.IgnoreUnexported(RunOnce{}, WorkflowSchedule{}, time.Location{}),
				cmpopts.IgnoreFields(ScheduleEntry{}, "ID", "WrappedJob"),
			}
			if diff := cmp.Diff(c.want, got, diffOpts...); diff != "" {
				t.Fatalf("s.Entries() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
