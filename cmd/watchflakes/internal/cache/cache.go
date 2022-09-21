// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cache implements a simple file-based cache.
package cache

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// A Cache is a directory holding cached data.
type Cache struct {
	dir      string
	disabled bool
}

// Disabled returns a Cache that is always empty.
// Reads always return no result, and writes succeed but are discarded.
func Disabled() *Cache {
	return &Cache{disabled: true}
}

// Create returns a cache using the named subdirectory
// of the user's cache directory.
// For example, on macOS, Create("myprog") uses $HOME/Library/Caches/myprog.
// Create creates the directory if it does not already exist.
func Create(name string) (*Cache, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return nil, err
	}
	return &Cache{dir: dir}, nil
}

// Read reads the file with the given name in the cache.
// It returns the file content, its last modification time,
// and any error encountered.
// If the file does not exist in the cache, Read returns nil, time.Time{}, nil.
func (c *Cache) Read(name string) ([]byte, time.Time, error) {
	if c.disabled {
		return nil, time.Time{}, nil
	}
	if c.dir == "" {
		return nil, time.Time{}, fmt.Errorf("use of zero Cache")
	}
	f, err := os.Open(filepath.Join(c.dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return nil, time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, err
	}
	return data, info.ModTime(), nil
}

// Write writes data to the file with the given name in the cache.
func (c *Cache) Write(name string, data []byte) error {
	if c.disabled {
		return nil
	}
	if c.dir == "" {
		return fmt.Errorf("use of zero Cache")
	}
	return os.WriteFile(filepath.Join(c.dir, name), data, 0666)
}
