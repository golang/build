// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	wf "golang.org/x/build/internal/workflow"
)

func TestTrivial(t *testing.T) {
	echo := func(ctx context.Context, arg string) (string, error) {
		return arg, nil
	}

	wd := wf.New(wf.ACL{})
	wf.Task1(wd, "echo", echo, wf.Const("hello world"))
	greeting := wf.Task1(wd, "echo", echo, wf.Const("hello world"))
	wf.Output(wd, "greeting", greeting)

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
	checkAction := func(ctx context.Context) (string, error) {
		if !actionRan {
			return "", fmt.Errorf("prior action didn't run")
		}
		checkRan = true
		return "", nil
	}
	hi := func(ctx context.Context) (string, error) {
		if !actionRan || !checkRan {
			return "", fmt.Errorf("either action (%v) or checkAction (%v) didn't run", actionRan, checkRan)
		}
		return "hello world", nil
	}

	wd := wf.New(wf.ACL{})
	firstDep := wf.Action0(wd, "first action", action)
	secondDep := wf.Task0(wd, "check action", checkAction, wf.After(firstDep))
	wf.Output(wd, "greeting", wf.Task0(wd, "say hi", hi, wf.After(secondDep)))

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

	wd := wf.New(wf.ACL{})
	dep := wf.Action0(wd, "failing action", action)
	wf.Output(wd, "output", wf.Task0(wd, "task", task, wf.After(dep)))
	w := startWorkflow(t, wd, nil)
	if got, want := runToFailure(t, w, nil, "failing action"), "hardcoded error"; got != want {
		t.Errorf("got error %q, want %q", got, want)
	}
}

func TestSub(t *testing.T) {
	hi := func(ctx context.Context) (string, error) {
		return "hi", nil
	}
	concat := func(ctx context.Context, s1, s2 string) (string, error) {
		return s1 + " " + s2, nil
	}

	wd := wf.New(wf.ACL{})
	topSub := wd.Sub("top-sub")
	sub1 := topSub.Sub("sub1")
	g1 := wf.Task0(sub1, "Greeting", hi)
	sub2 := topSub.Sub("sub2")
	g2 := wf.Task0(sub2, "Greeting", hi)
	wf.Output(wd, "result", wf.Task2(wd, "Concatenate", concat, g1, g2))

	storage := &mapListener{Listener: &verboseListener{t}}
	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, storage)
	if got, want := outputs["result"], "hi hi"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
	const wantTaskID = "top-sub: sub1: Greeting"
	if _, ok := storage.states[w.ID][wantTaskID]; !ok {
		t.Errorf("task ID %q doesn't exist, have: %q", wantTaskID, slices.Sorted(maps.Keys(storage.states[w.ID])))
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

	wd := wf.New(wf.ACL{})
	in := wf.Task1(wd, "echo", echo, wf.Const("string #"))
	add1 := wf.Task2(wd, "add 1", appendInt, in, wf.Const(1))
	add2 := wf.Task2(wd, "add 2", appendInt, in, wf.Const(2))
	both := wf.Slice(add1, add2)
	out := wf.Task1(wd, "join", join, both)
	wf.Output(wd, "strings", out)

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
	wd := wf.New(wf.ACL{})
	out1 := wf.Task0(wd, "block #1", block1)
	out2 := wf.Task0(wd, "block #2", block2)
	wf.Output(wd, "out1", out1)
	wf.Output(wd, "out2", out2)

	w := startWorkflow(t, wd, nil)
	runWorkflow(t, w, nil)
}

