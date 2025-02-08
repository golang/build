// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"
	"io/fs"
	"math/rand"

	"cloud.google.com/go/storage"
	"golang.org/x/build/internal/gcsfs"
	wf "golang.org/x/build/internal/workflow"
)

// ScratchFS manages scratch storage for workflows.
type ScratchFS struct {
	BaseURL string // BaseURL is a gs:// or file:// URL, no trailing slash. E.g., "gs://golang-release-staging/relui-scratch".
	GCS     *storage.Client
}

// OpenRead opens a file in the workflow's scratch storage.
func (s *ScratchFS) OpenRead(ctx *wf.TaskContext, name string) (fs.File, error) {
	sfs, err := s.fs(ctx)
	if err != nil {
		return nil, err
	}
	return sfs.Open(name)
}

// ReadFile fully reads a file in the workflow's scratch storage.
func (s *ScratchFS) ReadFile(ctx *wf.TaskContext, name string) ([]byte, error) {
	sfs, err := s.fs(ctx)
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(sfs, name)
}

// OpenWrite creates a new file in the workflow's scratch storage, with a name
// based on baseName. It returns that name, as well as the open file.
func (s *ScratchFS) OpenWrite(ctx *wf.TaskContext, baseName string) (name string, _ gcsfs.WriterFile, _ error) {
	sfs, err := s.fs(ctx)
	if err != nil {
		return "", nil, err
	}
	name = fmt.Sprintf("%v-%v", rand.Int63(), baseName)
	f, err := gcsfs.Create(sfs, name)
	return name, f, err
}

// WriteFilename returns a filename that can be used to write a new scratch file
// suitable for writing from an external systems.
func (s *ScratchFS) WriteFilename(ctx *wf.TaskContext, baseName string) string {
	return fmt.Sprintf("%v-%v", rand.Int63(), baseName)
}

// URL returns the URL of a file in the workflow's scratch storage, suitable
// for passing to external systems.
func (s *ScratchFS) URL(ctx *wf.TaskContext, name string) string {
	return fmt.Sprintf("%v/%v/%v", s.BaseURL, ctx.WorkflowID.String(), name)
}

func (s *ScratchFS) fs(ctx *wf.TaskContext) (fs.FS, error) {
	sfs, err := gcsfs.FromURL(ctx, s.GCS, s.BaseURL)
	if err != nil {
		return nil, err
	}
	return fs.Sub(sfs, ctx.WorkflowID.String())
}
