// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"golang.org/x/build/internal/relui/db"
	"golang.org/x/build/internal/workflow"
)

// listener implements workflow.Listener for recording workflow state.
type listener struct {
	db *pgxpool.Pool
}

// TaskStateChanged is called whenever a task is updated by the
// workflow. The workflow.TaskState is persisted as a db.Task,
// creating or updating a row as necessary.
func (l *listener) TaskStateChanged(workflowID uuid.UUID, taskName string, state *workflow.TaskState) error {
	log.Printf("TaskStateChanged(%q, %q, %v)", workflowID, taskName, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := json.Marshal(state.Result)
	if err != nil {
		return err
	}
	err = l.db.BeginFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		updated := time.Now()
		_, err := q.UpsertTask(ctx, db.UpsertTaskParams{
			WorkflowID: workflowID,
			Name:       taskName,
			Finished:   state.Finished,
			Result:     sql.NullString{String: string(result), Valid: len(result) > 0},
			Error:      sql.NullString{},
			CreatedAt:  updated,
			UpdatedAt:  updated,
		})
		return err
	})
	if err != nil {
		log.Printf("TaskStateChanged(%q, %q, %v) = %v", workflowID, taskName, state, err)
	}
	return err
}

func (l *listener) Logger(workflowID uuid.UUID, taskName string) workflow.Logger {
	return &postgresLogger{
		db:         l.db,
		workflowID: workflowID,
		taskName:   taskName,
	}
}

// postgresLogger logs task output to the database. It implements workflow.Logger.
type postgresLogger struct {
	db         *pgxpool.Pool
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
