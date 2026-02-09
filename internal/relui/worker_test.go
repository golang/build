// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	ctx := t.Context()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	wg := sync.WaitGroup{}
	dh := NewDefinitionHolder()
	w := NewWorker(dh, dbp, &testWorkflowListener{
		Listener:   &PGListener{DB: dbp},
		onFinished: wg.Done,
	})

	wd := newTestEchoWorkflow()
	dh.RegisterDefinition(t.Name(), wd)
	params := map[string]any{"greeting": "greetings", "names": []string{"alice", "bob"}}

	wg.Add(1)
	wfid, err := w.StartWorkflow(ctx, t.Name(), params, 0)
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
		ID: wfid,
		// Params ignored: nondeterministic serialization
		Name:      nullString(t.Name()),
		Output:    `{"echo": "greetings alice bob"}`,
		Finished:  true,
		CreatedAt: time.Now(), // cmpopts.EquateApproxTime
		UpdatedAt: time.Now(), // cmpopts.EquateApproxTime
	}}
	if diff := cmp.Diff(wantWfs, wfs, cmpopts.EquateApproxTime(time.Minute), cmpopts.IgnoreFields(db.Workflow{}, "Params")); diff != "" {
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
			Started:    true,
			Finished:   true,
			Result:     nullString(`"greetings alice bob"`),
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
	ctx := t.Context()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	wg := sync.WaitGroup{}
	dh := NewDefinitionHolder()
	w := NewWorker(dh, dbp, &testWorkflowListener{
		Listener:   &PGListener{DB: dbp},
		onFinished: wg.Done,
	})

	wd := newTestEchoWorkflow()
	dh.RegisterDefinition(t.Name(), wd)
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
		Started:    true,
		Finished:   true,
		Result:     nullString(`"hello alice bob"`),
		Error:      sql.NullString{},
		CreatedAt:  time.Now(), // cmpopts.EquateApproxTime
		UpdatedAt:  time.Now(), // cmpopts.EquateApproxTime
	}}
	if diff := cmp.Diff(want, tasks, cmpopts.EquateApproxTime(time.Minute)); diff != "" {
		t.Errorf("q.TasksForWorkflow(_, %q) mismatch (-want +got):\n%s", wfid, diff)
	}
}

func TestWorkerResumeMissingDefinition(t *testing.T) {
	ctx := t.Context()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	w := NewWorker(NewDefinitionHolder(), dbp, &PGListener{DB: dbp})

	cwp := db.CreateWorkflowParams{ID: uuid.New(), Name: nullString(t.Name()), Params: nullString("{}")}
	if wf, err := q.CreateWorkflow(ctx, cwp); err != nil {
		t.Fatalf("q.CreateWorkflow(_, %v) = %v, %v, wanted no error", cwp, wf, err)
	}

	if err := w.Resume(ctx, cwp.ID); err == nil {
		t.Fatalf("w.Resume(_, %q) = %v, wanted error", cwp.ID, err)
	}
}

func TestWorkflowResumeAll(t *testing.T) {
	ctx := t.Context()
	dbp := testDB(ctx, t)
	q := db.New(dbp)
	wg := sync.WaitGroup{}
	dh := NewDefinitionHolder()
	w := NewWorker(dh, dbp, &testWorkflowListener{
		Listener:   &PGListener{DB: dbp},
		onFinished: wg.Done,
	})

	wd := newTestEchoWorkflow()
	dh.RegisterDefinition(t.Name(), wd)
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
	want := []db.TasksRow{
		{
			WorkflowID:       wfid1,
			Name:             "echo",
			Started:          true,
			Finished:         true,
			Result:           nullString(`"hello alice bob"`),
			Error:            sql.NullString{},
			CreatedAt:        time.Now(), // cmpopts.EquateApproxTime
			UpdatedAt:        time.Now(), // cmpopts.EquateApproxTime
			MostRecentUpdate: time.Now(),
		},
		{
			WorkflowID:       wfid2,
			Name:             "echo",
			Started:          true,
			Finished:         true,
			Result:           nullString(`"hello alice bob"`),
			Error:            sql.NullString{},
			CreatedAt:        time.Now(), // cmpopts.EquateApproxTime
			UpdatedAt:        time.Now(), // cmpopts.EquateApproxTime
			MostRecentUpdate: time.Now(),
		},
	}
	sort := cmpopts.SortSlices(func(x, y db.TasksRow) bool {
		return x.WorkflowID.String() < y.WorkflowID.String()
	})
	if diff := cmp.Diff(want, tasks, cmpopts.EquateApproxTime(time.Minute), sort); diff != "" {
		t.Errorf("q.Tasks() mismatch (-want +got):\n%s", diff)
	}
}

func TestWorkflowResumeRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbp := testDB(ctx, t)
	dh := NewDefinitionHolder()
	w := NewWorker(dh, dbp, &PGListener{DB: dbp})

	counter := 0
	blockingChan := make(chan bool)
	wd := workflow.New(workflow.ACL{})
	nothing := workflow.Task0(wd, "needs retry", func(ctx context.Context) (string, error) {
		// Send twice so that the test can stop us mid-execution.
		for range 2 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case blockingChan <- true:
				counter++
			}
		}
		if counter > 4 {
			return "", nil
		}
		return "", errors.New("expected")
	})
	workflow.Output(wd, "nothing", nothing)
	dh.RegisterDefinition(t.Name(), wd)

	// Run the workflow. It will try the task up to 3 times. Stop the worker
	// during its second run, then resume it and verify the task retries.
	go func() {
		for range 3 {
			<-blockingChan
		}
		cancel()
	}()
	wfid, err := w.StartWorkflow(ctx, t.Name(), nil, 0)
	if err != nil {
		t.Fatalf("w.StartWorkflow(_, %v, %v) = %v, %v, wanted no error", wd, nil, wfid, err)
	}
	w.Run(ctx)
	if counter != 3 {
		t.Fatalf("task sent %v times, wanted 3", counter)
	}

	t.Log("Restarting worker")
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	wfDone := make(chan bool, 1)
	w = NewWorker(dh, dbp, &testWorkflowListener{
		Listener:   &PGListener{DB: dbp},
		onFinished: func() { wfDone <- true },
	})

	go func() {
		for {
			select {
			case <-blockingChan:
			case <-ctx.Done():
				return
			}
		}
	}()
	go w.Run(ctx)
	if err := w.Resume(ctx, wfid); err != nil {
		t.Fatalf("w.Resume(_, %v) = %v, wanted no error", wfid, err)
	}
	<-wfDone
}

func newTestEchoWorkflow() *workflow.Definition {
	wd := workflow.New(workflow.ACL{})
	echo := func(ctx context.Context, greeting string, names []string) (string, error) {
		return fmt.Sprintf("%v %v", greeting, strings.Join(names, " ")), nil
	}
	greeting := workflow.Param(wd, workflow.ParamDef[string]{Name: "greeting"})
	names := workflow.Param(wd, workflow.ParamDef[[]string]{Name: "names", ParamType: workflow.SliceShort})
	workflow.Output(wd, "echo", workflow.Task2(wd, "echo", echo, greeting, names))
	return wd
}

func createUnfinishedEchoWorkflow(t *testing.T, ctx context.Context, q *db.Queries) uuid.UUID {
	t.Helper()
	cwp := db.CreateWorkflowParams{ID: uuid.New(), Name: nullString(t.Name()), Params: nullString(`{"greeting": "hello", "names": ["alice", "bob"]}`)}
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
	onStalled  func()
}

func (t *testWorkflowListener) WorkflowFinished(ctx context.Context, wfid uuid.UUID, outputs map[string]any, wferr error) error {
	err := t.Listener.WorkflowFinished(ctx, wfid, outputs, wferr)
	if t.onFinished != nil {
		t.onFinished()
	}
	return err
}

func (t *testWorkflowListener) WorkflowStalled(workflowID uuid.UUID) error {
	err := t.Listener.WorkflowStalled(workflowID)
	if t.onStalled != nil {
		t.onStalled()
	}
	return err
}
