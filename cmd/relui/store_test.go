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
	dir, err := ioutil.TempDir("", "memory-store-test")
	if err != nil {
		t.Fatalf("ioutil.TempDir(%q, %q) = _, %v", "", "memory-store-test", err)
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
