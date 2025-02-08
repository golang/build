// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gcsfs

import (
	"context"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

var slowTest = flag.Bool("slow", false, "run slow tests that access GCS")

func TestGCSFS(t *testing.T) {
	if !*slowTest {
		t.Skip("reads a largeish GCS bucket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := storage.NewClient(context.Background(), option.WithScopes(storage.ScopeReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	fsys := NewFS(ctx, client, "vcs-test")
	expected := []string{
		"auth/or401.zip",
		"bzr/hello.zip",
	}
	if err := fstest.TestFS(fsys, expected...); err != nil {
		t.Error(err)
	}

	sub, err := fs.Sub(fsys, "auth")
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(sub, "or401.zip"); err != nil {
		t.Error(err)
	}
}

func TestDirFS(t *testing.T) {
	if err := fstest.TestFS(DirFS("./testdata/dirfs"), "a", "b", "dir/x"); err != nil {
		t.Fatal(err)
	}
}

func TestDirFSDotFiles(t *testing.T) {
	temp := t.TempDir()
	if err := os.WriteFile(temp+"/.foo", nil, 0777); err != nil {
		t.Fatal(err)
	}
	files, err := fs.ReadDir(DirFS(temp), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("ReadDir didn't hide . files: %v", files)
	}
}

func TestDirFSWrite(t *testing.T) {
	temp := t.TempDir()
	fsys := DirFS(temp)
	f, err := Create(fsys, "fsystest.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hey\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(temp, "fsystest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hey\n" {
		t.Fatalf("unexpected file contents %q, want %q", string(b), "hey\n")
	}
}
