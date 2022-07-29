// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package workflow declaratively defines computation graphs that support
// automatic parallelization, persistence, and monitoring.
//
// Workflows are a set of tasks and actions that produce and consume Values.
// Tasks don't run until the workflow is started, so Values represent data that
// doesn't exist yet, and can't be used directly. Each value has a dynamic type,
// which must match its uses.
//
// To wrap an existing Go object in a Value, use Const. To define a
// parameter that will be set when the workflow is started, use Param.
// To read a task's return value, register it as an Output, and it will be
// returned from Run. An arbitrary number of Values of the same type can
// be combined with Slice.
//
// Each Task has a set of input Values, and returns a single output Value.
// Calling Task defines a task that will run a Go function when it runs. That
// function must take a context.Context or *TaskContext, followed by arguments
// corresponding to the dynamic type of the Values passed to it. It must return
// a value of any type and an error. The TaskContext can be used as a normal
// Context, and also supports workflow features like unstructured logging.
// A task only runs once all of its inputs are ready. All task outputs must be
// used either as inputs to another task or as a workflow Output.
//
// In addition to Tasks, a workflow can have Actions, which represent functions
// that don't produce an output. Their Go function must only return an error,
// and their definition results in a Dependency rather than a Value. Both
// Dependencies and Values can be passed to After and then to Task and Action
// definitions to create an ordering dependency that doesn't correspond to a
// function argument.
//
// Once a Definition is complete, call Start to set its parameters and
// instantiate it into a Workflow. Call Run to execute the workflow until
// completion.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
)

// New creates a new workflow definition.
func New() *Definition {
	return &Definition{
		definitionState: &definitionState{
			tasks:   make(map[string]*taskDefinition),
			outputs: make(map[string]*taskDefinition),
		},
	}
}

// A Definition defines the structure of a workflow.
type Definition struct {
	namePrefix string // For sub-workflows, the prefix that will be prepended to various names.
	*definitionState
}

func (d *Definition) Sub(name string) *Definition {
	return &Definition{
		namePrefix:      name + ": " + d.namePrefix,
		definitionState: d.definitionState,
	}
}

func (d *Definition) name(name string) string {
	return d.namePrefix + name
}

type definitionState struct {
	parameters []MetaParameter // Ordered according to registration, unique parameter names.
	tasks      map[string]*taskDefinition
	outputs    map[string]*taskDefinition
}

// A TaskOption affects the execution of a task but is not an argument to its function.
type TaskOption interface {
	taskOption()
}

// A Value is a piece of data that will be produced or consumed when a task
// runs. It cannot be read directly.
type Value[T any] interface {
	// This function prevents Values of different types from being convertible
	// to each other.
	valueType(T)
	metaValue
}

type metaValue interface {
	Dependency
	typ() reflect.Type
	value(*Workflow) reflect.Value
}

type MetaParameter interface {
	// RequireNonZero reports whether parameter p is required to have a non-zero value.
	RequireNonZero() bool
	Name() string
	Type() reflect.Type
	HTMLElement() string
	HTMLInputType() string
	Doc() string
	Example() string
}

// ParamDef describes a Value that is filled in at workflow creation time.
//
// It can be registered to a workflow with the Parameter function.
type ParamDef[T any] struct {
	Name         string // Name identifies the parameter within a workflow. Must be non-empty.
	ParamType[T]        // Parameter type. For strings, defaults to BasicString if not specified.
	Doc          string // Doc documents the parameter. Optional.
	Example      string // Example is an example value. Optional.
}

// parameter adds Value methods to ParamDef, so that users can't accidentally
// use a ParamDef without registering it.
type parameter[T any] struct {
	d ParamDef[T]
}

func (p parameter[T]) Name() string          { return p.d.Name }
func (p parameter[T]) Type() reflect.Type    { return p.typ() }
func (p parameter[T]) HTMLElement() string   { return p.d.HTMLElement }
func (p parameter[T]) HTMLInputType() string { return p.d.HTMLInputType }
func (p parameter[T]) Doc() string           { return p.d.Doc }
func (p parameter[T]) Example() string       { return p.d.Example }
func (p parameter[T]) RequireNonZero() bool {
	return !strings.HasSuffix(p.d.Name, " (optional)")
}

