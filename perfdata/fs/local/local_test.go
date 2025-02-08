// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/build/internal/diff"
)

func TestNewWriter(t *testing.T) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "local_test")
	if err != nil {
		t.Fatalf("TempDir = %v", err)
	}
	defer os.RemoveAll(dir)

	fs := NewFS(dir)

	w, err := fs.NewWriter(ctx, "dir/file", map[string]string{"key": "value", "key2": "value2"})
	if err != nil {
		t.Fatalf("NewWriter = %v", err)
	}

	want := "hello world"

	if _, err := w.Write([]byte(want)); err != nil {
		t.Fatalf("Write = %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	have, err := os.ReadFile(filepath.Join(dir, "dir/file"))
	if err != nil {
		t.Fatalf("ReadFile = %v", err)
	}
	if d := diff.Diff("have", have, "want", []byte(want)); d != nil {
		t.Errorf("file contents differ:\n%s", d)
	}
}
