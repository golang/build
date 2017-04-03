// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"golang.org/x/build/maintner/maintpb"
	"golang.org/x/build/maintner/reclog"
)

// A MutationLogger logs mutations.
type MutationLogger interface {
	Log(*maintpb.Mutation) error
}

// DiskMutationLogger logs mutations to disk.
type DiskMutationLogger struct {
	directory string
	mu        sync.RWMutex
}

// NewDiskMutationLogger creates a new DiskMutationLogger, which will create
// mutations in the given directory.
func NewDiskMutationLogger(directory string) *DiskMutationLogger {
	if directory == "" {
		panic("empty directory")
	}
	return &DiskMutationLogger{directory: directory}
}

// filename returns the filename to write to. The oldest filename must come
// first in lexical order.
func (d *DiskMutationLogger) filename() string {
	now := time.Now().UTC()
	return filepath.Join(d.directory, fmt.Sprintf("maintner-%s.mutlog", now.Format("2006-01-02")))
}

// Log will write m to disk. If a mutation file does not exist for the current
// day, it will be created.
func (d *DiskMutationLogger) Log(m *maintpb.Mutation) error {
	data, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return reclog.AppendRecordToFile(d.filename(), data)
}

func (d *DiskMutationLogger) ForeachFile(fn func(fullPath string, fi os.FileInfo) error) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.directory == "" {
		panic("empty directory")
	}
	// Walk guarantees that files are walked in lexical order, which we depend on.
	return filepath.Walk(d.directory, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && path != filepath.Clean(d.directory) {
			return filepath.SkipDir
		}
		if !strings.HasPrefix(fi.Name(), "maintner-") {
			return nil
		}
		if !strings.HasSuffix(fi.Name(), ".mutlog") {
			return nil
		}
		return fn(path, fi)
	})
}

func (d *DiskMutationLogger) GetMutations(ctx context.Context) <-chan *maintpb.Mutation {
	ch := make(chan *maintpb.Mutation, 50) // buffered: overlap gunzip/unmarshal with loading
	go func() {
		defer close(ch)
		err := d.ForeachFile(func(fullPath string, fi os.FileInfo) error {
			return reclog.ForeachFileRecord(fullPath, func(off int64, hdr, rec []byte) error {
				m := new(maintpb.Mutation)
				if err := proto.Unmarshal(rec, m); err != nil {
					return err
				}
				select {
				case ch <- m:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		})
		if err != nil {
			panic(err)
		}
	}()
	return ch
}
