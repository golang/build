// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v4"
	"github.com/robfig/cron/v3"
	"golang.org/x/build/internal/relui/db"
)

// ScheduleType determines whether a workflow runs immediately or on
// some future date or cadence.
type ScheduleType string

// ElementID returns a string suitable for a HTML element ID.
func (s ScheduleType) ElementID() string {
	return strings.ReplaceAll(string(s), " ", "")
}

// FormField returns a string representing which datatype to present
// the user on the creation form.
func (s ScheduleType) FormField() string {
	switch s {
	case ScheduleCron:
		return "cron"
	case ScheduleOnce:
		return "datetime-local"
	}
	return ""
}

const (
	ScheduleImmediate ScheduleType = "Immediate"
	ScheduleOnce      ScheduleType = "Future Date"
	ScheduleCron      ScheduleType = "Cron"
)

var (
	ScheduleTypes = []ScheduleType{ScheduleImmediate, ScheduleOnce, ScheduleCron}
)

// Schedule represents the interval on which a job should be run. Only
// Type and one other field should be set.
type Schedule struct {
	Once     time.Time
	Duration time.Duration
	Cron     string
	Type     ScheduleType
}

// NewScheduler returns a Scheduler ready to run jobs.
func NewScheduler(db db.PGDBTX, w *Worker) *Scheduler {
	c := cron.New()
	c.Start()
	return &Scheduler{
		w:    w,
		cron: c,
		db:   db,
	}
}

type Scheduler struct {
	w    *Worker
	cron *cron.Cron
	db   db.PGDBTX
}

// Create schedules a job and records it in the database.
func (s *Scheduler) Create(ctx context.Context, sched Schedule, workflowName string, params map[string]any) (row db.Schedule, err error) {
	def := s.w.dh.Definition(workflowName)
	if def == nil {
		return row, fmt.Errorf("no workflow named %q", workflowName)
	}
	m, err := json.Marshal(params)
	if err != nil {
		return row, err
	}
	// Validate parameters against workflow definition before enqueuing.
	params, err = UnmarshalWorkflow(string(m), def)
	if err != nil {
		return row, err
	}
	err = s.db.BeginFunc(ctx, func(tx pgx.Tx) error {
		now := time.Now()
		q := db.New(tx)
		row, err = q.CreateSchedule(ctx, db.CreateScheduleParams{
			WorkflowName:   workflowName,
			WorkflowParams: sql.NullString{String: string(m), Valid: len(m) > 0},
			Once:           sched.Once,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
		if err != nil {
			return err
		}
		return nil
	})
	s.cron.Schedule(&RunOnce{next: sched.Once}, &WorkflowSchedule{Schedule: row, worker: s.w, Params: params})
	return row, err
}

// Resume fetches schedules from the database and schedules them.
func (s *Scheduler) Resume(ctx context.Context) error {
	q := db.New(s.db)
	scheds, err := q.Schedules(ctx)
	if err != nil {
		return err
	}
	for _, sched := range scheds {
		def := s.w.dh.Definition(sched.WorkflowName)
		if def == nil {
			log.Printf("Unable to schedule %q (schedule.id: %d): no definition found", sched.WorkflowName, sched.ID)
			continue
		}
		params, err := UnmarshalWorkflow(sched.WorkflowParams.String, def)
		if err != nil {
			log.Printf("Error in UnmarshalWorkflow(%q, %q) for schedule %d: %q", sched.WorkflowParams.String, sched.WorkflowName, sched.ID, err)
			continue
		}
		s.cron.Schedule(&RunOnce{next: sched.Once}, &WorkflowSchedule{
			Schedule: sched,
			Params:   params,
			worker:   s.w,
		})
	}
	return nil
}

// Entries returns a slice of active jobs.
func (s *Scheduler) Entries() []ScheduleEntry {
	entries := s.cron.Entries()
	ret := make([]ScheduleEntry, len(entries))
	for i, e := range s.cron.Entries() {
		ret[i] = (ScheduleEntry)(e)
	}
	return ret
}

type ScheduleEntry cron.Entry

// WorkflowJob returns a *WorkflowSchedule for the ScheduleEntry.
func (s *ScheduleEntry) WorkflowJob() *WorkflowSchedule {
	return s.Job.(*WorkflowSchedule)
}

// WorkflowSchedule represents the data needed to create a Workflow.
type WorkflowSchedule struct {
	Schedule db.Schedule
	Params   map[string]any
	worker   *Worker
}

// Run starts a Workflow.
func (w *WorkflowSchedule) Run() {
	id, err := w.worker.StartWorkflow(context.Background(), w.Schedule.WorkflowName, w.Params, int(w.Schedule.ID))
	log.Printf("StartWorkflow(_, %q, %v, %d) = %q, %q", w.Schedule.WorkflowName, w.Params, w.Schedule.ID, id, err)
}

// RunOnce is a cron.Schedule for running a job at a specific time.
type RunOnce struct {
	next time.Time
}

// Next returns the next time a job should run.
func (r *RunOnce) Next(t time.Time) time.Time {
	if t.After(r.next) {
		return time.Time{}
	}
	return r.next
}

var _ cron.Schedule = &RunOnce{}
