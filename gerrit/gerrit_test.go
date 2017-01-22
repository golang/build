// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gerrit

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/context"
)

// taken from https://go-review.googlesource.com/projects/go
var exampleProjectResponse = []byte(`)]}'
{
  "id": "go",
  "name": "go",
  "parent": "All-Projects",
  "description": "The Go Programming Language",
  "state": "ACTIVE",
  "web_links": [
    {
      "name": "gitiles",
      "url": "https://go.googlesource.com/go/",
      "target": "_blank"
    }
  ]
}
`)

func TestGetProjectInfo(t *testing.T) {
	hitServer := false
	path := ""
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitServer = true
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(200)
		w.Write(exampleProjectResponse)
	}))
	defer s.Close()
	c := NewClient(s.URL, NoAuth)
	info, err := c.GetProjectInfo(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if !hitServer {
		t.Errorf("expected to hit test server, didn't")
	}
	if path != "/projects/go" {
		t.Errorf("expected Path to be '/projects/go', got %s", path)
	}
	if info.Name != "go" {
		t.Errorf("expected Name to be 'go', got %s", info.Name)
	}
}

func TestProjectNotFound(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		w.WriteHeader(404)
		w.Write([]byte("Not found: unknown"))
	}))
	defer s.Close()
	c := NewClient(s.URL, NoAuth)
	_, err := c.GetProjectInfo(context.Background(), "unknown")
	if err != ErrProjectNotExist {
		t.Errorf("expected to get ErrProjectNotExist, got %v", err)
	}
}

func TestContextError(t *testing.T) {
	c := NewClient("http://localhost", NoAuth)
	yearsAgo, _ := time.Parse("2006", "2006")
	ctx, cancel := context.WithDeadline(context.Background(), yearsAgo)
	defer cancel()
	_, err := c.GetProjectInfo(ctx, "unknown")
	if err == nil {
		t.Errorf("expected non-nil error, got nil")
	}
	uerr, ok := err.(*url.Error)
	if !ok {
		t.Errorf("expected url.Error, got %#v", err)
	}
	if uerr.Err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded error, got %v", uerr.Err)
	}
}
