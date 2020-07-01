// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"sync"
)

type workflow interface {
	// Params are the list of parameters given when the workflow was created.
	Params() map[string]string
	// Title is a human-readable description of a task.
	Title() string
	// Tasks are a list of steps in a workflow.
	Tasks() []task
}

type task interface {
	// Title is a human-readable description of a task.
	Title() string
	// Status is the current status of the task.
	Status() string
}

// newLocalGoRelease creates a localGoRelease workflow.
func newLocalGoRelease(revision string) *localGoRelease {
	return &localGoRelease{GitObject: revision, tasks: []task{&fetchGoSource{gitObject: revision}}}
}

type localGoRelease struct {
	GitObject string
	tasks     []task
}

func (l *localGoRelease) Params() map[string]string {
	return map[string]string{"GitObject": l.GitObject}
}

func (l *localGoRelease) Title() string {
	return fmt.Sprintf("Local Go release (%s)", l.GitObject)
}

func (l *localGoRelease) Tasks() []task {
	return l.tasks
}

// fetchGoSource is a task for fetching the Go repository at a specific commit reference.
type fetchGoSource struct {
	gitObject string
}

func (f *fetchGoSource) Title() string {
	return "Fetch Go source at " + f.gitObject
}

func (f *fetchGoSource) Status() string {
	return "created"
}

// store is a persistence adapter for saving data. When running locally, this is implemented by memoryStore.
type store interface {
	GetWorkflows() []workflow
	AddWorkflow(workflow) error
}

// memoryStore is a non-durable implementation of store that keeps everything in memory.
type memoryStore struct {
	sync.Mutex
	Workflows []workflow
}

// AddWorkflow adds a workflow to the store.
func (m *memoryStore) AddWorkflow(w workflow) error {
	m.Lock()
	defer m.Unlock()
	m.Workflows = append(m.Workflows, w)
	return nil
}

// GetWorkflows returns all workflows stored.
func (m *memoryStore) GetWorkflows() []workflow {
	m.Lock()
	defer m.Unlock()
	return m.Workflows
}
