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
// To wrap an existing Go object in a Value, use Constant. To define a
// parameter that will be set when the workflow is started, use Parameter.
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
// A task only runs once all of its input Values and Dependencies are ready.
// All task outputs must be used either as inputs to another task or as a
// workflow Output.
//
// In addition to Tasks, a workflow can have Actions, which represent functions
// that don't produce an output. Their Go function must only return an error,
// and their definition results in a Dependency rather than a Value.
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

	"github.com/google/uuid"
)

// New creates a new workflow definition.
func New() *Definition {
	return &Definition{
		definitionState: &definitionState{
			tasks:   make(map[string]*taskDefinition),
			outputs: make(map[string]*taskResult),
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
	parameters []Parameter // Ordered according to registration, unique parameter names.
	tasks      map[string]*taskDefinition
	outputs    map[string]*taskResult
}

// A TaskInput is any input to the definition of a task.
type TaskInput interface {
	deps() []*taskDefinition
}

// A Value is a piece of data that will be produced or consumed when a task
// runs. It cannot be read directly.
type Value interface {
	TaskInput
	typ() reflect.Type
	value(*Workflow) reflect.Value
}

// A Dependency represents a dependency on a prior task that does not produce
// any in-band Value.
type Dependency interface {
	TaskInput
	dependencyOnly()
}

// Parameter describes a Value that is filled in at workflow creation time.
//
// It can be registered to a workflow with the Workflow.Parameter method.
type Parameter struct {
	Name          string // Name identifies the parameter within a workflow. Must be non-empty.
	ParameterType        // Parameter type. Defaults to BasicString if not specified.
	Doc           string // Doc documents the parameter. Optional.
	Example       string // Example is an example value. Optional.
}

// RequireNonZero reports whether parameter p is required to have a non-zero value.
func (p Parameter) RequireNonZero() bool {
	return !strings.HasSuffix(p.Name, " (optional)")
}

// ParameterType defines the type of a workflow parameter.
//
// Since parameters are entered via an HTML form,
// there are some HTML-related knobs available.
type ParameterType struct {
	Type reflect.Type // The Go type of the parameter.

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
	BasicString = ParameterType{
		Type:        reflect.TypeOf(""),
		HTMLElement: "input",
	}
	URL = ParameterType{
		Type:          reflect.TypeOf(""),
		HTMLElement:   "input",
		HTMLInputType: "url",
	}

	// Slice of string parameter types.
	SliceShort = ParameterType{
		Type:        reflect.TypeOf([]string(nil)),
		HTMLElement: "input",
	}
	SliceLong = ParameterType{
		Type:        reflect.TypeOf([]string(nil)),
		HTMLElement: "textarea",
	}
)

// Parameter registers a new parameter p that is filled in at
// workflow creation time and returns the corresponding Value.
// Parameter name must be non-empty and uniquely identify the
// parameter in the workflow definition.
//
// If the parameter type is unspecified, BasicString is used.
func (d *Definition) Parameter(p Parameter) Value {
	if p.Name == "" {
		panic(fmt.Errorf("parameter name must be non-empty"))
	}
	p.Name = d.name(p.Name)
	if p.ParameterType == (ParameterType{}) {
		p.ParameterType = BasicString
	}
	for _, old := range d.parameters {
		if p.Name == old.Name {
			panic(fmt.Errorf("parameter with name %q was already registered with this workflow definition", p.Name))
		}
	}
	d.parameters = append(d.parameters, p)
	return parameter(p)
}

// parameter implements Value for a workflow parameter.
type parameter Parameter

func (p parameter) typ() reflect.Type               { return p.Type }
func (p parameter) value(w *Workflow) reflect.Value { return reflect.ValueOf(w.params[p.Name]) }
func (p parameter) deps() []*taskDefinition         { return nil }

// Parameters returns parameters associated with the Definition
// in the same order that they were registered.
func (d *Definition) Parameters() []Parameter {
	return d.parameters
}

// Constant creates a Value from an existing object.
func (d *Definition) Constant(value interface{}) Value {
	return &constant{reflect.ValueOf(value)}
}

type constant struct {
	v reflect.Value
}

func (c *constant) typ() reflect.Type               { return c.v.Type() }
func (c *constant) value(_ *Workflow) reflect.Value { return c.v }
func (c *constant) deps() []*taskDefinition         { return nil }

// Slice combines multiple Values of the same type into a Value containing
// a slice of that type.
func (d *Definition) Slice(vs ...Value) Value {
	if len(vs) == 0 {
		return &slice{}
	}
	typ := vs[0].typ()
	for _, v := range vs[1:] {
		if v.typ() != typ {
			panic(fmt.Errorf("mismatched value types in Slice: %v vs. %v", v.typ(), typ))
		}
	}
	return &slice{elt: typ, vals: vs}
}

type slice struct {
	elt  reflect.Type
	vals []Value
}

func (s *slice) typ() reflect.Type {
	return reflect.SliceOf(s.elt)
}

func (s *slice) value(w *Workflow) reflect.Value {
	value := reflect.MakeSlice(reflect.SliceOf(s.elt), len(s.vals), len(s.vals))
	for i, v := range s.vals {
		value.Index(i).Set(v.value(w))
	}
	return value
}

func (s *slice) deps() []*taskDefinition {
	var result []*taskDefinition
	for _, v := range s.vals {
		result = append(result, v.deps()...)
	}
	return result
}

// Output registers a Value as a workflow output which will be returned when
// the workflow finishes.
func (d *Definition) Output(name string, v Value) {
	tr, ok := v.(*taskResult)
	if !ok {
		panic(fmt.Errorf("output must be a task result"))
	}
	d.outputs[d.name(name)] = tr
}

// Task adds a task to the workflow definition. It can take any number of
// inputs, and returns one output. name must uniquely identify the task in
// the workflow.
// f must be a function that takes a context.Context or *TaskContext argument,
// followed by one argument for each Value in inputs, corresponding to the
// Value's dynamic type. It must return two values, the first of which will
// be returned as its Value, and an error that will be used by the workflow
// engine. See the package documentation for examples.
func (d *Definition) Task(name string, f interface{}, inputs ...TaskInput) Value {
	td := d.addTask(true, name, f, inputs...)
	return &taskResult{td}
}

func (d *Definition) addTask(hasResult bool, name string, f interface{}, inputs ...TaskInput) *taskDefinition {
	name = d.name(name)
	var args []Value
	for _, arg := range inputs {
		val, ok := arg.(Value)
		if ok {
			args = append(args, val)
		}
	}
	if d.tasks[name] != nil {
		panic(fmt.Errorf("task %q already exists in the workflow", name))
	}
	ftyp := reflect.ValueOf(f).Type()
	if ftyp.Kind() != reflect.Func {
		panic(fmt.Errorf("%v is not a function", f))
	}
	if ftyp.NumIn()-1 != len(args) {
		panic(fmt.Errorf("%v takes %v non-Context arguments, but was passed %v", f, ftyp.NumIn()-1, len(args)))
	}
	if !reflect.TypeOf((*TaskContext)(nil)).AssignableTo(ftyp.In(0)) {
		panic(fmt.Errorf("the first argument of %v must be a context.Context or *TaskContext, is %v", f, ftyp.In(0)))
	}
	for i, val := range args {
		if !val.typ().AssignableTo(ftyp.In(i + 1)) {
			panic(fmt.Errorf("argument %v to %v is %v, but was passed %v", i, f, ftyp.In(i+1), val.typ()))
		}
	}
	wantOuts := 2
	if !hasResult {
		wantOuts = 1
	}
	if ftyp.NumOut() != wantOuts {
		panic(fmt.Errorf("function for task %v returns %v values, must return %v", name, ftyp.NumOut(), wantOuts))
	}
	if ftyp.Out(wantOuts-1) != reflect.TypeOf((*error)(nil)).Elem() {
		panic(fmt.Errorf("%v's last return value must be error, is %v", f, ftyp.Out(wantOuts-1)))
	}
	td := &taskDefinition{name: name, inputs: inputs, f: f}
	d.tasks[name] = td
	return td
}

// Action adds an Action to the workflow definition. Its behavior and
// requirements are the same as Task, except that f must only return an error,
// and the result of the definition is a Dependency.
func (d *Definition) Action(name string, f interface{}, inputs ...TaskInput) Dependency {
	td := d.addTask(false, name, f, inputs...)
	return &dependency{td}
}

type dependency struct {
	task *taskDefinition
}

func (d *dependency) dependencyOnly() {}

func (d *dependency) deps() []*taskDefinition {
	return []*taskDefinition{d.task}
}

// A TaskContext is a context.Context, plus workflow-related features.
type TaskContext struct {
	context.Context
	Logger
	TaskName   string
	WorkflowID uuid.UUID
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
	name   string
	inputs []TaskInput
	f      interface{}
}

type taskResult struct {
	task *taskDefinition
}

func (tr *taskResult) typ() reflect.Type {
	return reflect.ValueOf(tr.task.f).Type().Out(0)
}

func (tr *taskResult) value(w *Workflow) reflect.Value {
	return reflect.ValueOf(w.tasks[tr.task].result)
}

func (tr *taskResult) deps() []*taskDefinition {
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
}

func (t *taskState) args() ([]reflect.Value, bool) {
	var args []reflect.Value
	for _, arg := range t.def.inputs {
		for _, dep := range arg.deps() {
			if depState, ok := t.w.tasks[dep]; !ok || !depState.finished || depState.err != nil {
				return nil, false
			}
		}
		if v, ok := arg.(Value); ok {
			args = append(args, v.value(t.w))
		}
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
		for _, arg := range taskDef.inputs {
			for _, argDep := range arg.deps() {
				used[argDep] = true
			}
		}
	}
	for _, output := range w.def.outputs {
		used[output.task] = true
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
	paramDefs := map[string]Value{} // Key is parameter name.
	for _, p := range w.def.parameters {
		if _, ok := w.params[p.Name]; !ok {
			return fmt.Errorf("parameter name mismatch: workflow instance doesn't have %q, but definition requires it", p.Name)
		}
		paramDefs[p.Name] = parameter(p)
	}
	for name, v := range w.params {
		if v == nil {
			w.params[name] = reflect.New(paramDefs[name].typ()).Elem().Interface()
			continue
		}
		if !paramDefs[name].typ().AssignableTo(reflect.TypeOf(v)) {
			return fmt.Errorf("parameter type mismatch: value of parameter %q has type %v, but definition specifies %v", name, reflect.TypeOf(v), paramDefs[name].typ())
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
			if task := w.tasks[outDef.task]; task.finished && task.err == nil {
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
		w.tasks[state.def] = &state
		listener.TaskStateChanged(w.ID, state.def.name, state.toExported())
	}
}

func (w *Workflow) runTask(ctx context.Context, listener Listener, state taskState, args []reflect.Value) taskState {
	tctx := &TaskContext{
		Context:    ctx,
		Logger:     listener.Logger(w.ID, state.def.name),
		TaskName:   state.def.name,
		WorkflowID: w.ID,
	}
	in := append([]reflect.Value{reflect.ValueOf(tctx)}, args...)
	fv := reflect.ValueOf(state.def.f)
	out := fv.Call(in)

	if errIdx := len(out) - 1; !out[errIdx].IsNil() {
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
