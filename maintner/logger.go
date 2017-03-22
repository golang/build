// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package maintner

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"golang.org/x/build/maintner/maintpb"
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
	return filepath.Join(d.directory, fmt.Sprintf("maintner-%s.proto.gz", now.Format("2006-01-02")))
}

// Log will write m to disk. If a mutation file does not exist for the current
// day, it will be created.
func (d *DiskMutationLogger) Log(m *maintpb.Mutation) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	// TODO lock the file for writing
	f, err := os.OpenFile(d.filename(), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}
	return f.Close()
}

func (d *DiskMutationLogger) GetMutations(ctx context.Context) <-chan *maintpb.Mutation {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ch := make(chan *maintpb.Mutation, 50) // buffered: overlap gunzip/unmarshal with loading
	if d.directory == "" {
		panic("empty directory")
	}
	go func() {
		// Walk guarantees that files are walked in lexical order, which we depend on.
		err := filepath.Walk(d.directory, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() && path != filepath.Clean(d.directory) {
				return filepath.SkipDir
			}
			if !strings.HasPrefix(fi.Name(), "maintner-") {
				return nil
			}
			if !strings.HasSuffix(fi.Name(), ".proto.gz") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			br := bufio.NewReader(f)
			zr, err := gzip.NewReader(br)
			if err != nil {
				return err
			}
			for {
				zr.Multistream(false)
				rec, err := ioutil.ReadAll(zr)
				if err != nil {
					return err
				}
				m := new(maintpb.Mutation)
				if err := proto.Unmarshal(rec, m); err != nil {
					return err
				}
				select {
				case ch <- m:
					break
				case <-ctx.Done():
					return ctx.Err()
				}
				err = zr.Reset(br)
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
			}
			if err := f.Close(); err != nil {
				return err
			}
			return zr.Close()
		})
		if err != nil {
			log.Printf("error walking directory %s: %v", d.directory, err)
		}
		close(ch)
	}()
	return ch
}
