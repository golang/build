// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package workflowtest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	wf "golang.org/x/build/internal/workflow"
)

type PipeListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func NewPipeListener() *PipeListener {
	return &PipeListener{
		ch:   make(chan net.Conn),
		done: make(chan struct{}),
	}
}

func (l *PipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *PipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *PipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *PipeListener) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case l.ch <- server:
		return client, nil
	case <-l.done:
		server.Close()
		client.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		server.Close()
		client.Close()
		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// NewInMemoryServer provides the necessary abstraction to
// cleanly use synctest and httptest.
//
// TODO(nealpatel): Remove these abstractions once x/build
// uses go1.27.
func NewInMemoryServer(handler http.Handler) (url string, client *http.Client, cleanup func()) {
	pl := NewPipeListener()
	srv := &http.Server{Handler: handler}
	go srv.Serve(pl)

	client = &http.Client{
		Transport: &http.Transport{
			DialContext: pl.DialContext,
		},
	}
	cleanup = func() {
		srv.Close()
	}
	return "http://pipe", client, cleanup
}

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
	OnStall        func() error
}

func (l *VerboseListener) WorkflowStalled(workflowID uuid.UUID) error {
	l.T.Logf("workflow %q: stalled", workflowID.String())
	if l.OnStall != nil {
		return l.OnStall()
	}
	return fmt.Errorf("workflow %s stalled with no handler", workflowID)
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

type ErrorListener struct {
	TaskName string
	Callback func(string)
	wf.Listener
}

func (l *ErrorListener) TaskStateChanged(id uuid.UUID, taskID string, st *wf.TaskState) error {
	if st.Name == l.TaskName && st.Finished && st.Error != "" {
		l.Callback(st.Error)
	}
	return l.Listener.TaskStateChanged(id, taskID, st)
}

// Subtest shadows [synctest.Subtest] behavior in go1.27+.
func Subtest(t *testing.T, name string, f func(*testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Helper()
		synctest.Test(t, f)
	})
}

func RunToFailure(t testing.TB, ctx context.Context, w *wf.Workflow, task string, listener wf.Listener) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t.Helper()
	var message string
	l := &ErrorListener{
		TaskName: task,
		Callback: func(m string) {
			message = m
			cancel()
		},
		Listener: listener,
	}
	_, err := w.Run(ctx, l)
	if err == nil {
		t.Fatalf("workflow unexpectedly succeeded")
	}
	return message
}
