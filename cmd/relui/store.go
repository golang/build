// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"sync"

	reluipb "golang.org/x/build/cmd/relui/protos"
)

// store is a persistence adapter for saving data.
type store interface {
	GetWorkflows() []*reluipb.Workflow
	AddWorkflow(workflow *reluipb.Workflow) error
}

var _ store = (*memoryStore)(nil)

// memoryStore is a non-durable implementation of store that keeps everything in memory.
type memoryStore struct {
	mut       sync.Mutex
	workflows []*reluipb.Workflow
}

// AddWorkflow adds a workflow to the store.
func (m *memoryStore) AddWorkflow(w *reluipb.Workflow) error {
	m.mut.Lock()
	defer m.mut.Unlock()
	m.workflows = append(m.workflows, w)
	return nil
}

// GetWorkflows returns all workflows stored.
//
// TODO(golang.org/issue/40279) - clone workflows if they're ever mutated.
func (m *memoryStore) GetWorkflows() []*reluipb.Workflow {
	m.mut.Lock()
	defer m.mut.Unlock()
	return m.workflows
}