func (p parameter[T]) valueType(T) {}
func (p parameter[T]) typ() reflect.Type {
	var zero T
	return reflect.TypeOf(zero)
}
func (p parameter[T]) value(w *Workflow) reflect.Value { return reflect.ValueOf(w.params[p.d.Name]) }
func (p parameter[T]) dependencies() []*taskDefinition { return nil }

// ParamType defines the type of a workflow parameter.
//
// Since parameters are entered via an HTML form,
// there are some HTML-related knobs available.
type ParamType[T any] struct {
	// HTMLElement configures the HTML element for entering the parameter value.
	// Supported values are "input" and "textarea".
	HTMLElement string
	// HTMLInputType optionally configures the <input> type attribute when HTMLElement is "input".
	// If this attribute is not specified, <input> elements default to type="text".
	// See https://developer.mozilla.org/en-US/docs/Web/HTML/Element/input#input_types.
	HTMLInputType string
}

var (
	// String parameter types.
	BasicString = ParamType[string]{
		HTMLElement: "input",
	}
	URL = ParamType[string]{
		HTMLElement:   "input",
		HTMLInputType: "url",
	}

	// Slice of string parameter types.
	SliceShort = ParamType[[]string]{
		HTMLElement: "input",
	}
	SliceLong = ParamType[[]string]{
		HTMLElement: "textarea",
	}
)

// Param registers a new parameter p that is filled in at
// workflow creation time and returns the corresponding Value.
// Param name must be non-empty and uniquely identify the
// parameter in the workflow definition.
func Param[T any](d *Definition, p ParamDef[T]) Value[T] {
	if p.Name == "" {
		panic(fmt.Errorf("parameter name must be non-empty"))
	}
	p.Name = d.name(p.Name)
	if p.HTMLElement == "" {
		var zero T
		switch any(zero).(type) {
		case string:
			p.HTMLElement = "input"
		default:
			panic(fmt.Errorf("must specify ParamType for %T", zero))
		}
	}
	for _, old := range d.parameters {
		if p.Name == old.Name() {
			panic(fmt.Errorf("parameter with name %q was already registered with this workflow definition", p.Name))
		}
	}
	d.parameters = append(d.parameters, parameter[T]{p})
	return parameter[T]{p}
}

// Parameters returns parameters associated with the Definition
// in the same order that they were registered.
func (d *Definition) Parameters() []MetaParameter {
	return d.parameters
}

// Const creates a Value from an existing object.
func Const[T any](value T) Value[T] {
	return &constant[T]{value}
}

type constant[T any] struct {
	v T
}

func (c *constant[T]) valueType(T) {}
func (c *constant[T]) typ() reflect.Type {
	var zero []T
	return reflect.TypeOf(zero)
}
func (c *constant[T]) value(_ *Workflow) reflect.Value { return reflect.ValueOf(c.v) }
func (c *constant[T]) dependencies() []*taskDefinition { return nil }

// Slice combines multiple Values of the same type into a Value containing
// a slice of that type.
func Slice[T any](vs ...Value[T]) Value[[]T] {
	return &slice[T]{vals: vs}
}

type slice[T any] struct {
	vals []Value[T]
}

func (s *slice[T]) valueType([]T) {}

func (s *slice[T]) typ() reflect.Type {
	var zero []T
	return reflect.TypeOf(zero)
}

func (s *slice[T]) value(w *Workflow) reflect.Value {
	value := reflect.ValueOf(make([]T, len(s.vals)))
	for i, v := range s.vals {
		value.Index(i).Set(v.value(w))
	}
	return value
}

func (s *slice[T]) dependencies() []*taskDefinition {
	var result []*taskDefinition
	for _, v := range s.vals {
		result = append(result, v.dependencies()...)
	}
	return result
}

// Output registers a Value as a workflow output which will be returned when
// the workflow finishes.
func Output[T any](d *Definition, name string, v Value[T]) {
	tr, ok := v.(*taskResult[T])
	if !ok {
		panic(fmt.Errorf("output must be a task result, is %T", v))
	}
	d.outputs[d.name(name)] = tr.task
}

