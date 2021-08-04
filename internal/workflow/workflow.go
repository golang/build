// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package workflow declaratively defines computation graphs that support
// automatic parallelization, persistance, and monitoring.
//
// Workflows are a set of tasks that produce and consume Values. Tasks don't
// run until the workflow is started, so Values represent data that doesn't
// exist yet, and can't be used directly. Each value has a dynamic type, which
// must match its uses.
//
// To wrap an existing Go object in a Value, use Constant. To define a
// parameter that will be set when the workflow is started, use Parameter.
// To read a task's return value, register it as an Output, and it will be
// returned from Run. An arbitrary number of Values of the same type can
// be combined with Slice.
//
// Each task has a set of input Values, and returns a single output Value.
// Calling Task defines a task that will run a Go function when it runs. That
// function must take a context.Context, followed by arguments corresponding to
// the dynamic type of the Values passed to it.
//
// Once a Definition is complete, call Start to set its parameters and
// instantiate it into a Workflow. Call Run to execute the workflow until
// completion.
package workflow

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/uuid"
)

// New creates a new workflow definition.
func New() *Definition {
	return &Definition{
		parameterNames: map[string]struct{}{},
		tasks:          map[string]*taskDefinition{},
		outputs:        map[string]*taskResult{},
	}
}

// A Definition defines the structure of a workflow.
type Definition struct {
	parameterNames map[string]struct{}
	tasks          map[string]*taskDefinition
	outputs        map[string]*taskResult
}

// A Value is a piece of data that will be produced or consumed when a task
// runs. It cannot be read directly.
type Value interface {
	typ() reflect.Type
	value(*Workflow) reflect.Value
	deps() []*taskDefinition
}

// Parameter creates a Value that is filled in at workflow creation time.
func (d *Definition) Parameter(name string) Value {
	d.parameterNames[name] = struct{}{}
	return &workflowParameter{name: name}
}

type workflowParameter struct {
	name string
}

func (wp *workflowParameter) typ() reflect.Type {
	return reflect.TypeOf("")
}

func (wp *workflowParameter) value(w *Workflow) reflect.Value {
	return reflect.ValueOf(w.params[wp.name])
}

func (wp *workflowParameter) deps() []*taskDefinition {
	return nil
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
func (d *Definition) Slice(vs []Value) Value {
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
	d.outputs[name] = tr
}

// Task adds a task to the workflow definition. It can take any number of
// arguments, and returns one output. name must uniquely identify the task in
// the workflow.
// f must be a function that takes a context.Context argument, followed by one
// argument for each of args, corresponding to the Value's dynamic type.
// It must return two values, the first of which will be returned as its Value,
// and an error that will be used by the workflow engine. See the package
// documentation for examples.
func (d *Definition) Task(name string, f interface{}, args ...Value) Value {
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
	if ftyp.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() {
		panic(fmt.Errorf("the first argument of %v must be a context.Context, is %v", f, ftyp.In(0)))
	}
	for i, arg := range args {
		if !arg.typ().AssignableTo(ftyp.In(i + 1)) {
			panic(fmt.Errorf("argument %v to %v is %v, but was passed %v", i, f, ftyp.In(i+1), arg.typ()))
		}
	}
	if ftyp.NumOut() != 2 {
		panic(fmt.Errorf("%v returns %v results, must return 2", f, ftyp.NumOut()))
	}
	if ftyp.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		panic(fmt.Errorf("%v's second return value must be error, is %v", f, ftyp.Out(1)))
	}
	td := &taskDefinition{name: name, args: args, f: f}
	d.tasks[name] = td
	return &taskResult{task: td}
}

type taskDefinition struct {
	name string
	args []Value
	f    interface{}
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
	id     string
	def    *Definition
	params map[string]string

	tasks map[*taskDefinition]*taskState
}

type taskState struct {
	def      *taskDefinition
	w        *Workflow
	started  bool
	finished bool
	result   interface{}
	err      error
}

func (t *taskState) args() ([]reflect.Value, bool) {
	var args []reflect.Value
	for _, arg := range t.def.args {
		for _, dep := range arg.deps() {
			if depState, ok := t.w.tasks[dep]; !ok || !depState.finished || depState.err != nil {
				return nil, false
			}
		}
		args = append(args, arg.value(t.w))
	}
	return args, true
}

func (t *taskState) toExported() *TaskState {
	return &TaskState{
		Name:     t.def.name,
		Finished: t.finished,
		Result:   t.result,
		Error:    t.err,
	}
}

// TaskState contains the state of a task in a running workflow. Once finished
// is true, either Result or Error will be populated.
type TaskState struct {
	Name     string
	Finished bool
	Result   interface{}
	Error    error
}

// Start instantiates a workflow with the given parameters.
func Start(def *Definition, params map[string]string) (*Workflow, error) {
	w := &Workflow{
		id:     uuid.New().String(),
		def:    def,
		params: params,
		tasks:  map[*taskDefinition]*taskState{},
	}
	used := map[*taskDefinition]bool{}
	for _, taskDef := range def.tasks {
		w.tasks[taskDef] = &taskState{def: taskDef, w: w}
		for _, arg := range taskDef.args {
			for _, argDep := range arg.deps() {
				used[argDep] = true
			}
		}
	}
	for _, output := range def.outputs {
		used[output.task] = true
	}
	for _, task := range def.tasks {
		if !used[task] {
			return nil, fmt.Errorf("task %v is not referenced and should be deleted", task.name)
		}
	}
	return w, nil
}

// Run runs a workflow to successful completion and returns its outputs.
// statusFunc will be called when each task starts and finishes. It should be
// used only for monitoring purposes - to read task results, register Outputs.
func (w *Workflow) Run(ctx context.Context, stateFunc func(*TaskState)) (map[string]interface{}, error) {
	var running sync.WaitGroup
	defer running.Wait()

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
			stateFunc(task.toExported())
			running.Add(1)
			go func(task taskState) {
				stateChan <- w.runTask(ctx, task, in)
				running.Done()
			}(*task)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case state := <-stateChan:
			w.tasks[state.def] = &state
			stateFunc(state.toExported())
		}
	}
}

func (w *Workflow) runTask(ctx context.Context, state taskState, args []reflect.Value) taskState {
	in := append([]reflect.Value{reflect.ValueOf(ctx)}, args...)
	out := reflect.ValueOf(state.def.f).Call(in)
	var err error
	if !out[1].IsNil() {
		err = out[1].Interface().(error)
	}
	state.finished = true
	state.result, state.err = out[0].Interface(), err
	return state
}
