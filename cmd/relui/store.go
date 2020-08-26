// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"github.com/golang/protobuf/proto"
	reluipb "golang.org/x/build/cmd/relui/protos"
)

// store is a persistence adapter for saving data.
type store interface {
	Workflows() []*reluipb.Workflow
	AddWorkflow(workflow *reluipb.Workflow) error
}

var _ store = (*fileStore)(nil)

// newFileStore initializes a fileStore ready for use.
//
// If dir is set to an empty string (""), no data will be saved to disk.
func newFileStore(dir string) *fileStore {
	return &fileStore{
		persistDir: dir,
		ls:         new(reluipb.LocalStorage),
	}
}

// fileStoreName is the name of the data file used by fileStore for persistence.
const fileStoreName = "local_storage.textpb"

// fileStore is a non-durable implementation of store that keeps everything in memory.
type fileStore struct {
	mu sync.Mutex
	ls *reluipb.LocalStorage

	// persistDir is a path to a directory for saving application data in textproto format.
	// Set persistDir to an empty string to disable saving and loading from the filesystem.
	persistDir string
}

// AddWorkflow adds a workflow to the store, persisting changes to disk.
func (f *fileStore) AddWorkflow(w *reluipb.Workflow) error {
	f.mu.Lock()
	f.ls.Workflows = append(f.ls.Workflows, w)
	f.mu.Unlock()
	if err := f.persist(); err != nil {
		return err
	}
	return nil
}

// Workflows returns all workflows stored.
func (f *fileStore) Workflows() []*reluipb.Workflow {
	return f.localStorage().GetWorkflows()
}

// localStorage returns a deep copy of data stored in fileStore.
func (f *fileStore) localStorage() *reluipb.LocalStorage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return proto.Clone(f.ls).(*reluipb.LocalStorage)
}

// persist saves fileStore state to persistDir/fileStoreName.
func (f *fileStore) persist() error {
	if f.persistDir == "" {
		return nil
	}
	if err := os.MkdirAll(f.persistDir, 0755); err != nil {
		return fmt.Errorf("os.MkDirAll(%q, %v) = %w", f.persistDir, 0755, err)
	}
	dst := filepath.Join(f.persistDir, fileStoreName)
	data := []byte(proto.MarshalTextString(f.localStorage()))
	if err := ioutil.WriteFile(dst, data, 0644); err != nil {
		return fmt.Errorf("ioutil.WriteFile(%q, _, %v) = %w", dst, 0644, err)
	}
	return nil
}

// load reads fileStore state from persistDir/fileStoreName.
func (f *fileStore) load() error {
	if f.persistDir == "" {
		return nil
	}
	path := filepath.Join(f.persistDir, fileStoreName)
	b, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("ioutil.ReadFile(%q) = _, %v", path, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return proto.UnmarshalText(string(b), f.ls)
}
