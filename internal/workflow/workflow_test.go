// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"golang.org/x/build/internal/workflow"
)

func TestTrivial(t *testing.T) {
	echo := func(ctx context.Context, arg string) (string, error) {
		return arg, nil
	}

	wd := workflow.New()
	greeting := wd.Task("echo", echo, wd.Constant("hello world"))
	wd.Output("greeting", greeting)

	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, nil)
	if got, want := outputs["greeting"], "hello world"; got != want {
		t.Errorf("greeting = %q, want %q", got, want)
	}
}

func TestDependency(t *testing.T) {
	var actionRan, checkRan bool
	action := func(ctx context.Context) error {
		actionRan = true
		return nil
	}
	checkAction := func(ctx context.Context) error {
		if !actionRan {
			return fmt.Errorf("prior action didn't run")
		}
		checkRan = true
		return nil
	}
	hi := func(ctx context.Context) (string, error) {
		if !actionRan || !checkRan {
			return "", fmt.Errorf("either action (%v) or checkAction (%v) didn't run", actionRan, checkRan)
		}
		return "hello world", nil
	}

	wd := workflow.New()
	firstDep := wd.Action("first action", action)
	secondDep := wd.Action("check action", checkAction, firstDep)
	wd.Output("greeting", wd.Task("say hi", hi, secondDep))

	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, nil)
	if got, want := outputs["greeting"], "hello world"; got != want {
		t.Errorf("greeting = %q, want %q", got, want)
	}
}

func TestDependencyError(t *testing.T) {
	action := func(ctx context.Context) error {
		return fmt.Errorf("hardcoded error")
	}
	task := func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("unexpected error")
	}

	wd := workflow.New()
	dep := wd.Action("failing action", action)
	wd.Output("output", wd.Task("task", task, dep))
	w := startWorkflow(t, wd, nil)
	l := &verboseListener{t: t}
	if _, err := w.Run(context.Background(), l); err == nil {
		t.Errorf("workflow finished successfully, expected an error")
	}
}

func TestSub(t *testing.T) {
	hi := func(ctx context.Context) (string, error) {
		return "hi", nil
	}
	concat := func(ctx context.Context, s1, s2 string) (string, error) {
		return s1 + " " + s2, nil
	}

	wd := workflow.New()
	sub1 := wd.Sub("sub1")
	g1 := sub1.Task("Greeting", hi)
	sub2 := wd.Sub("sub2")
	g2 := sub2.Task("Greeting", hi)
	wd.Output("result", wd.Task("Concatenate", concat, g1, g2))

	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, nil)
	if got, want := outputs["result"], "hi hi"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
}

func TestStuck(t *testing.T) {
	fail := func(context.Context) (string, error) {
		return "", fmt.Errorf("goodbye world")
	}

	wd := workflow.New()
	nothing := wd.Task("fail", fail)
	wd.Output("nothing", nothing)

	w := startWorkflow(t, wd, nil)
	_, err := w.Run(context.Background(), &verboseListener{t: t})
	if err == nil || !strings.Contains(err.Error(), "as far as it can") {
		t.Errorf("Run of stuck workflow = %v, wanted it to give up early", err)
	}
}

func TestSplitJoin(t *testing.T) {
	echo := func(ctx context.Context, arg string) (string, error) {
		return arg, nil
	}
	appendInt := func(ctx context.Context, s string, i int) (string, error) {
		return fmt.Sprintf("%v%v", s, i), nil
	}
	join := func(ctx context.Context, s []string) (string, error) {
		return strings.Join(s, ","), nil
	}

	wd := workflow.New()
	in := wd.Task("echo", echo, wd.Constant("string #"))
	add1 := wd.Task("add 1", appendInt, in, wd.Constant(1))
	add2 := wd.Task("add 2", appendInt, in, wd.Constant(2))
	both := wd.Slice(add1, add2)
	out := wd.Task("join", join, both)
	wd.Output("strings", out)

	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, nil)
	if got, want := outputs["strings"], "string #1,string #2"; got != want {
		t.Errorf("joined output = %q, want %q", got, want)
	}
}

