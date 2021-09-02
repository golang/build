// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build go1.16
// +build go1.16

package relui

import (
	"embed"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
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

	s := &Server{}
	s.homeHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("resp.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServerNewWorkflowHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workflows/new", nil)
	w := httptest.NewRecorder()

	s := &Server{}
	s.newWorkflowHandler(w, req)
	resp := w.Result()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("rep.StatusCode = %d, wanted %d", resp.StatusCode, http.StatusOK)
	}
}
