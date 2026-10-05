// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package workflowtest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	wf "golang.org/x/build/internal/workflow"
)

type Listener struct {
	addr net.Addr
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

var nextPort atomic.Int64

func NewListener() *Listener {
	return &Listener{
		addr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), uint16(10000+nextPort.Add(1)))),
		ch:   make(chan net.Conn),
		done: make(chan struct{}),
	}
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *Listener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *Listener) Addr() net.Addr { return l.addr }

func (l *Listener) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
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

func NewClient(listeners ...*Listener) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				for _, l := range listeners {
					if l.Addr().String() == addr {
						return l.DialContext(ctx, network, addr)
					}
				}
				return nil, fmt.Errorf("no listener for address %q", addr)
			},
		},
	}
}

// NewInMemoryServer provides the necessary abstraction to
// cleanly use synctest and httptest.
//
// TODO(nealpatel): Remove these abstractions once x/build
// uses go1.27.
func NewInMemoryServer(handler http.Handler) (url string, client *http.Client, cleanup func()) {
	li := NewListener()
	srv := &http.Server{Handler: handler}
	go srv.Serve(li)

	return "http://" + li.Addr().String(), NewClient(li), func() { srv.Close() }
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
	return fmt.Errorf("workflow %s stalled with no OnStall handler", workflowID)
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

type CapturingLogger struct {
	Lines []string
}

func (l *CapturingLogger) Printf(format string, v ...any) {
	l.Lines = append(l.Lines, fmt.Sprintf(format, v...))
}

type LoggerListener struct {
	wf.Listener
	Log wf.Logger
}

func (l *LoggerListener) Logger(_ uuid.UUID, _ string) wf.Logger {
	return l.Log
}

type MapListener struct {
	wf.Listener
	States map[uuid.UUID]map[string]*wf.TaskState
}

func (l *MapListener) TaskStateChanged(workflowID uuid.UUID, taskID string, state *wf.TaskState) error {
	if l.States == nil {
		l.States = map[uuid.UUID]map[string]*wf.TaskState{}
	}
	if l.States[workflowID] == nil {
		l.States[workflowID] = map[string]*wf.TaskState{}
	}
	l.States[workflowID][taskID] = state
	return l.Listener.TaskStateChanged(workflowID, taskID, state)
}

func (l *MapListener) AssertState(t *testing.T, w *wf.Workflow, want map[string]*wf.TaskState) {
	t.Helper()
	if diff := cmp.Diff(l.States[w.ID], want, cmpopts.IgnoreFields(wf.TaskState{}, "SerializedResult")); diff != "" {
		t.Errorf("task state didn't match expectations: %v", diff)
	}
}

// Subtest shadows [synctest.Subtest] behavior in go1.27+.
func Subtest(t *testing.T, name string, f func(*testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Helper()
		synctest.Test(t, f)
	})
}

func Start(t testing.TB, wd *wf.Definition, params map[string]any) *wf.Workflow {
	t.Helper()
	w, err := wf.Start(wd, params)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// Run runs w to completion and returns its outputs, failing t on error.
// If listener is nil, a [VerboseListener] logging to t is used.
func Run(t testing.TB, ctx context.Context, w *wf.Workflow, listener wf.Listener) map[string]any {
	t.Helper()
	if listener == nil {
		listener = &VerboseListener{T: t}
	}
	outputs, err := w.Run(ctx, listener)
	if err != nil {
		t.Fatalf("w.Run() = _, %v, wanted no error", err)
	}
	return outputs
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
