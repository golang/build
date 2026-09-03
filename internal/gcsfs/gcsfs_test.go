// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gcsfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

func TestGCSFS(t *testing.T) {
	if testing.Short() {
		t.Skip("reads a real GCS bucket over the internet")
	}

	client, err := storage.NewClient(t.Context(), option.WithScopes(storage.ScopeReadOnly), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	// Note: It may be somewhat preferable to have a dedicated GCS bucket for this test,
	// as that would make it viable to test NewFS without having to wrap it with fs.Sub.
	// In the meantime, settle on using a dedicated directory in an existing GCS bucket.
	fsys, err := fs.Sub(NewFS(t.Context(), client, "go-build-log"), "gcsfs-testdata")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"a",
		"b",
		"dir/x",
	}
	if err := fstest.TestFS(fsys, expected...); err != nil {
		t.Error(err)
	}

	sub, err := fs.Sub(fsys, "dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(sub, "x"); err != nil {
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