func TestParallelism(t *testing.T) {
	// block1 and block2 block until they're both running.
	chan1, chan2 := make(chan bool, 1), make(chan bool, 1)
	block1 := func(ctx context.Context) (string, error) {
		chan1 <- true
		select {
		case <-chan2:
		case <-ctx.Done():
		}
		return "", ctx.Err()
	}
	block2 := func(ctx context.Context) (string, error) {
		chan2 <- true
		select {
		case <-chan1:
		case <-ctx.Done():
		}
		return "", ctx.Err()
	}
	wd := workflow.New()
	out1 := wd.Task("block #1", block1)
	out2 := wd.Task("block #2", block2)
	wd.Output("out1", out1)
	wd.Output("out2", out2)

	w := startWorkflow(t, wd, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.Run(ctx, &verboseListener{t}); err != nil {
		t.Fatal(err)
	}
}

func TestParameters(t *testing.T) {
	echo := func(ctx context.Context, arg string) (string, error) {
		return arg, nil
	}

	wd := workflow.New()
	param1 := wd.Parameter(workflow.Parameter{Name: "param1"})
	param2 := wd.Parameter(workflow.Parameter{Name: "param2"})
	out1 := wd.Task("echo 1", echo, param1)
	out2 := wd.Task("echo 2", echo, param2)
	wd.Output("out1", out1)
	wd.Output("out2", out2)

	wantParams := []workflow.Parameter{
		{Name: "param1", ParameterType: workflow.BasicString},
		{Name: "param2", ParameterType: workflow.BasicString},
	}
	if diff := cmp.Diff(wantParams, wd.Parameters(), cmp.Comparer(func(x, y reflect.Type) bool { return x == y })); diff != "" {
		t.Errorf("wd.Parameters() mismatch (-want +got):\n%s", diff)
	}

	w := startWorkflow(t, wd, map[string]interface{}{"param1": "#1", "param2": "#2"})
	outputs := runWorkflow(t, w, nil)
	if want := map[string]interface{}{"out1": "#1", "out2": "#2"}; !reflect.DeepEqual(outputs, want) {
		t.Errorf("outputs = %#v, want %#v", outputs, want)
	}

	t.Run("CountMismatch", func(t *testing.T) {
		_, err := workflow.Start(wd, map[string]interface{}{"param1": "#1"})
		if err == nil {
			t.Errorf("workflow.Start didn't return an error despite a parameter count mismatch")
		}
	})
	t.Run("NameMismatch", func(t *testing.T) {
		_, err := workflow.Start(wd, map[string]interface{}{"paramA": "#1", "paramB": "#2"})
		if err == nil {
			t.Errorf("workflow.Start didn't return an error despite a parameter name mismatch")
		}
	})
	t.Run("TypeMismatch", func(t *testing.T) {
		_, err := workflow.Start(wd, map[string]interface{}{"param1": "#1", "param2": 42})
		if err == nil {
			t.Errorf("workflow.Start didn't return an error despite a parameter type mismatch")
		}
	})
}

// Test that passing workflow.Parameter{...} directly to Definition.Task would be a build-time error.
// Parameters need to be registered via the Definition.Parameter method.
func TestParameterValue(t *testing.T) {
	var p interface{} = workflow.Parameter{}
	if _, ok := p.(workflow.Value); ok {
		t.Errorf("Parameter unexpectedly implements Value; it intentionally tries not to to reduce possible API misuse")
	}
}

func TestRetry(t *testing.T) {
	counter := 0
	needsRetry := func(ctx context.Context) (string, error) {
		if counter < 2 {
			counter++
			return "", fmt.Errorf("counter %v too low", counter)
		}
		return "hi", nil
	}

	wd := workflow.New()
	wd.Output("result", wd.Task("needs retry", needsRetry))

	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, nil)
	if got, want := outputs["result"], "hi"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
	if counter != 2 {
		t.Errorf("counter = %v, want 2", counter)
	}
}

func TestRetryDisabled(t *testing.T) {
	counter := 0
	noRetry := func(ctx *workflow.TaskContext) (string, error) {
		ctx.DisableRetries()
		counter++
		return "", fmt.Errorf("do not pass go")
	}

	wd := workflow.New()
	wd.Output("result", wd.Task("no retry", noRetry))

	w := startWorkflow(t, wd, nil)
	_, err := w.Run(context.Background(), &verboseListener{t: t})
	if err == nil || !strings.Contains(err.Error(), "as far as it can") {
		t.Errorf("Run of failing workflow = %v, wanted it to fail", err)
	}

	if counter != 1 {
		t.Errorf("task with retries disabled ran %v times, wanted 1", counter)
	}
}

func TestLogging(t *testing.T) {
	log := func(ctx *workflow.TaskContext, arg string) (string, error) {
		ctx.Printf("logging argument: %v", arg)
		return arg, nil
	}

	wd := workflow.New()
	out := wd.Task("log", log, wd.Constant("hey there"))
	wd.Output("out", out)

	logger := &capturingLogger{}
	listener := &logTestListener{
		Listener: &verboseListener{t},
		logger:   logger,
	}
	w := startWorkflow(t, wd, nil)
	runWorkflow(t, w, listener)
	if want := []string{"logging argument: hey there"}; !reflect.DeepEqual(logger.lines, want) {
		t.Errorf("unexpected logging result: got %v, want %v", logger.lines, want)
	}
}

type logTestListener struct {
	workflow.Listener
	logger workflow.Logger
}

func (l *logTestListener) Logger(_ uuid.UUID, _ string) workflow.Logger {
	return l.logger
}

type capturingLogger struct {
	lines []string
}

