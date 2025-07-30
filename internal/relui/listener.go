// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
)

// PGListener implements workflow.Listener for recording workflow state.
type PGListener struct {
	DB db.PGDBTX

	BaseURL *url.URL

	ScheduleFailureMailHeader task.MailHeader
	SendMail                  func(task.MailHeader, task.MailContent) error // Optional.

	templ *template.Template
}

// WorkflowStalled is called when no tasks are runnable.
func (l *PGListener) WorkflowStalled(workflowID uuid.UUID) error {
	if l.SendMail == nil {
		// Not configured to send mail. Nothing to do.
		return nil
	}

	// Send mail notifying about workflow failure.
	wf, err := db.New(l.DB).Workflow(context.Background(), workflowID)
	if err != nil || wf.ScheduleID.Int32 == 0 {
		return err
	}
	var buf bytes.Buffer
	body := scheduledFailureEmailBody{Workflow: wf}
	if err := l.template("scheduled_workflow_failure_email.txt").Execute(&buf, body); err != nil {
		log.Printf("WorkflowFinished: Execute(_, %v) = %q", body, err)
		return err
	}
	return l.SendMail(l.ScheduleFailureMailHeader, task.MailContent{
		Subject:  fmt.Sprintf("[relui] Scheduled workflow %q failed", wf.Name.String),
		BodyText: buf.String(),
	})
}

// TaskStateChanged is called whenever a task is updated by the
// workflow. The workflow.TaskState is persisted as a db.Task,
// creating or updating a row as necessary.
func (l *PGListener) TaskStateChanged(workflowID uuid.UUID, taskName string, state *workflow.TaskState) error {
	log.Printf("TaskStateChanged(%q, %q, %#v)", workflowID, taskName, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := json.Marshal(state.Result)
	if err != nil {
		return err
	}
	err = l.DB.BeginFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		updated := time.Now()
		_, err := q.UpsertTask(ctx, db.UpsertTaskParams{
			WorkflowID: workflowID,
			Name:       taskName,
			Started:    state.Started,
			Finished:   state.Finished,
			Result:     sql.NullString{String: string(result), Valid: len(result) > 0},
			Error:      sql.NullString{String: state.Error, Valid: state.Error != ""},
			CreatedAt:  updated,
			UpdatedAt:  updated,
			RetryCount: int32(state.RetryCount),
		})
		return err
	})
	if err != nil {
		log.Printf("TaskStateChanged(%q, %q, %#v) = %v", workflowID, taskName, state, err)
	}
	return err
}

// WorkflowStarted persists a new workflow execution in the database.
func (l *PGListener) WorkflowStarted(ctx context.Context, workflowID uuid.UUID, name string, params map[string]interface{}, scheduleID int) error {
	q := db.New(l.DB)
	m, err := json.Marshal(params)
	if err != nil {
		return err
	}
	updated := time.Now()
	wfp := db.CreateWorkflowParams{
		ID:         workflowID,
		Name:       sql.NullString{String: name, Valid: true},
		Params:     sql.NullString{String: string(m), Valid: len(m) > 0},
		ScheduleID: sql.NullInt32{Int32: int32(scheduleID), Valid: scheduleID != 0},
		CreatedAt:  updated,
		UpdatedAt:  updated,
	}
	_, err = q.CreateWorkflow(ctx, wfp)
	return err
}

type scheduledFailureEmailBody struct {
	Workflow db.Workflow
	Err      error
}

// WorkflowFinished saves the final state of a workflow after its run
// has completed.
func (l *PGListener) WorkflowFinished(ctx context.Context, workflowID uuid.UUID, outputs map[string]interface{}, workflowErr error) error {
	log.Printf("WorkflowFinished(%q, %v, %q)", workflowID, outputs, workflowErr)
	q := db.New(l.DB)
	m, err := json.Marshal(outputs)
	if err != nil {
		return err
	}
	wp := db.WorkflowFinishedParams{
		ID:        workflowID,
		Finished:  true,
		Output:    string(m),
		UpdatedAt: time.Now(),
	}
	if workflowErr != nil {
		wp.Error = workflowErr.Error()
	}
	_, err = q.WorkflowFinished(ctx, wp)
	return err
}

func (l *PGListener) template(name string) *template.Template {
	if l.templ == nil {
		helpers := map[string]any{"baseLink": l.baseLink}
		l.templ = template.Must(template.New("").Funcs(helpers).ParseFS(templates, "templates/*.txt"))
	}
	return l.templ.Lookup(name)
}

func (l *PGListener) baseLink(target string, extras ...string) string {
	return BaseLink(l.BaseURL)(target, extras...)
}

func (l *PGListener) Logger(workflowID uuid.UUID, taskName string) workflow.Logger {
	return &postgresLogger{
		db:         l.DB,
		workflowID: workflowID,
		taskName:   taskName,
	}
}

// postgresLogger logs task output to the database. It implements workflow.Logger.
type postgresLogger struct {
	db         db.PGDBTX
	workflowID uuid.UUID
	taskName   string
}

func (l *postgresLogger) Printf(format string, v ...interface{}) {
	ctx := context.Background()
	err := l.db.BeginFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		body := fmt.Sprintf(format, v...)
		_, err := q.CreateTaskLog(ctx, db.CreateTaskLogParams{
			WorkflowID: l.workflowID,
			TaskName:   l.taskName,
			Body:       body,
		})
		if err != nil {
			log.Printf("q.CreateTaskLog(%v, %v, %q) = %v", l.workflowID, l.taskName, body, err)
		}
		return err
	})
	if err != nil {
		log.Printf("l.Printf(%q, %v) = %v", format, v, err)
	}
}

func LogOnlyMailer(header task.MailHeader, content task.MailContent) error {
	log.Println("Logging but not sending mail:", header, content)
	return nil
}