func TestParameters(t *testing.T) {
	echo := func(ctx context.Context, arg string) (string, error) {
		return arg, nil
	}

	wd := wf.New(wf.ACL{})
	param1 := wf.Param(wd, wf.ParamDef[string]{Name: "param1"})
	param2 := wf.Param(wd, wf.ParamDef[string]{Name: "param2"})
	out1 := wf.Task1(wd, "echo 1", echo, param1)
	out2 := wf.Task1(wd, "echo 2", echo, param2)
	wf.Output(wd, "out1", out1)
	wf.Output(wd, "out2", out2)

	w := startWorkflow(t, wd, map[string]interface{}{"param1": "#1", "param2": "#2"})
	outputs := runWorkflow(t, w, nil)
	if want := map[string]interface{}{"out1": "#1", "out2": "#2"}; !reflect.DeepEqual(outputs, want) {
		t.Errorf("outputs = %#v, want %#v", outputs, want)
	}

	t.Run("CountMismatch", func(t *testing.T) {
		_, err := wf.Start(wd, map[string]interface{}{"param1": "#1"})
		if err == nil {
			t.Errorf("wf.Start didn't return an error despite a parameter count mismatch")
		}
	})
	t.Run("NameMismatch", func(t *testing.T) {
		_, err := wf.Start(wd, map[string]interface{}{"paramA": "#1", "paramB": "#2"})
		if err == nil {
			t.Errorf("wf.Start didn't return an error despite a parameter name mismatch")
		}
	})
	t.Run("TypeMismatch", func(t *testing.T) {
		_, err := wf.Start(wd, map[string]interface{}{"param1": "#1", "param2": 42})
		if err == nil {
			t.Errorf("wf.Start didn't return an error despite a parameter type mismatch")
		}
	})
}

// Test that passing wf.Parameter{...} directly to Definition.Task would be a build-time error.
// Parameters need to be registered via the Definition.Parameter method.
func TestParameterValue(t *testing.T) {
	var p interface{} = wf.ParamDef[int]{}
	if _, ok := p.(wf.Value[int]); ok {
		t.Errorf("Parameter unexpectedly implements Value; it intentionally tries not to reduce possible API misuse")
	}
}

func TestExpansion(t *testing.T) {
	first := func(_ context.Context) (string, error) {
		return "hey", nil
	}
	second := func(_ context.Context) (string, error) {
		return "there", nil
	}
	third := func(_ context.Context) (string, error) {
		return "friend", nil
	}
	join := func(_ context.Context, args []string) (string, error) {
		return strings.Join(args, " "), nil
	}

	wd := wf.New(wf.ACL{})
	v1 := wf.Task0(wd, "first", first)
	v2 := wf.Task0(wd, "second", second)
	wf.Output(wd, "second", v2)
	joined := wf.Expand1(wd, "add a task", func(wd *wf.Definition, arg string) (wf.Value[string], error) {
		v3 := wf.Task0(wd, "third", third)
		// v1 is resolved before the expansion runs, v2 and v3 are dependencies
		// created outside and inside the epansion.
		return wf.Task1(wd, "join", join, wf.Slice(wf.Const(arg), v2, v3)), nil
	}, v1)
	wf.Output(wd, "final value", joined)

	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, nil)
	if got, want := outputs["final value"], "hey there friend"; got != want {
		t.Errorf("joined output = %q, want %q", got, want)
	}
}

func TestResumeExpansion(t *testing.T) {
	counter := 0
	succeeds := func(ctx *wf.TaskContext) (string, error) {
		counter++
		return "", nil
	}
	wd := wf.New(wf.ACL{})
	result := wf.Expand0(wd, "expand", func(wd *wf.Definition) (wf.Value[string], error) {
		return wf.Task0(wd, "succeeds", succeeds), nil
	})
	wf.Output(wd, "result", result)

	storage := &mapListener{Listener: &verboseListener{t}}
	w := startWorkflow(t, wd, nil)
	runWorkflow(t, w, storage)
	resumed, err := wf.Resume(wd, &wf.WorkflowState{ID: w.ID}, storage.states[w.ID])
	if err != nil {
		t.Fatal(err)
	}
	runWorkflow(t, resumed, nil)
	if counter != 1 {
		t.Errorf("task ran %v times, wanted 1", counter)
	}
}