// A Dependency represents a dependency on a prior task.
type Dependency interface {
	dependencies() []*taskDefinition
}

// After represents an ordering dependency on another Task or Action. It can be
// passed in addition to any arguments to the task's function.
func After(afters ...Dependency) TaskOption {
	var deps []*taskDefinition
	for _, a := range afters {
		deps = append(deps, a.dependencies()...)
	}
	return &after{deps}
}

type after struct {
	deps []*taskDefinition
}

func (a *after) taskOption() {}

// TaskN adds a task to the workflow definition. It takes N inputs, and returns
// one output. name must uniquely identify the task in the workflow.
// f must be a function that takes a context.Context or *TaskContext argument,
// followed by one argument for each Value in inputs, corresponding to the
// Value's dynamic type. It must return two values, the first of which will
// be returned as its Value, and an error that will be used by the workflow
// engine. See the package documentation for examples.
func Task0[C context.Context, O1 any](d *Definition, name string, f func(C) (O1, error), opts ...TaskOption) Value[O1] {
	return addTask[O1](d, name, f, nil, opts)
}

func Task1[C context.Context, I1, O1 any](d *Definition, name string, f func(C, I1) (O1, error), i1 Value[I1], opts ...TaskOption) Value[O1] {
	return addTask[O1](d, name, f, []metaValue{i1}, opts)
}

func Task2[C context.Context, I1, I2, O1 any](d *Definition, name string, f func(C, I1, I2) (O1, error), i1 Value[I1], i2 Value[I2], opts ...TaskOption) Value[O1] {
	return addTask[O1](d, name, f, []metaValue{i1, i2}, opts)
}

func Task3[C context.Context, I1, I2, I3, O1 any](d *Definition, name string, f func(C, I1, I2, I3) (O1, error), i1 Value[I1], i2 Value[I2], i3 Value[I3], opts ...TaskOption) Value[O1] {
	return addTask[O1](d, name, f, []metaValue{i1, i2, i3}, opts)
}

func Task4[C context.Context, I1, I2, I3, I4, O1 any](d *Definition, name string, f func(C, I1, I2, I3, I4) (O1, error), i1 Value[I1], i2 Value[I2], i3 Value[I3], i4 Value[I4], opts ...TaskOption) Value[O1] {
	return addTask[O1](d, name, f, []metaValue{i1, i2, i3, i4}, opts)
}

func Task5[C context.Context, I1, I2, I3, I4, I5, O1 any](d *Definition, name string, f func(C, I1, I2, I3, I4, I5) (O1, error), i1 Value[I1], i2 Value[I2], i3 Value[I3], i4 Value[I4], i5 Value[I5], opts ...TaskOption) Value[O1] {
	return addTask[O1](d, name, f, []metaValue{i1, i2, i3, i4, i5}, opts)
}

func addTask[O1 any](d *Definition, name string, f interface{}, inputs []metaValue, opts []TaskOption) *taskResult[O1] {
	name = d.name(name)
	td := &taskDefinition{name: name, f: f, args: inputs}
	for _, input := range inputs {
		td.deps = append(td.deps, input.dependencies()...)
	}
	for _, opt := range opts {
		td.deps = append(td.deps, opt.(*after).deps...)
	}
	d.tasks[name] = td
	return &taskResult[O1]{td}
}

func addAction(d *Definition, name string, f interface{}, inputs []metaValue, opts []TaskOption) *dependency {
	tr := addTask[interface{}](d, name, f, inputs, opts)
	return &dependency{tr.task}
}

// ActionN adds an Action to the workflow definition. Its behavior and
// requirements are the same as Task, except that f must only return an error,
// and the result of the definition is a Dependency.
func Action0[C context.Context](d *Definition, name string, f func(C) error, opts ...TaskOption) Dependency {
	return addAction(d, name, f, nil, opts)
}

func Action1[C context.Context, I1 any](d *Definition, name string, f func(C, I1) error, i1 Value[I1], opts ...TaskOption) Dependency {
	return addAction(d, name, f, []metaValue{i1}, opts)
}

