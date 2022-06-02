// Copyright 2021 The Go Authors. All rights reserved.
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
	"github.com/google/uuid"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/workflow"
)

func TestListenerTaskStateChanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	q := db.New(dbp)

	cases := []struct {
		desc  string
		state *workflow.TaskState
		want  []db.Task
	}{
		{
			desc: "records successful tasks",
			state: &workflow.TaskState{
				Name:             "TestTask",
				Finished:         true,
				Result:           struct{ Value int }{5},
				SerializedResult: []byte(`{"Value": 5}`),
				Error:            "",
			},
			want: []db.Task{
				{
					Name:      "TestTask",
					Finished:  true,
					Result:    sql.NullString{String: `{"Value": 5}`, Valid: true},
					CreatedAt: time.Now(), // cmpopts.EquateApproxTime
					UpdatedAt: time.Now(), // cmpopts.EquateApproxTime
				},
			},
		},
		{
			desc: "records failing tasks",
			state: &workflow.TaskState{
				Name:             "TestTask",
				Finished:         true,
				Result:           struct{ Value int }{5},
				SerializedResult: []byte(`{"Value": 5}`),
				Error:            "it's completely broken and hopeless",
			},
			want: []db.Task{
				{
					Name:      "TestTask",
					Finished:  true,
					Result:    sql.NullString{String: `{"Value": 5}`, Valid: true},
					Error:     sql.NullString{String: "it's completely broken and hopeless", Valid: true},
					CreatedAt: time.Now(), // cmpopts.EquateApproxTime
					UpdatedAt: time.Now(), // cmpopts.EquateApproxTime
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			wfp := db.CreateWorkflowParams{ID: uuid.New()}
			wf, err := q.CreateWorkflow(ctx, wfp)
			if err != nil {
				t.Fatalf("q.CreateWorkflow(%v, %v) = %v, wanted no error", ctx, wfp, err)
			}

			l := &PGListener{db: dbp}
			err = l.TaskStateChanged(wf.ID, "TestTask", c.state)
			if err != nil {
				t.Fatalf("l.TaskStateChanged(%v, %q, %v) = %v, wanted no error", wf.ID, "TestTask", c.state, err)
			}

			tasks, err := q.TasksForWorkflow(ctx, wf.ID)
			if err != nil {
				t.Fatalf("q.TasksForWorkflow(%v, %v) = %v, %v, wanted no error", ctx, wf.ID, tasks, err)
			}
			if diff := cmp.Diff(c.want, tasks, cmpopts.EquateApproxTime(time.Minute), cmpopts.IgnoreFields(db.Task{}, "WorkflowID")); diff != "" {
				t.Errorf("q.TasksForWorkflow(_, %q) mismatch (-want +got):\n%s", wf.ID, diff)
			}
		})
	}
}

func TestListenerLogger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	q := db.New(dbp)

	wfp := db.CreateWorkflowParams{ID: uuid.New()}
	wf, err := q.CreateWorkflow(ctx, wfp)
	if err != nil {
		t.Fatalf("q.CreateWorkflow(%v, %v) = %v, wanted no error", ctx, wfp, err)
	}
	params := db.UpsertTaskParams{WorkflowID: wf.ID, Name: "TestTask"}
	_, err = q.UpsertTask(ctx, params)
	if err != nil {
		t.Fatalf("q.UpsertTask(%v, %v) = %v, wanted no error", ctx, params, err)
	}

	l := &PGListener{db: dbp}
	l.Logger(wf.ID, "TestTask").Printf("A fancy log line says %q", "hello")

	logs, err := q.TaskLogs(ctx)
	if err != nil {
		t.Fatalf("q.TaskLogs(%v) = %v, wanted no error", ctx, err)
	}
	want := []db.TaskLog{{
		WorkflowID: wf.ID,
		TaskName:   "TestTask",
		Body:       `A fancy log line says "hello"`,
		CreatedAt:  time.Now(), // cmpopts.EquateApproxTime
		UpdatedAt:  time.Now(), // cmpopts.EquateApproxTime
	}}
	if diff := cmp.Diff(want, logs, cmpopts.EquateApproxTime(time.Minute), cmpopts.IgnoreFields(db.TaskLog{}, "ID")); diff != "" {
		t.Errorf("q.TaskLogs(_, %q) mismatch (-want +got):\n%s", wf.ID, diff)
	}
}
