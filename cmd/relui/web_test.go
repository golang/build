// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFileServerHandler(t *testing.T) {
	h := fileServerHandler("./testing", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			path:     "/test.css",
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

	s := &server{store: &memoryStore{}}
	s.homeHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServerNewWorkflowHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workflows/new", nil)
	w := httptest.NewRecorder()

	s := &server{store: &memoryStore{}}
	s.newWorkflowHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServerCreateWorkflowHandler(t *testing.T) {
	cases := []struct {
		desc        string
		params      url.Values
		wantCode    int
		wantHeaders map[string]string
		wantParams  map[string]string
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
			wantParams: map[string]string{"GitObject": "abc"},
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/workflows/create", strings.NewReader(c.params.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			s := &server{store: &memoryStore{}}
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
			if len(s.store.GetWorkflows()) != 1 && c.wantParams != nil {
				t.Fatalf("len(s.store.GetWorkflows()) = %d, wanted %d", len(s.store.GetWorkflows()), 1)
			} else if len(s.store.GetWorkflows()) != 0 && c.wantParams == nil {
				t.Fatalf("len(s.store.GetWorkflows()) = %d, wanted %d", len(s.store.GetWorkflows()), 0)
			}
			if c.wantParams == nil {
				return
			}
			if diff := cmp.Diff(c.wantParams, s.store.GetWorkflows()[0].Params()); diff != "" {
				t.Errorf("s.Store.GetWorkflows()[0].Params() mismatch (-want, +got):\n%s", diff)
			}
		})
	}
}