func Action2[C context.Context, I1, I2 any](d *Definition, name string, f func(C, I1, I2) error, i1 Value[I1], i2 Value[I2], opts ...TaskOption) Dependency {
	return addAction(d, name, f, []metaValue{i1, i2}, opts)
}

func Action3[C context.Context, I1, I2, I3 any](d *Definition, name string, f func(C, I1, I2, I3) error, i1 Value[I1], i2 Value[I2], i3 Value[I3], opts ...TaskOption) Dependency {
	return addAction(d, name, f, []metaValue{i1, i2, i3}, opts)
}

func Action4[C context.Context, I1, I2, I3, I4 any](d *Definition, name string, f func(C, I1, I2, I3, I4) error, i1 Value[I1], i2 Value[I2], i3 Value[I3], i4 Value[I4], opts ...TaskOption) Dependency {
	return addAction(d, name, f, []metaValue{i1, i2, i3, i4}, opts)
}

func Action5[C context.Context, I1, I2, I3, I4, I5 any](d *Definition, name string, f func(C, I1, I2, I3, I4, I5) error, i1 Value[I1], i2 Value[I2], i3 Value[I3], i4 Value[I4], i5 Value[I5], opts ...TaskOption) Dependency {
	return addAction(d, name, f, []metaValue{i1, i2, i3, i4, i5}, opts)
}

type dependency struct {
	task *taskDefinition
}

func (d *dependency) dependencies() []*taskDefinition {
	return []*taskDefinition{d.task}
}

// A TaskContext is a context.Context, plus workflow-related features.
type TaskContext struct {
	disableRetries bool
	context.Context
	Logger     Logger
	TaskName   string
	WorkflowID uuid.UUID

	watchdogTimer *time.Timer
}

func (c *TaskContext) Printf(format string, v ...interface{}) {
	c.ResetWatchdog()
	c.Logger.Printf(format, v...)
}

func (c *TaskContext) DisableRetries() {
	c.disableRetries = true
}

func (c *TaskContext) ResetWatchdog() {
	c.watchdogTimer.Reset(WatchdogDelay)
}

// A Listener is used to notify the workflow host of state changes, for display
// and persistence.
type Listener interface {
	// TaskStateChanged is called when the state of a task changes.
	// state is safe to store or modify.
	TaskStateChanged(workflowID uuid.UUID, taskID string, state *TaskState) error
	// Logger is called to obtain a Logger for a particular task.
	Logger(workflowID uuid.UUID, taskID string) Logger
}

// TaskState contains the state of a task in a running workflow. Once Finished
// is true, either Result or Error will be populated.
type TaskState struct {
	Name             string
	Started          bool
	Finished         bool
	Result           interface{}
	SerializedResult []byte
	Error            string
	RetryCount       int
}

// WorkflowState contains the shallow state of a running workflow.
type WorkflowState struct {
	ID     uuid.UUID
	Params map[string]interface{}
}

// A Logger is a debug logger passed to a task implementation.
type Logger interface {
	Printf(format string, v ...interface{})
}

type taskDefinition struct {
	name string
	args []metaValue
	deps []*taskDefinition
	f    interface{}
}

type taskResult[T any] struct {
	task *taskDefinition
}

func (tr *taskResult[T]) valueType(T) {}

func (tr *taskResult[T]) typ() reflect.Type {
	var zero []T
	return reflect.TypeOf(zero)
}

func (tr *taskResult[T]) value(w *Workflow) reflect.Value {
	return reflect.ValueOf(w.tasks[tr.task].result)
}

func (tr *taskResult[T]) dependencies() []*taskDefinition {
	return []*taskDefinition{tr.task}
}

// A Workflow is an instantiated workflow instance, ready to run.
type Workflow struct {
	ID     uuid.UUID
	def    *Definition
	params map[string]interface{}

	tasks map[*taskDefinition]*taskState
}

type taskState struct {
	def              *taskDefinition
	w                *Workflow
	started          bool
	finished         bool
	result           interface{}
	serializedResult []byte
	err              error
	retryCount       int
}

