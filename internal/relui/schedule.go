// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
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
	Once time.Time
	Cron string
	Type ScheduleType
}

func (s Schedule) Parse() (cron.Schedule, error) {
	if err := s.Valid(); err != nil {
		return nil, err
	}
	switch s.Type {
	case ScheduleOnce:
		return &RunOnce{next: s.Once}, nil
	case ScheduleCron:
		return cron.ParseStandard(s.Cron)
	}
	return nil, fmt.Errorf("unschedulable Schedule.Type %q", s.Type)
}

func (s Schedule) Valid() error {
	switch s.Type {
	case ScheduleOnce:
		if s.Once.IsZero() {
			return fmt.Errorf("time not set for %q", ScheduleOnce)
		}
		return nil
	case ScheduleCron:
		_, err := cron.ParseStandard(s.Cron)
		return err
	case ScheduleImmediate:
		return nil
	}
	return fmt.Errorf("invalid ScheduleType %q", s.Type)
}

func (s *Schedule) setType() {
	switch {
	case !s.Once.IsZero():
		s.Type = ScheduleOnce
	case s.Cron != "":
		s.Type = ScheduleCron
	default:
		s.Type = ScheduleImmediate
	}
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

	// failed is populated by Resume with schedules that
	// failed to resume due to a change in workflow definition.
	failedMu sync.Mutex
	failed   []FailedToScheduleEntry
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
	cronSched, err := sched.Parse()
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
			Spec:           sched.Cron,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
		if err != nil {
			return err
		}
		return nil
	})
	s.cron.Schedule(cronSched, &WorkflowSchedule{Schedule: row, worker: s.w, Params: params})
	return row, err
}

// Resume fetches schedules from the database and schedules them.
func (s *Scheduler) Resume(ctx context.Context) error {
	q := db.New(s.db)
	rows, err := q.Schedules(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		def := s.w.dh.Definition(row.WorkflowName)
		if def == nil {
			log.Printf("Unable to schedule %q (schedule.id: %d): no definition found", row.WorkflowName, row.ID)
			continue
		}
		sched := Schedule{Once: row.Once, Cron: row.Spec}
		sched.setType()
		if sched.Type == ScheduleOnce && row.Once.Before(time.Now()) {
			log.Printf("Skipping %q Schedule (schedule.id: %d): %q is in the past", sched.Type, row.ID, sched.Once.String())
			continue
		}
		cronSched, err := sched.Parse()
		if err != nil {
			log.Printf("Unable to schedule %q (schedule.id %d): invalid Schedule: %q", row.WorkflowName, row.ID, err)
			continue
		}
		params, err := UnmarshalWorkflow(row.WorkflowParams.String, def)
		if err != nil {
			log.Printf("Error in UnmarshalWorkflow(%q, %q) for schedule %d: %q", row.WorkflowParams.String, row.WorkflowName, row.ID, err)
			s.failed = append(s.failed, FailedToScheduleEntry{
				WorkflowSchedule: WorkflowSchedule{Schedule: row},
				ParsedSchedule:   cronSched,
			})
			continue
		}

		s.cron.Schedule(cronSched, &WorkflowSchedule{
			Schedule: row,
			Params:   params,
			worker:   s.w,
		})
	}
	return nil
}

