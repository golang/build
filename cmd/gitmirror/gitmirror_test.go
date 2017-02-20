// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestHomepage(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handleRoot(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /: want code 200, got %d", w.Code)
	}
	if hdr := w.Header().Get("Content-Type"); !strings.Contains(hdr, "text/html") {
		t.Fatalf("GET /: want html content-type, got %s", hdr)
	}
}

func TestDebugWatcher(t *testing.T) {
	r := &Repo{path: "build"}
	r.setStatus("waiting")
	req := httptest.NewRequest("GET", "/debug/watcher/build", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /: want code 200, got %d", w.Code)
	}
	body := w.Body.String()
	if substr := `watcher status for repo: "build"`; !strings.Contains(body, substr) {
		t.Fatalf("GET /debug/watcher/build: want %q in body, got %s", substr, body)
	}
	if substr := "waiting"; !strings.Contains(body, substr) {
		t.Fatalf("GET /debug/watcher/build: want %q in body, got %s", substr, body)
	}
}

// fakeCmd records the results of CommandContext and echoes any arguments to
// stdout.
type fakeCmd struct {
	Cmd       string
	Args      []string
	callCount int
}

func (f *fakeCmd) CommandContext(ctx context.Context, cmd string, args ...string) *exec.Cmd {
	f.callCount++
	f.Cmd = cmd
	f.Args = args
	return exec.CommandContext(ctx, "echo", append([]string{cmd}, args...)...)
}

func TestRev(t *testing.T) {
	f := &fakeCmd{}
	testHookArchiveCmd = f.CommandContext
	defer func() { testHookArchiveCmd = nil }()
	r := &Repo{path: "build"}
	r.setStatus("waiting")
	req := httptest.NewRequest("GET", "/build.tar.gz?rev=example-branch", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /: want code 200, got %d", w.Code)
	}
	if f.Cmd != "git" {
		t.Fatalf("cmd: want 'git' for cmd, got %s", f.Cmd)
	}
	wantArgs := []string{"archive", "--format=tgz", "example-branch"}
	if !reflect.DeepEqual(f.Args, wantArgs) {
		t.Fatalf("cmd: want '%q' for args, got %q", wantArgs, f.Args)
	}
}

func TestRevNotFound(t *testing.T) {
	f := &fakeCmd{}
	f2 := &fakeCmd{}
	testHookArchiveCmd = f.CommandContext
	testHookFetchCmd = f2.CommandContext
	defer func() {
		testHookArchiveCmd = nil
		testHookFetchCmd = nil
	}()
	r := &Repo{path: "build"}
	r.setStatus("waiting")
	req := httptest.NewRequest("GET", "/build.tar.gz?rev=example-branch", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /build.tar.gz: want code 200, got %d", w.Code)
	}
	if f2.callCount != 1 {
		t.Fatal("GET /build.tar.gz: want 'git fetch' to be called, wasn't called")
	}
	wantArgs := []string{"fetch", "origin", "example-branch"}
	if !reflect.DeepEqual(f2.Args, wantArgs) {
		t.Fatalf("cmd: want '%q' for args, got %q", wantArgs, f2.Args)
	}
}
