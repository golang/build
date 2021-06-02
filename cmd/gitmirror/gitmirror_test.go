// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// The tests need a dummy directory that exists with a
	// basename of "build", but the tests never write to it. So we
	// can create one and share it for all tests.
	tempDir, err := ioutil.TempDir("", "")
	if err != nil {
		log.Fatal(err)
	}
	tempRepoRoot = filepath.Join(tempDir, "build")
	if err := os.Mkdir(tempRepoRoot, 0700); err != nil {
		log.Fatal(err)
	}

	e := m.Run()
	os.RemoveAll(tempDir)
	os.Exit(e)
}

var tempRepoRoot string

func newTestRepo() *repo {
	return &repo{
		name: "build",
		root: tempRepoRoot,
	}
}

func TestHomepage(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	(&mirror{}).handleRoot(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /: want code 200, got %d", w.Code)
	}
	if hdr := w.Header().Get("Content-Type"); !strings.Contains(hdr, "text/html") {
		t.Fatalf("GET /: want html content-type, got %s", hdr)
	}
}

func TestDebugWatcher(t *testing.T) {
	r := newTestRepo()
	r.setStatus("waiting")
	req := httptest.NewRequest("GET", "/debug/watcher/build", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET / = code %d, want 200", w.Code)
	}
	body := w.Body.String()
	if substr := `watcher status for repo: "build"`; !strings.Contains(body, substr) {
		t.Fatalf("GET /debug/watcher/build: want %q in body, got %s", substr, body)
	}
	if substr := "waiting"; !strings.Contains(body, substr) {
		t.Fatalf("GET /debug/watcher/build: want %q in body, got %s", substr, body)
	}
}

func mustHaveGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("skipping; git not in PATH")
	}
}
