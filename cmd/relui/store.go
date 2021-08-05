// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package main

import (
	"context"
	"log"

	"cloud.google.com/go/datastore"
	"github.com/googleapis/google-cloud-go-testing/datastore/dsiface"
	reluipb "golang.org/x/build/cmd/relui/protos"
)

// store is a persistence interface for saving data.
type store interface {
	AddWorkflow(workflow *reluipb.Workflow) error
	BuildableTask(workflowId, id string) *reluipb.BuildableTask
	Workflow(id string) *reluipb.Workflow
	Workflows() []*reluipb.Workflow
}

var _ store = (*dsStore)(nil)

// dsStore is a store backed by Google Cloud Datastore.
type dsStore struct {
	client dsiface.Client
}

// AddWorkflow adds a reluipb.Workflow to the database.
func (d *dsStore) AddWorkflow(wf *reluipb.Workflow) error {
	key := datastore.NameKey("Workflow", wf.GetId(), nil)
	_, err := d.client.Put(context.TODO(), key, wf)
	return err
}

// BuildableTask fetches a reluipb.BuildableTask from the database.
func (d *dsStore) BuildableTask(workflowId, id string) *reluipb.BuildableTask {
	wf := d.Workflow(workflowId)
	for _, bt := range wf.GetBuildableTasks() {
		if bt.GetId() == id {
			return bt
		}
	}
	return nil
}

// Workflow fetches a reluipb.Workflow from the database.
func (d *dsStore) Workflow(id string) *reluipb.Workflow {
	key := datastore.NameKey("Workflow", id, nil)
	wf := new(reluipb.Workflow)
	if err := d.client.Get(context.TODO(), key, wf); err != nil {
		log.Printf("d.client.Get(_, %q, %v) = %v", key, wf, err)
		return nil
	}
	return wf
}

// Workflows returns all reluipb.Workflow entities from the database.
func (d *dsStore) Workflows() []*reluipb.Workflow {
	var wfs []*reluipb.Workflow
	if _, err := d.client.GetAll(context.TODO(), datastore.NewQuery("Workflow"), &wfs); err != nil {
		log.Printf("d.client.GetAll(_, %#v, %v) = %v", datastore.NewQuery("Workflow"), &wfs, err)
		return nil
	}
	return wfs
}