func (l *capturingLogger) Printf(format string, v ...interface{}) {
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

func TestResume(t *testing.T) {
	// We expect runOnlyOnce to only run once.
	var runs int64
	runOnlyOnce := func(ctx context.Context) (string, error) {
		atomic.AddInt64(&runs, 1)
		return "ran", nil
	}
	// blockOnce blocks the first time it's called, so that the workflow can be
	// canceled at its step.
	block := true
	blocked := make(chan bool, 1)
	maybeBlock := func(ctx *workflow.TaskContext, _ string) (string, error) {
		ctx.DisableRetries()
		if block {
			blocked <- true
			<-ctx.Done()
			return "blocked", ctx.Err()
		}
		return "not blocked", nil
	}
	wd := workflow.New()
	v1 := wd.Task("run once", runOnlyOnce)
	v2 := wd.Task("block", maybeBlock, v1)
	wd.Output("output", v2)

	// Cancel the workflow once we've entered maybeBlock.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-blocked
		cancel()
	}()
	w, err := workflow.Start(wd, nil)
	if err != nil {
		t.Fatal(err)
	}
	storage := &mapListener{Listener: &verboseListener{t}}
	_, err = w.Run(ctx, storage)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled workflow returned error %v, wanted Canceled", err)
	}
	storage.assertState(t, w, map[string]*workflow.TaskState{
		"run once": {Name: "run once", Started: true, Finished: true, Result: "ran"},
		"block":    {Name: "block", Started: true, Finished: true, Error: "context canceled"}, // We cancelled the workflow before it could save its state.
	})

	block = false
	wfState := &workflow.WorkflowState{ID: w.ID, Params: nil}
	taskStates := storage.states[w.ID]
	taskStates["block"] = &workflow.TaskState{Name: "block"}
	w2, err := workflow.Resume(wd, wfState, taskStates)
	if err != nil {
		t.Fatal(err)
	}
	out := runWorkflow(t, w2, storage)
	if got, want := out["output"], "not blocked"; got != want {
		t.Errorf("output from maybeBlock was %q, wanted %q", got, want)
	}
	if runs != 1 {
		t.Errorf("runOnlyOnce ran %v times, wanted 1", runs)
	}
	storage.assertState(t, w, map[string]*workflow.TaskState{
		"run once": {Name: "run once", Started: true, Finished: true, Result: "ran"},
		"block":    {Name: "block", Started: true, Finished: true, Result: "not blocked"},
	})
}

type badResult struct {
	unexported string
}

func TestBadMarshaling(t *testing.T) {
	greet := func(_ context.Context) (badResult, error) {
		return badResult{"hi"}, nil
	}

	wd := workflow.New()
	wd.Output("greeting", wd.Task("greet", greet))
	w := startWorkflow(t, wd, nil)
	if _, err := w.Run(context.Background(), &verboseListener{t}); err == nil {
		t.Errorf("running a workflow with bad JSON should give an error, got none")
	}
}

type mapListener struct {
	workflow.Listener
	states map[uuid.UUID]map[string]*workflow.TaskState
}

func (l *mapListener) TaskStateChanged(workflowID uuid.UUID, taskID string, state *workflow.TaskState) error {
	if l.states == nil {
		l.states = map[uuid.UUID]map[string]*workflow.TaskState{}
	}
	if l.states[workflowID] == nil {
		l.states[workflowID] = map[string]*workflow.TaskState{}
	}
	l.states[workflowID][taskID] = state
	return l.Listener.TaskStateChanged(workflowID, taskID, state)
}

func (l *mapListener) assertState(t *testing.T, w *workflow.Workflow, want map[string]*workflow.TaskState) {
	t.Helper()
	if diff := cmp.Diff(l.states[w.ID], want, cmpopts.IgnoreFields(workflow.TaskState{}, "SerializedResult")); diff != "" {
		t.Errorf("task state didn't match expections: %v", diff)
	}
}

func startWorkflow(t *testing.T, wd *workflow.Definition, params map[string]interface{}) *workflow.Workflow {
	t.Helper()
	w, err := workflow.Start(wd, params)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func runWorkflow(t *testing.T, w *workflow.Workflow, listener workflow.Listener) map[string]interface{} {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Helper()
	if listener == nil {
		listener = &verboseListener{t}
	}
	outputs, err := w.Run(ctx, listener)
	if err != nil {
		t.Fatalf("w.Run() = _, %v, wanted no error", err)
	}
	return outputs
}

type verboseListener struct{ t *testing.T }

func (l *verboseListener) TaskStateChanged(_ uuid.UUID, _ string, st *workflow.TaskState) error {
	switch {
	case !st.Started:
		// Task creation is uninteresting.
	case !st.Finished:
		l.t.Logf("task %-10v: started", st.Name)
	case st.Error != "":
		l.t.Logf("task %-10v: error: %v", st.Name, st.Error)
	default:
		l.t.Logf("task %-10v: done: %v", st.Name, st.Result)
	}
	return nil
}

func (l *verboseListener) Logger(_ uuid.UUID, task string) workflow.Logger {
	return &testLogger{t: l.t, task: task}
}

type testLogger struct {
	t    *testing.T
	task string
}

func (l *testLogger) Printf(format string, v ...interface{}) {
	l.t.Logf("task %-10v: LOG: %s", l.task, fmt.Sprintf(format, v...))
}
