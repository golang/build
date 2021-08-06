// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package main

import (
	"embed"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	reluipb "golang.org/x/build/cmd/relui/protos"
	"golang.org/x/build/internal/datastore/fake"
)

// testStatic is our static web server content.
//go:embed testing
var testStatic embed.FS

func TestFileServerHandler(t *testing.T) {
	h := fileServerHandler(testStatic, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Home"))
	}))

	cases := []struct {
		desc        string
		path        string
		wantCode    int
		wantBody    string
		wantHeaders map[string]string
	}{
		{
			desc:     "fallback to next handler",
			path:     "/",
			wantCode: http.StatusOK,
			wantBody: "Home",
		},
		{
			desc:     "sets headers and returns file",
			path:     "/testing/test.css",
			wantCode: http.StatusOK,
			wantBody: ".Header { font-size: 10rem; }\n",
			wantHeaders: map[string]string{
				"Content-Type":  "text/css; charset=utf-8",
				"Cache-Control": "no-cache, private, max-age=0",
			},
		},
		{
			desc:     "handles missing file",
			path:     "/foo.js",
			wantCode: http.StatusNotFound,
			wantBody: "404 page not found\n",
			wantHeaders: map[string]string{
				"Content-Type": "text/plain; charset=utf-8",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != c.wantCode {
				t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, c.wantCode)
			}
			b, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Errorf("resp.Body = _, %v, wanted no error", err)
			}
			if string(b) != c.wantBody {
				t.Errorf("resp.Body = %q, %v, wanted %q, %v", b, err, c.wantBody, nil)
			}
			for k, v := range c.wantHeaders {
				if resp.Header.Get(k) != v {
					t.Errorf("resp.Header.Get(%q) = %q, wanted %q", k, resp.Header.Get(k), v)
				}
			}
		})
	}
}

func TestServerHomeHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	s := &server{store: &dsStore{client: &fake.Client{}}}
	s.homeHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServerNewWorkflowHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workflows/new", nil)
	w := httptest.NewRecorder()

	s := &server{store: &dsStore{client: &fake.Client{}}}
	s.newWorkflowHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServerCreateWorkflowHandler(t *testing.T) {
	config := []*reluipb.Workflow{
		{
			Name:           "test_workflow",
			BuildableTasks: []*reluipb.BuildableTask{{Name: "test_task"}},
		},
	}
	cases := []struct {
		desc        string
		params      url.Values
		wantCode    int
		wantHeaders map[string]string
	}{
		{
			desc:     "bad request",
			wantCode: http.StatusBadRequest,
		},
		{
			desc:     "successful creation",
			params:   url.Values{"workflow.revision": []string{"abc"}},
			wantCode: http.StatusSeeOther,
			wantHeaders: map[string]string{
				"Location": "/",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/workflows/create", strings.NewReader(c.params.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			s := &server{store: &dsStore{client: &fake.Client{}}, configs: config}
			s.createWorkflowHandler(w, req)
			resp := w.Result()

			if resp.StatusCode != c.wantCode {
				t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, c.wantCode)
			}
			for k, v := range c.wantHeaders {
				if resp.Header.Get(k) != v {
					t.Errorf("resp.Header.Get(%q) = %q, wanted %q", k, resp.Header.Get(k), v)
				}
			}
			if c.wantCode == http.StatusBadRequest {
				return
			}
			wfs := s.store.Workflows()
			if len(wfs) != 1 {
				t.Fatalf("len(wfs) = %d, wanted %d", len(wfs), 1)
			}
			if wfs[0].GetId() == "" {
				t.Errorf("s.Store.Workflows[0].GetId() = %q, wanted not empty", wfs[0].GetId())
			}
			if wfs[0].GetBuildableTasks()[0].GetId() == "" {
				t.Errorf("s.Store.Workflows[0].GetBuildableTasks()[0].GetId() = %q, wanted not empty", wfs[0].GetId())
			}
		})
	}
}

func TestServerStartTaskHandler(t *testing.T) {
	s := server{store: &dsStore{client: &fake.Client{}}}
	wf := &reluipb.Workflow{
		Id:   "someworkflow",
		Name: "test_workflow",
		BuildableTasks: []*reluipb.BuildableTask{{
			Name:     "test_task",
			TaskType: "TestTask",
			Id:       "sometask",
		}},
	}
	if err := s.store.AddWorkflow(wf); err != nil {
		t.Fatalf("store.AddWorkflow(%v) = %v, wanted no error", wf, err)
	}
	params := url.Values{"workflow.id": []string{"someworkflow"}, "task.id": []string{"sometask"}}
	req := httptest.NewRequest(http.MethodPost, "/tasks/start", strings.NewReader(params.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	w := httptest.NewRecorder()
	s.startTaskHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusSeeOther)
	}
	if resp.Header.Get("Location") != "/" {
		t.Errorf("resp.Header.Get(%q) = %q, wanted %q", "Location", resp.Header.Get("Location"), "/")
	}
}

func TestStartTaskHandlerErrors(t *testing.T) {
	wf := &reluipb.Workflow{
		Id:   "someworkflow",
		Name: "test_workflow",
		BuildableTasks: []*reluipb.BuildableTask{{
			Name:     "test_task",
			TaskType: "TestTask",
			Id:       "sometask",
		}},
	}

	cases := []struct {
		desc     string
		params   url.Values
		wantCode int
	}{
		{
			desc:     "task not found",
			params:   url.Values{"workflow.id": []string{"someworkflow"}, "task.id": []string{"notexist"}},
			wantCode: http.StatusNotFound,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			s := server{store: &dsStore{client: &fake.Client{}}}
			if err := s.store.AddWorkflow(wf); err != nil {
				t.Fatalf("store.AddWorkflow(%v) = %v, wanted no error", wf, err)
			}
			req := httptest.NewRequest(http.MethodPost, "/tasks/start", strings.NewReader(c.params.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			w := httptest.NewRecorder()
			s.startTaskHandler(w, req)
			resp := w.Result()

			if resp.StatusCode != c.wantCode {
				t.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, c.wantCode)
			}
		})
	}
}