func TestRetryExpansion(t *testing.T) {
	counter := 0
	wd := wf.New(wf.ACL{})
	out := wf.Expand0(wd, "expand", func(wd *wf.Definition) (wf.Value[string], error) {
		counter++
		if counter == 1 {
			return nil, fmt.Errorf("first try fail")
		}
		return wf.Task0(wd, "hi", func(_ context.Context) (string, error) {
			return "", nil
		}), nil
	})
	wf.Output(wd, "out", out)

	w := startWorkflow(t, wd, nil)
	retry := func(string) {
		go func() {
			w.RetryTask(context.Background(), "expand")
		}()
	}
	listener := &errorListener{
		taskName: "expand",
		callback: retry,
		Listener: &verboseListener{t},
	}
	runWorkflow(t, w, listener)
	if counter != 2 {
		t.Errorf("task ran %v times, wanted 2", counter)
	}
}

func TestManualRetry(t *testing.T) {
	counter := 0
	needsRetry := func(ctx *wf.TaskContext) (string, error) {
		ctx.DisableRetries()
		counter++
		if counter == 1 {
			return "", fmt.Errorf("counter %v too low", counter)
		}
		return "hi", nil
	}

	wd := wf.New(wf.ACL{})
	wf.Output(wd, "result", wf.Task0(wd, "needs retry", needsRetry))

	w := startWorkflow(t, wd, nil)

	retry := func(string) {
		go func() {
			w.RetryTask(context.Background(), "needs retry")
		}()
	}
	listener := &errorListener{
		taskName: "needs retry",
		callback: retry,
		Listener: &verboseListener{t},
	}
	runWorkflow(t, w, listener)
	if counter != 2 {
		t.Errorf("task ran %v times, wanted 2", counter)
	}
}

// Test that manual retry works on tasks that come from different expansions.
//
// This is similar to how the Go minor release workflow plans builders for
// both releases. It previously failed due to expansions racing with with other,
// leading to "unknown task" errors when retrying. See go.dev/issue/70249.
func TestManualRetryMultipleExpansions(t *testing.T) {
	// Create two sub-workflows, each one with an expansion that adds one work task.
	// The work tasks fail on the first try, and require being successfully restarted
	// for the workflow to complete.
	var counters, retried [2]int
	wd := wf.New(wf.ACL{})
	sub1 := wd.Sub("sub1")
	sub2 := wd.Sub("sub2")
	for i, wd := range []*wf.Definition{sub1, sub2} {
		out := wf.Expand0(wd, fmt.Sprintf("expand %d", i+1), func(wd *wf.Definition) (wf.Value[string], error) {
			return wf.Task0(wd, fmt.Sprintf("work %d", i+1), func(ctx *wf.TaskContext) (string, error) {
				ctx.DisableRetries()
				counters[i]++
				if counters[i] == 1 {
					return "", fmt.Errorf("first try fail")
				}
				return "", nil
			}), nil
		})
		wf.Output(wd, "out", out)
	}

	w := startWorkflow(t, wd, nil)
	listener := &errorListener{
		taskName: "sub1: work 1",
		callback: func(string) {
			go func() {
				retried[0]++
				err := w.RetryTask(context.Background(), "sub1: work 1")
				if err != nil {
					t.Errorf(`RetryTask("sub1: work 1") failed: %v`, err)
				}
			}()
		},
		Listener: &errorListener{
			taskName: "sub2: work 2",
			callback: func(string) {
				go func() {
					retried[1]++
					err := w.RetryTask(context.Background(), "sub2: work 2")
					if err != nil {
						t.Errorf(`RetryTask("sub2: work 2") failed: %v`, err)
					}
				}()
			},
			Listener: &verboseListener{t},
		},
	}
	runWorkflow(t, w, listener)
	if counters[0] != 2 {
		t.Errorf("sub1 task ran %v times, wanted 2", counters[0])
	}
	if retried[0] != 1 {
		t.Errorf("sub1 task was retried %v times, wanted 1", retried[0])
	}
	if counters[1] != 2 {
		t.Errorf("sub2 task ran %v times, wanted 2", counters[1])
	}
	if retried[1] != 1 {
		t.Errorf("sub2 task was retried %v times, wanted 1", retried[1])
	}
}