func (t *taskState) args() ([]reflect.Value, bool) {
	for _, dep := range t.def.deps {
		if depState, ok := t.w.tasks[dep]; !ok || !depState.finished || depState.err != nil {
			return nil, false
		}
	}
	var args []reflect.Value
	for _, v := range t.def.args {
		args = append(args, v.value(t.w))
	}
	return args, true
}

func (t *taskState) toExported() *TaskState {
	state := &TaskState{
		Name:             t.def.name,
		Finished:         t.finished,
		Result:           t.result,
		SerializedResult: append([]byte(nil), t.serializedResult...),
		Started:          t.started,
		RetryCount:       t.retryCount,
	}
	if t.err != nil {
		state.Error = t.err.Error()
	}
	return state
}

// Start instantiates a workflow with the given parameters.
func Start(def *Definition, params map[string]interface{}) (*Workflow, error) {
	w := &Workflow{
		ID:     uuid.New(),
		def:    def,
		params: params,
		tasks:  map[*taskDefinition]*taskState{},
	}
	if err := w.validate(); err != nil {
		return nil, err
	}
	for _, taskDef := range def.tasks {
		w.tasks[taskDef] = &taskState{def: taskDef, w: w}
	}
	return w, nil
}

func (w *Workflow) validate() error {
	// Validate tasks.
	used := map[*taskDefinition]bool{}
	for _, taskDef := range w.def.tasks {
		for _, dep := range taskDef.deps {
			used[dep] = true
		}
	}
	for _, output := range w.def.outputs {
		used[output] = true
	}
	for _, task := range w.def.tasks {
		if !used[task] {
			return fmt.Errorf("task %v is not referenced and should be deleted", task.name)
		}
	}

	// Validate parameters.
	if got, want := len(w.params), len(w.def.parameters); got != want {
		return fmt.Errorf("parameter count mismatch: workflow instance has %d, but definition has %d", got, want)
	}
	paramDefs := map[string]MetaParameter{} // Key is parameter name.
	for _, p := range w.def.parameters {
		if _, ok := w.params[p.Name()]; !ok {
			return fmt.Errorf("parameter name mismatch: workflow instance doesn't have %q, but definition requires it", p.Name())
		}
		paramDefs[p.Name()] = p
	}
	for name, v := range w.params {
		if !paramDefs[name].Type().AssignableTo(reflect.TypeOf(v)) {
			return fmt.Errorf("parameter type mismatch: value of parameter %q has type %v, but definition specifies %v", name, reflect.TypeOf(v), paramDefs[name].Type())
		}
	}

	return nil
}

// Resume restores a workflow from stored state. Tasks that had not finished
// will be restarted, but tasks that finished in errors will not be retried.
//
// The host must create the WorkflowState. TaskStates should be saved from
// listener callbacks, but for ease of storage, their Result field does not
// need to be populated.
func Resume(def *Definition, state *WorkflowState, taskStates map[string]*TaskState) (*Workflow, error) {
	w := &Workflow{
		ID:     state.ID,
		def:    def,
		params: state.Params,
		tasks:  map[*taskDefinition]*taskState{},
	}
	if err := w.validate(); err != nil {
		return nil, err
	}
	for _, taskDef := range def.tasks {
		tState, ok := taskStates[taskDef.name]
		if !ok {
			return nil, fmt.Errorf("task state for %q not found", taskDef.name)
		}
		state := &taskState{
			def:              taskDef,
			w:                w,
			started:          tState.Finished, // Can't resume tasks, so either it's new or done.
			finished:         tState.Finished,
			serializedResult: tState.SerializedResult,
			retryCount:       tState.RetryCount,
		}
		if state.serializedResult != nil {
			result, err := unmarshalNew(reflect.ValueOf(taskDef.f).Type().Out(0), tState.SerializedResult)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal result of %v: %v", taskDef.name, err)
			}
			state.result = result
		}
		if tState.Error != "" {
			state.err = fmt.Errorf("serialized error: %v", tState.Error) // untyped, but hopefully that doesn't matter.
		}
		w.tasks[taskDef] = state
	}
	return w, nil
}