// Entries returns a list of scheduled jobs, and a list of jobs that failed to schedule.
//
// The scheduled jobs are filtered by workflowNames, if provided, otherwise all scheduled
// jobs are included.
func (s *Scheduler) Entries(ctx context.Context, workflowNames ...string) ([]ScheduleEntry, []FailedToScheduleEntry) {
	q := db.New(s.db)
	rows, err := q.SchedulesLastRun(ctx)
	if err != nil {
		log.Println("leaving out last runs of scheduled workflows because SchedulesLastRun failed:", err)
		rows = nil
	}
	rowMap := make(map[int32]db.SchedulesLastRunRow)
	for _, row := range rows {
		rowMap[row.ID] = row
	}
	entries := s.cron.Entries()
	ses := make([]ScheduleEntry, 0, len(entries))
	for _, e := range s.cron.Entries() {
		entry := ScheduleEntry{Entry: e}
		if len(workflowNames) != 0 && !slices.Contains(workflowNames, entry.WorkflowJob().Schedule.WorkflowName) {
			continue
		}
		if row, ok := rowMap[entry.WorkflowJob().Schedule.ID]; ok {
			entry.LastRun = row
		}
		ses = append(ses, entry)
	}
	s.failedMu.Lock()
	fes := slices.Clone(s.failed)
	s.failedMu.Unlock()
	for i, fe := range fes {
		if row, ok := rowMap[fe.Schedule.ID]; ok {
			fes[i].LastRun = row
		}
	}
	return ses, fes
}

var ErrScheduleNotFound = errors.New("schedule not found")

// Delete removes a schedule from the scheduler, preventing subsequent
// runs, and deletes the schedule from the database.
//
// Jobs in progress are not interrupted, but will be prevented from
// starting again.
func (s *Scheduler) Delete(ctx context.Context, id int) error {
	// Look for a schedule with the given id, which may have been either successfully or unsuccessfully scheduled,
	// and delete it from memory.
	entries := s.cron.Entries()
	s.failedMu.Lock()
	defer s.failedMu.Unlock()
	i := slices.IndexFunc(entries, func(e cron.Entry) bool { return int(e.Job.(*WorkflowSchedule).Schedule.ID) == id })
	j := slices.IndexFunc(s.failed, func(e FailedToScheduleEntry) bool { return int(e.Schedule.ID) == id })
	switch {
	case i >= 0 && j == -1:
		entry := entries[i]
		s.cron.Remove(entry.ID)
	case j >= 0 && i == -1:
		s.failed = slices.Delete(s.failed, j, j+1)
	case i == -1 && j == -1:
		// No such schedule found.
		return ErrScheduleNotFound
	default:
		return fmt.Errorf("internal error: Scheduler.Delete: impossible case i == %d, j == %d", i, j)
	}

	// Next, delete the schedule with the specified id from the database.
	return s.db.BeginFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.ClearWorkflowSchedule(ctx, int32(id)); err != nil {
			return err
		}
		if _, err := q.DeleteSchedule(ctx, int32(id)); err != nil {
			return err
		}
		return nil
	})
}

type ScheduleEntry struct {
	// Entry is the cron entry.
	//
	// Its Job field holds a *WorkflowSchedule that describes the scheduled
	// workflow and how to start running a new instance of it.
	cron.Entry

	LastRun db.SchedulesLastRunRow
}

type FailedToScheduleEntry struct {
	WorkflowSchedule
	ParsedSchedule cron.Schedule
	LastRun        db.SchedulesLastRunRow
}

func (e FailedToScheduleEntry) Next() time.Time { return e.ParsedSchedule.Next(time.Now()) }

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

// ScheduleDesc returns a description of the schedule.
func (w WorkflowSchedule) ScheduleDesc() string {
	switch {
	case !w.Schedule.Once.IsZero():
		return "Run once on the future date."
	case w.Schedule.Spec != "":
		return fmt.Sprintf("Using the cron schedule %q.", w.Schedule.Spec)
	default:
		return ""
	}
}

// ParamDesc returns a description of the parameters.
func (w WorkflowSchedule) ParamDesc() string {
	if w.Params == nil {
		// If parameters couldn't be unmarshaled, it's because the workflow definition changed.
		// The best description we can offer are the originally marshaled parameters, indented.
		var buf bytes.Buffer
		err := json.Indent(&buf, []byte(w.Schedule.WorkflowParams.String), "", "\t")
		if err != nil {
			return "failed to indent workflow parameters: " + err.Error()
		}
		return buf.String()
	}

	b, err := json.MarshalIndent(w.Params, "", "\t")
	if err != nil {
		return "failed to marshal workflow parameters: " + err.Error()
	}
	return string(b)
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