func TestAutomaticRetry(t *testing.T) {
	counter := 0
	needsRetry := func(ctx *wf.TaskContext) (string, error) {
		if counter < 2 {
			counter++
			return "", fmt.Errorf("counter %v too low", counter)
		}
		return "hi", nil
	}

	wd := wf.New(wf.ACL{})
	wf.Output(wd, "result", wf.Task0(wd, "needs retry", needsRetry))

	w := startWorkflow(t, wd, nil)
	outputs := runWorkflow(t, w, nil)
	if got, want := outputs["result"], "hi"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
	if counter != 2 {
		t.Errorf("counter = %v, want 2", counter)
	}
}

func TestAutomaticRetryDisabled(t *testing.T) {
	counter := 0
	noRetry := func(ctx *wf.TaskContext) (string, error) {
		ctx.DisableRetries()
		counter++
		return "", fmt.Errorf("do not pass go")
	}

	wd := wf.New(wf.ACL{})
	wf.Output(wd, "result", wf.Task0(wd, "no retry", noRetry))

	w := startWorkflow(t, wd, nil)
	if got, want := runToFailure(t, w, nil, "no retry"), "do not pass go"; got != want {
		t.Errorf("got error %q, want %q", got, want)
	}
	if counter != 1 {
		t.Errorf("task with retries disabled ran %v times, wanted 1", counter)
	}
}

func TestWatchdog(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		testWatchdog(t, true)
	})
	t.Run("failure", func(t *testing.T) {
		testWatchdog(t, false)
	})
}

func testWatchdog(t *testing.T, success bool) {
	defer func(r int, d time.Duration) {
		wf.MaxRetries = r
		wf.WatchdogDelay = d
	}(wf.MaxRetries, wf.WatchdogDelay)
	wf.MaxRetries = 1
	wf.WatchdogDelay = 750 * time.Millisecond

	maybeLog := func(ctx *wf.TaskContext) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if success {
			ctx.Printf("*snore*")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		return "huh? what?", nil
	}

	wd := wf.New(wf.ACL{})
	wf.Output(wd, "result", wf.Task0(wd, "sleepy", maybeLog))

	w := startWorkflow(t, wd, nil)
	if success {
		runWorkflow(t, w, nil)
	} else {
		if got, want := runToFailure(t, w, nil, "sleepy"), "assumed hung"; !strings.Contains(got, want) {
			t.Errorf("got error %q, want %q", got, want)
		}
	}
}

func TestLogging(t *testing.T) {
	log := func(ctx *wf.TaskContext, arg string) (string, error) {
		ctx.Printf("logging argument: %v", arg)
		return arg, nil
	}

	wd := wf.New(wf.ACL{})
	out := wf.Task1(wd, "log", log, wf.Const("hey there"))
	wf.Output(wd, "out", out)

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
	wf.Listener
	logger wf.Logger
}

