// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/workflow"
)

func TestWorkerStartWorkflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	wg := sync.WaitGroup{}
	w := NewWorker(dbp, &testWorkflowListener{
		Listener:   &PGListener{dbp},
		onFinished: wg.Done,
	})

	wd := newTestEchoWorkflow()
	RegisterDefinition(t.Name(), wd)
	params := map[string]string{"echo": "greetings"}

	wg.Add(1)
	wfid, err := w.StartWorkflow(ctx, t.Name(), wd, params)
	if err != nil {
		t.Fatalf("w.StartWorkflow(_, %v, %v) = %v, %v, wanted no error", wd, params, wfid, err)
	}
	go w.Run(ctx)
	wg.Wait()

	wfs, err := q.Workflows(ctx)
	if err != nil {
		t.Fatalf("q.Workflows() = %v, %v, wanted no error", wfs, err)
	}
	wantWfs := []db.Workflow{{
		ID:        wfid,
		Params:    nullString(`{"echo": "greetings"}`),
		Name:      nullString(t.Name()),
		Output:    `{"echo": "greetings"}`,
		Finished:  true,
		CreatedAt: time.Now(), // cmpopts.EquateApproxTime
		UpdatedAt: time.Now(), // cmpopts.EquateApproxTime
	}}
	if diff := cmp.Diff(wantWfs, wfs, cmpopts.EquateApproxTime(time.Minute)); diff != "" {
		t.Fatalf("q.Workflows() mismatch (-want +got):\n%s", diff)
	}
	tasks, err := q.TasksForWorkflow(ctx, wfid)
	if err != nil {
		t.Fatalf("q.TasksForWorkflow(_, %v) = %v, %v, wanted no error", wfid, tasks, err)
	}
	want := []db.Task{
		{
			WorkflowID: wfid,
			Name:       "echo",
			Finished:   true,
			Result:     nullString(`"greetings"`),
			Error:      sql.NullString{},
			CreatedAt:  time.Now(), // cmpopts.EquateApproxTime
			UpdatedAt:  time.Now(), // cmpopts.EquateApproxTime
		},
	}
	if diff := cmp.Diff(want, tasks, cmpopts.EquateApproxTime(time.Minute)); diff != "" {
		t.Errorf("q.TasksForWorkflow(_, %q) mismatch (-want +got):\n%s", wfid, diff)
	}
}

func TestWorkerResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	wg := sync.WaitGroup{}
	w := NewWorker(dbp, &testWorkflowListener{
		Listener:   &PGListener{dbp},
		onFinished: wg.Done,
	})

	wd := newTestEchoWorkflow()
	RegisterDefinition(t.Name(), wd)
	wfid := createUnfinishedEchoWorkflow(t, ctx, q)

	wg.Add(1)
	go w.Run(ctx)
	if err := w.Resume(ctx, wfid); err != nil {
		t.Fatalf("w.Resume(_, %v) = %v, wanted no error", wfid, err)
	}
	wg.Wait()

	tasks, err := q.TasksForWorkflow(ctx, wfid)
	if err != nil {
		t.Fatalf("q.TasksForWorkflow(_, %v) = %v, %v, wanted no error", wfid, tasks, err)
	}
	want := []db.Task{{
		WorkflowID: wfid,
		Name:       "echo",
		Finished:   true,
		Result:     nullString(`"hello"`),
		Error:      sql.NullString{},
		CreatedAt:  time.Now(), // cmpopts.EquateApproxTime
		UpdatedAt:  time.Now(), // cmpopts.EquateApproxTime
	}}
	if diff := cmp.Diff(want, tasks, cmpopts.EquateApproxTime(time.Minute)); diff != "" {
		t.Errorf("q.TasksForWorkflow(_, %q) mismatch (-want +got):\n%s", wfid, diff)
	}
}

func TestWorkerResumeMissingDefinition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	w := NewWorker(dbp, &PGListener{dbp})

	cwp := db.CreateWorkflowParams{ID: uuid.New(), Name: nullString(t.Name()), Params: nullString("{}")}
	if wf, err := q.CreateWorkflow(ctx, cwp); err != nil {
		t.Fatalf("q.CreateWorkflow(_, %v) = %v, %v, wanted no error", cwp, wf, err)
	}

	if err := w.Resume(ctx, cwp.ID); err == nil {
		t.Fatalf("w.Resume(_, %q) = %v, wanted error", cwp.ID, err)
	}
}

func TestWorkflowResumeAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	wg := sync.WaitGroup{}
	w := NewWorker(dbp, &testWorkflowListener{
		Listener:   &PGListener{dbp},
		onFinished: wg.Done,
	})

	wd := newTestEchoWorkflow()
	RegisterDefinition(t.Name(), wd)
	wfid1 := createUnfinishedEchoWorkflow(t, ctx, q)
	wfid2 := createUnfinishedEchoWorkflow(t, ctx, q)

	wg.Add(2)
	go w.Run(ctx)
	if err := w.ResumeAll(ctx); err != nil {
		t.Fatalf("w.ResumeAll() = %v, wanted no error", err)
	}
	wg.Wait()

	tasks, err := q.Tasks(ctx)
	if err != nil {
		t.Fatalf("q.Tasks() = %v, %v, wanted no error", tasks, err)
	}
	want := []db.Task{
		{
			WorkflowID: wfid1,
			Name:       "echo",
			Finished:   true,
			Result:     nullString(`"hello"`),
			Error:      sql.NullString{},
			CreatedAt:  time.Now(), // cmpopts.EquateApproxTime
			UpdatedAt:  time.Now(), // cmpopts.EquateApproxTime
		},
		{
			WorkflowID: wfid2,
			Name:       "echo",
			Finished:   true,
			Result:     nullString(`"hello"`),
			Error:      sql.NullString{},
			CreatedAt:  time.Now(), // cmpopts.EquateApproxTime
			UpdatedAt:  time.Now(), // cmpopts.EquateApproxTime
		},
	}
	if diff := cmp.Diff(want, tasks, cmpopts.EquateApproxTime(time.Minute)); diff != "" {
		t.Errorf("q.Tasks() mismatch (-want +got):\n%s", diff)
	}
}

func TestWorkerRunListenerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	w := NewWorker(dbp, &unimplementedListener{})

	wd := newTestEchoWorkflow()
	RegisterDefinition(t.Name(), wd)
	wfid := createUnfinishedEchoWorkflow(t, ctx, q)

	if err := w.Resume(ctx, wfid); err != nil {
		t.Fatalf("w.Resume(_, %v) = %v, wanted no error", wfid, err)
	}

	if err := w.Run(ctx); err == nil {
		t.Fatalf("w.Run() = %v, wanted error", err)
	}
}

func newTestEchoWorkflow() *workflow.Definition {
	wd := workflow.New()
	echo := func(ctx context.Context, arg string) (string, error) {
		return arg, nil
	}
	wd.Output("echo", wd.Task("echo", echo, wd.Parameter("echo")))
	return wd
}

func createUnfinishedEchoWorkflow(t *testing.T, ctx context.Context, q *db.Queries) uuid.UUID {
	t.Helper()
	cwp := db.CreateWorkflowParams{ID: uuid.New(), Name: nullString(t.Name()), Params: nullString(`{"echo": "hello"}`)}
	if wf, err := q.CreateWorkflow(ctx, cwp); err != nil {
		t.Fatalf("q.CreateWorkflow(_, %v) = %v, %v, wanted no error", cwp, wf, err)
	}
	cwt := db.CreateTaskParams{WorkflowID: cwp.ID, Name: "echo", Result: nullString("null"), CreatedAt: time.Now()}
	if wt, err := q.CreateTask(ctx, cwt); err != nil {
		t.Fatalf("q.CreateWorkflowTask(_, %v) = %v, %v, wanted no error", cwt, wt, err)
	}
	return cwp.ID
}

type testWorkflowListener struct {
	Listener

	onFinished func()
}

func (t *testWorkflowListener) WorkflowFinished(ctx context.Context, wfid uuid.UUID, outputs map[string]interface{}, err error) error {
	defer t.onFinished()
	return t.Listener.WorkflowFinished(ctx, wfid, outputs, err)
}

type unimplementedListener struct {
}

func (u *unimplementedListener) TaskStateChanged(uuid.UUID, string, *workflow.TaskState) error {
	return errors.New("method TaskStateChanged not implemented")
}

func (u *unimplementedListener) Logger(uuid.UUID, string) workflow.Logger {
	return log.Default()
}

func (u *unimplementedListener) WorkflowStarted(context.Context, uuid.UUID, string, map[string]string) error {
	return errors.New("method WorkflowStarted not implemented")
}

func (u *unimplementedListener) WorkflowFinished(context.Context, uuid.UUID, map[string]interface{}, error) error {
	return errors.New("method WorkflowFinished not implemented")
}