func unmarshalNew(t reflect.Type, data []byte) (interface{}, error) {
	ptr := reflect.New(t)
	if err := json.Unmarshal(data, ptr.Interface()); err != nil {
		return nil, err
	}
	return ptr.Elem().Interface(), nil
}

// Run runs a workflow to completion or quiescence and returns its outputs.
// listener.TaskStateChanged will be called immediately, when each task starts,
// and when they finish. It should be used only for monitoring and persistence
// purposes. Register Outputs to read task results.
func (w *Workflow) Run(ctx context.Context, listener Listener) (map[string]interface{}, error) {
	if listener == nil {
		listener = &defaultListener{}
	}

	for _, task := range w.tasks {
		listener.TaskStateChanged(w.ID, task.def.name, task.toExported())
	}

	stateChan := make(chan taskState, 2*len(w.def.tasks))
	for {
		// If we have all the outputs, the workflow is done.
		outValues := map[string]interface{}{}
		for outName, outDef := range w.def.outputs {
			if task := w.tasks[outDef]; task.finished && task.err == nil {
				outValues[outName] = task.result
			}
		}
		if len(outValues) == len(w.def.outputs) {
			return outValues, nil
		}

		running := 0
		for _, task := range w.tasks {
			if task.started && !task.finished {
				running++
			}
		}

		if ctx.Err() == nil {
			// Start any idle tasks whose dependencies are all done.
			for _, task := range w.tasks {
				if task.started {
					continue
				}
				in, ready := task.args()
				if !ready {
					continue
				}
				task.started = true
				running++
				listener.TaskStateChanged(w.ID, task.def.name, task.toExported())
				go func(task taskState) {
					stateChan <- w.runTask(ctx, listener, task, in)
				}(*task)
			}
		}

		// Exit if we've run everything we can given errors.
		if running == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, fmt.Errorf("workflow has progressed as far as it can")
			}
		}

		state := <-stateChan
		listener.TaskStateChanged(w.ID, state.def.name, state.toExported())
		w.tasks[state.def] = &state
	}
}

// Maximum number of retries. This could be a workflow property.
var MaxRetries = 3

var WatchdogDelay = 10 * time.Minute

func (w *Workflow) runTask(ctx context.Context, listener Listener, state taskState, args []reflect.Value) taskState {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tctx := &TaskContext{
		Context:       ctx,
		Logger:        listener.Logger(w.ID, state.def.name),
		TaskName:      state.def.name,
		WorkflowID:    w.ID,
		watchdogTimer: time.AfterFunc(WatchdogDelay, cancel),
	}

	in := append([]reflect.Value{reflect.ValueOf(tctx)}, args...)
	fv := reflect.ValueOf(state.def.f)
	out := fv.Call(in)

	if  !tctx.watchdogTimer.Stop() {
		state.err = fmt.Errorf("task did not log for %v, assumed hung", WatchdogDelay)
	} else if errIdx := len(out) - 1; !out[errIdx].IsNil() {
		state.err = out[errIdx].Interface().(error)
	}
	state.finished = true
	if len(out) == 2 && state.err == nil {
		state.serializedResult, state.err = json.Marshal(out[0].Interface())
		if state.err == nil {
			state.result, state.err = unmarshalNew(fv.Type().Out(0), state.serializedResult)
		}
		if state.err == nil && !reflect.DeepEqual(out[0].Interface(), state.result) {
			state.err = fmt.Errorf("JSON marshaling changed result from %#v to %#v", out[0].Interface(), state.result)
		}
	}

	if state.err != nil && !tctx.disableRetries && state.retryCount+1 < MaxRetries {
		tctx.Printf("task failed, will retry (%v of %v): %v", state.retryCount+1, MaxRetries, state.err)
		state = taskState{
			def:        state.def,
			w:          state.w,
			retryCount: state.retryCount + 1,
		}
	}
	return state
}

type defaultListener struct{}

func (s *defaultListener) TaskStateChanged(_ uuid.UUID, _ string, _ *TaskState) error {
	return nil
}

func (s *defaultListener) Logger(_ uuid.UUID, task string) Logger {
	return &defaultLogger{}
}

type defaultLogger struct{}

func (l *defaultLogger) Printf(format string, v ...interface{}) {}
