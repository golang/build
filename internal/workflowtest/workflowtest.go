// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package workflowtest

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	wf "golang.org/x/build/internal/workflow"
)

type Logger struct {
	T    testing.TB
	Task string
}

func (l *Logger) Printf(format string, v ...any) {
	l.T.Logf("%v\ttask %-10v: LOG: %s", time.Now(), l.Task, fmt.Sprintf(format, v...))
}

type VerboseListener struct {
	T              testing.TB
	OutputListener func(string, any)
	OnStall        func()
}

func (l *VerboseListener) WorkflowStalled(workflowID uuid.UUID) error {
	l.T.Logf("workflow %q: stalled", workflowID.String())
	if l.OnStall != nil {
		l.OnStall()
	}
	return nil
}

func (l *VerboseListener) TaskStateChanged(_ uuid.UUID, _ string, st *wf.TaskState) error {
	switch {
	case !st.Finished:
		l.T.Logf("task %-10v: started", st.Name)
	case st.Error != "":
		l.T.Logf("task %-10v: error: %v", st.Name, st.Error)
	default:
		l.T.Logf("task %-10v: done: %v", st.Name, st.Result)
		if l.OutputListener != nil {
			l.OutputListener(st.Name, st.Result)
		}
	}
	return nil
}

func (l *VerboseListener) Logger(_ uuid.UUID, task string) wf.Logger {
	return &Logger{T: l.T, Task: task}
}