func (l *logTestListener) Logger(_ uuid.UUID, _ string) wf.Logger {
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
	maybeBlock := func(ctx *wf.TaskContext, _ string) (string, error) {
		ctx.DisableRetries()
		if block {
			blocked <- true
			<-ctx.Done()
			return "blocked", ctx.Err()
		}
		return "not blocked", nil
	}
	wd := wf.New(wf.ACL{})
	v1 := wf.Task0(wd, "run once", runOnlyOnce)
	v2 := wf.Task1(wd, "block", maybeBlock, v1)
	wf.Output(wd, "output", v2)

	// Cancel the workflow once we've entered maybeBlock.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-blocked
		cancel()
	}()
	w, err := wf.Start(wd, nil)
	if err != nil {
		t.Fatal(err)
	}
	storage := &mapListener{Listener: &verboseListener{t}}
	_, err = w.Run(ctx, storage)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled workflow returned error %v, wanted Canceled", err)
	}
	storage.assertState(t, w, map[string]*wf.TaskState{
		"run once": {Name: "run once", Started: true, Finished: true, Result: "ran"},
		"block":    {Name: "block", Started: true, Finished: true, Error: "context canceled"}, // We cancelled the workflow before it could save its state.
	})

	block = false
	wfState := &wf.WorkflowState{ID: w.ID, Params: nil}
	taskStates := storage.states[w.ID]
	taskStates["block"] = &wf.TaskState{Name: "block"}
	w2, err := wf.Resume(wd, wfState, taskStates)
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
	storage.assertState(t, w, map[string]*wf.TaskState{
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

	wd := wf.New(wf.ACL{})
	wf.Output(wd, "greeting", wf.Task0(wd, "greet", greet))
	w := startWorkflow(t, wd, nil)
	if got, want := runToFailure(t, w, nil, "greet"), "JSON marshaling"; !strings.Contains(got, want) {
		t.Errorf("got error %q, want %q", got, want)
	}
}

type mapListener struct {
	wf.Listener
	states map[uuid.UUID]map[string]*wf.TaskState
}

func (l *mapListener) TaskStateChanged(workflowID uuid.UUID, taskID string, state *wf.TaskState) error {
	if l.states == nil {
		l.states = map[uuid.UUID]map[string]*wf.TaskState{}
	}
	if l.states[workflowID] == nil {
		l.states[workflowID] = map[string]*wf.TaskState{}
	}
	l.states[workflowID][taskID] = state
	return l.Listener.TaskStateChanged(workflowID, taskID, state)
}

func (l *mapListener) assertState(t *testing.T, w *wf.Workflow, want map[string]*wf.TaskState) {
	t.Helper()
	if diff := cmp.Diff(l.states[w.ID], want, cmpopts.IgnoreFields(wf.TaskState{}, "SerializedResult")); diff != "" {
		t.Errorf("task state didn't match expectations: %v", diff)
	}
}

func startWorkflow(t *testing.T, wd *wf.Definition, params map[string]interface{}) *wf.Workflow {
	t.Helper()
	w, err := wf.Start(wd, params)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func runWorkflow(t *testing.T, w *wf.Workflow, listener wf.Listener) map[string]interface{} {
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

func (l *verboseListener) WorkflowStalled(workflowID uuid.UUID) error {
	l.t.Logf("workflow %q: stalled", workflowID.String())
	return nil
}

func (l *verboseListener) TaskStateChanged(_ uuid.UUID, _ string, st *wf.TaskState) error {
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

func (l *verboseListener) Logger(_ uuid.UUID, task string) wf.Logger {
	return &testLogger{t: l.t, task: task}
}

type testLogger struct {
	t    *testing.T
	task string
}

func (l *testLogger) Printf(format string, v ...interface{}) {
	l.t.Logf("task %-10v: LOG: %s", l.task, fmt.Sprintf(format, v...))
}

func runToFailure(t *testing.T, w *wf.Workflow, listener wf.Listener, task string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Helper()
	if listener == nil {
		listener = &verboseListener{t}
	}
	var message string
	listener = &errorListener{
		taskName: task,
		callback: func(m string) {
			message = m
			// Allow other tasks to run before shutting down the workflow.
			time.AfterFunc(50*time.Millisecond, cancel)
		},
		Listener: listener,
	}
	_, err := w.Run(ctx, listener)
	if err == nil {
		t.Fatalf("workflow unexpectedly succeeded")
	}
	return message
}

type errorListener struct {
	taskName string
	callback func(string)
	wf.Listener
}

func (l *errorListener) TaskStateChanged(id uuid.UUID, taskID string, st *wf.TaskState) error {
	if st.Name == l.taskName && st.Finished && st.Error != "" {
		l.callback(st.Error)
	}
	l.Listener.TaskStateChanged(id, taskID, st)
	return nil
}
