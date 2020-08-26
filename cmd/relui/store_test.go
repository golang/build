// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang/protobuf/proto"
	"github.com/google/go-cmp/cmp"
	reluipb "golang.org/x/build/cmd/relui/protos"
)

func TestFileStorePersist(t *testing.T) {
	dir, err := ioutil.TempDir("", "fileStore-test")
	if err != nil {
		t.Fatalf("ioutil.TempDir(%q, %q) = _, %v", "", "fileStore-test", err)
	}
	defer os.RemoveAll(dir)
	want := &reluipb.LocalStorage{
		Workflows: []*reluipb.Workflow{
			{
				Name:           "Persist Test",
				BuildableTasks: []*reluipb.BuildableTask{{Name: "Persist Test Task"}},
			},
		},
	}
	fs := newFileStore(filepath.Join(dir, "relui"))
	fs.ls = want

	err = fs.persist()
	if err != nil {
		t.Fatalf("fs.Persist() = %v, wanted no error", err)
	}

	b, err := ioutil.ReadFile(filepath.Join(dir, "relui", fileStoreName))
	if err != nil {
		t.Fatalf("ioutil.ReadFile(%q) = _, %v, wanted no error", filepath.Join(dir, "relui", fileStoreName), err)
	}
	got := new(reluipb.LocalStorage)
	err = proto.UnmarshalText(string(b), got)
	if err != nil {
		t.Fatalf("proto.UnmarshalText(_) = %v, wanted no error", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("reluipb.LocalStorage mismatch (-want, +got):\n%s", diff)
	}
}

func TestFileStoreLoad(t *testing.T) {
	dir, err := ioutil.TempDir("", "fileStore-test")
	if err != nil {
		t.Fatalf("ioutil.TempDir(%q, %q) = _, %v", "", "fileStore-test", err)
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, "relui"), 0755); err != nil {
		t.Errorf("os.MkDirAll(%q, %v) = %w", filepath.Join(dir, "relui"), 0755, err)
	}
	want := &reluipb.LocalStorage{
		Workflows: []*reluipb.Workflow{
			{
				Name:           "Load Test",
				BuildableTasks: []*reluipb.BuildableTask{{Name: "Load Test Task"}},
			},
		},
	}
	data := []byte(proto.MarshalTextString(want))
	dst := filepath.Join(dir, "relui", fileStoreName)
	if err := ioutil.WriteFile(dst, data, 0644); err != nil {
		t.Fatalf("ioutil.WriteFile(%q, _, %v) = %v", dst, 0644, err)
	}

	fs := newFileStore(filepath.Join(dir, "relui"))
	if err := fs.load(); err != nil {
		t.Errorf("reluipb.load() = %v, wanted no error", err)
	}

	if diff := cmp.Diff(want, fs.localStorage()); diff != "" {
		t.Errorf("reluipb.LocalStorage mismatch (-want, +got):\n%s", diff)
	}
}

func TestFileStoreLoadErrors(t *testing.T) {
	empty, err := ioutil.TempDir("", "fileStoreLoad")
	if err != nil {
		t.Fatalf("ioutil.TempDir(%q, %q) = %v, wanted no error", "", "fileStoreLoad", err)
	}
	defer os.RemoveAll(empty)

	collision, err := ioutil.TempDir("", "fileStoreLoad")
	if err != nil {
		t.Fatalf("ioutil.TempDir(%q, %q) = %v, wanted no error", "", "fileStoreLoad", err)
	}
	defer os.RemoveAll(collision)
	// We want to trigger an error when trying to read the file, so make a directory with the same name.
	if err := os.MkdirAll(filepath.Join(collision, fileStoreName), 0755); err != nil {
		t.Errorf("os.MkDirAll(%q, %v) = %w", filepath.Join(collision, fileStoreName), 0755, err)
	}

	corrupt, err := ioutil.TempDir("", "fileStoreLoad")
	if err != nil {
		t.Fatalf("ioutil.TempDir(%q, %q) = %v, wanted no error", "", "fileStoreLoad", err)
	}
	defer os.RemoveAll(corrupt)
	if err := ioutil.WriteFile(filepath.Join(corrupt, fileStoreName), []byte("oh no"), 0644); err != nil {
		t.Fatalf("ioutil.WriteFile(%q, %q, %v) = %v, wanted no error", filepath.Join(corrupt, fileStoreName), "oh no", 0644, err)
	}

	cases := []struct {
		desc    string
		dir     string
		wantErr bool
	}{
		{
			desc: "no persistDir configured",
		},
		{
			desc: "no file in persistDir",
			dir:  empty,
		},
		{
			desc:    "other error reading file",
			dir:     collision,
			wantErr: true,
		},
		{
			desc:    "corrupt data in persistDir",
			dir:     corrupt,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			f := newFileStore(c.dir)
			if err := f.load(); (err != nil) != c.wantErr {
				t.Errorf("f.load() = %v, wantErr = %t", err, c.wantErr)
			}
		})
	}
}
