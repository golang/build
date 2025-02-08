// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gcsfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime"
	"strings"
)

var _ = fs.FS((*dirFS)(nil))
var _ = CreateFS((*dirFS)(nil))

// DirFS is a variant of os.DirFS that supports file creation and is a suitable
// test fake for the GCS FS.
func DirFS(dir string) fs.FS {
	return dirFS(dir)
}

func containsAny(s, chars string) bool {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return true
			}
		}
	}
	return false
}

type dirFS string

func (dir dirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || runtime.GOOS == "windows" && containsAny(name, `\:`) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	f, err := os.Open(string(dir) + "/" + name)
	if err != nil {
		return nil, err // nil fs.File
	}
	return &atomicWriteFile{f, nil}, nil
}

func (dir dirFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) || runtime.GOOS == "windows" && containsAny(name, `\:`) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	f, err := os.Stat(string(dir) + "/" + name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (dir dirFS) Create(name string) (WriterFile, error) {
	if !fs.ValidPath(name) || runtime.GOOS == "windows" && containsAny(name, `\:`) {
		return nil, &fs.PathError{Op: "create", Path: name, Err: fs.ErrInvalid}
	}
	fullName := path.Join(string(dir), name)
	if err := os.MkdirAll(path.Dir(fullName), 0700); err != nil {
		return nil, err
	}

	// GCS doesn't let you see a file until you're done writing it. Write
	// to a temp file, which will be renamed to the expected name on Close.
	temp, err := os.CreateTemp(path.Dir(fullName), "."+path.Base(fullName)+".writing-*")
	if err != nil {
		return nil, err
	}
	finalize := func() error {
		if _, err := os.Stat(fullName); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("file exists and cannot be overwritten: %v", name)
		}
		return os.Rename(temp.Name(), fullName)
	}
	return &atomicWriteFile{temp, finalize}, nil
}

type atomicWriteFile struct {
	*os.File
	finalize func() error
}

func (wf *atomicWriteFile) ReadDir(n int) ([]fs.DirEntry, error) {
	unfiltered, err := wf.File.ReadDir(n)
	var result []fs.DirEntry
	for _, de := range unfiltered {
		if !strings.HasPrefix(de.Name(), ".") {
			result = append(result, de)
		}
	}
	return result, err
}

func (wf *atomicWriteFile) Close() error {
	if err := wf.File.Close(); err != nil {
		return err
	}
	if wf.finalize != nil {
		if err := wf.finalize(); err != nil {
			return err
		}
		wf.finalize = nil
	}
	return nil
}

func (dir dirFS) Sub(subDir string) (fs.FS, error) {
	return dirFS(path.Join(string(dir), subDir)), nil
}
