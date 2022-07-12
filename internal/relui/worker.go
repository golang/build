// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/sync/errgroup"
)

type Listener interface {
	workflow.Listener

	WorkflowStarted(ctx context.Context, workflowID uuid.UUID, name string, params map[string]interface{}) error
	WorkflowFinished(ctx context.Context, workflowID uuid.UUID, outputs map[string]interface{}, err error) error
}

type stopFunc func()

// Worker runs workflows, and persists their state.
type Worker struct {
	dh *DefinitionHolder

	db *pgxpool.Pool
	l  Listener

	done    chan struct{}
	pending chan *workflow.Workflow

	mu sync.Mutex
	// running is a set of currently running Workflow ids. Run uses
	// this set to prevent starting a simultaneous execution of a
	// currently running Workflow.
	running map[string]stopFunc
}

// NewWorker returns a Worker ready to accept and run workflows.
func NewWorker(dh *DefinitionHolder, db *pgxpool.Pool, l Listener) *Worker {
	return &Worker{
		dh:      dh,
		db:      db,
		l:       l,
		done:    make(chan struct{}),
		pending: make(chan *workflow.Workflow, 1),
		running: make(map[string]stopFunc),
	}
}

// Run runs started workflows, waiting for new workflows to start.
//
// On context cancellation, Run waits for all running workflows to
// finish.
func (w *Worker) Run(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)
	for {
		select {
		case <-ctx.Done():
			close(w.done)
			if err := eg.Wait(); err != nil {
				return err
			}
			return ctx.Err()
		case wf := <-w.pending:
			eg.Go(func() error {
				runCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				if err := w.markRunning(wf, cancel); err != nil {
					log.Println(err)
					return nil
				}
				defer w.markStopped(wf)

				outputs, err := wf.Run(runCtx, w.l)
				if wfErr := w.l.WorkflowFinished(ctx, wf.ID, outputs, err); wfErr != nil {
					return fmt.Errorf("w.l.WorkflowFinished(_, %q, %v, %q) = %w", wf.ID, outputs, err, wfErr)
				}
				return nil
			})
		}
	}
}

func (w *Worker) markRunning(wf *workflow.Workflow, stop func()) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.running[wf.ID.String()]; ok {
		return fmt.Errorf("workflow %q already running", wf.ID)
	}
	w.running[wf.ID.String()] = stop
	return nil
}

func (w *Worker) markStopped(wf *workflow.Workflow) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.running, wf.ID.String())
}

func (w *Worker) cancelWorkflow(id uuid.UUID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	stop, ok := w.running[id.String()]
	if !ok {
		return ok
	}
	stop()
	return ok
}

func (w *Worker) run(wf *workflow.Workflow) error {
	select {
	case <-w.done:
		return errors.New("worker stopped")
	case w.pending <- wf:
		return nil
	}
}

// StartWorkflow persists and starts running a workflow.
func (w *Worker) StartWorkflow(ctx context.Context, name string, def *workflow.Definition, params map[string]interface{}) (uuid.UUID, error) {
	wf, err := workflow.Start(def, params)
	if err != nil {
		return uuid.UUID{}, err
	}
	if err := w.l.WorkflowStarted(ctx, wf.ID, name, params); err != nil {
		return wf.ID, err
	}
	if err := w.run(wf); err != nil {
		return wf.ID, err
	}
	return wf.ID, err
}

// ResumeAll resumes all workflows with unfinished tasks.
func (w *Worker) ResumeAll(ctx context.Context) error {
	q := db.New(w.db)
	wfs, err := q.UnfinishedWorkflows(ctx)
	if err != nil {
		return fmt.Errorf("q.UnfinishedWorkflows() = _, %w", err)
	}
	for _, wf := range wfs {
		if err := w.Resume(ctx, wf.ID); err != nil {
			log.Printf("w.Resume(_, %q) = %v", wf.ID, err)
		}
	}
	return nil
}

// Resume resumes a workflow.
func (w *Worker) Resume(ctx context.Context, id uuid.UUID) error {
	var err error
	var wf db.Workflow
	var tasks []db.Task
	err = w.db.BeginFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(w.db)
		wf, err = q.Workflow(ctx, id)
		if err != nil {
			return fmt.Errorf("q.Workflow(_, %v) = %w", id, err)
		}
		tasks, err = q.TasksForWorkflow(ctx, id)
		if err != nil {
			return fmt.Errorf("q.TasksForWorkflow(_, %v) = %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	d := w.dh.Definition(wf.Name.String)
	if d == nil {
		err := fmt.Errorf("no workflow named %q", wf.Name.String)
		w.l.WorkflowFinished(ctx, wf.ID, nil, err)
		return err
	}
	state := &workflow.WorkflowState{ID: wf.ID}
	if err := json.Unmarshal([]byte(wf.Params.String), &state.Params); err != nil {
		err := fmt.Errorf("unmarshalling params for %q: %w", id, err)
		w.l.WorkflowFinished(ctx, wf.ID, nil, err)
		return err
	}
	taskStates := make(map[string]*workflow.TaskState)
	for _, t := range tasks {
		ts := &workflow.TaskState{
			Name:     t.Name,
			Finished: t.Finished,
			Error:    t.Error.String,
		}
		// The worker may have crashed, or been re-deployed. Any
		// started but unfinished tasks are in an unknown state.
		// Mark them as such for human review.
		if t.Started && !t.Finished {
			ts.Finished = true
			ts.Error = "task interrupted before completion"
			if t.Error.Valid {
				ts.Error = fmt.Sprintf("%s. Previous error: %s", ts.Error, t.Error.String)
			}
		}
		if t.Result.Valid {
			ts.SerializedResult = []byte(t.Result.String)
		}
		taskStates[t.Name] = ts
	}
	res, err := workflow.Resume(d, state, taskStates)
	if err != nil {
		w.l.WorkflowFinished(ctx, wf.ID, nil, err)
		return err
	}
	return w.run(res)
}
