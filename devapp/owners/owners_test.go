// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package owners

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMatch(t *testing.T) {
	testCases := []struct {
		path  string
		entry *Entry
	}{
		{
			"crypto/chacha20poly1305/chacha20poly1305.go",
			&Entry{
				Primary:   []Owner{filippo},
				Secondary: []Owner{agl},
			},
		},
		{
			"go/src/archive/zip/a.go",
			&Entry{
				Primary:   []Owner{joetsai},
				Secondary: []Owner{bradfitz},
			},
		},
		{
			"go/path/with/no/owners",
			&Entry{
				Primary: []Owner{rsc, iant, bradfitz},
			},
		},
		{
			"nonexistentrepo/foo/bar", nil,
		},
	}
	for _, tc := range testCases {
		matches := match(tc.path)
		if diff := cmp.Diff(matches, tc.entry); diff != "" {
			t.Errorf("%s: owners differ (-got +want)\n%s", tc.path, diff)
		}
	}
}

func TestHandler(t *testing.T) {
	testCases := []struct {
		method  string
		path    string
		code    int
		paths   []string
		entries map[string]*Entry
	}{
		{"PUT", "/owners/go/src/archive/zip/a.go", http.StatusMethodNotAllowed, nil, nil},
		{"GET", "/owners/go/src/archive/zip/a.go", http.StatusOK, nil,
			map[string]*Entry{"go/src/archive/zip/a.go": {Primary: []Owner{joetsai}, Secondary: []Owner{bradfitz}}},
		},
		{"GET", "/owners/nonexistent/path", http.StatusOK, nil,
			map[string]*Entry{"nonexistent/path": nil},
		},
		{
			"POST", "/owners/", http.StatusOK,
			[]string{"go/src/archive/zip/a.go"},
			map[string]*Entry{"go/src/archive/zip/a.go": {Primary: []Owner{joetsai}, Secondary: []Owner{bradfitz}}},
		},
		{
			"POST", "/owners/", http.StatusOK,
			[]string{
				"go/src/archive/zip/a.go",
				"go/src/archive/zip/b.go",
			},
			map[string]*Entry{
				"go/src/archive/zip/a.go": {Primary: []Owner{joetsai}, Secondary: []Owner{bradfitz}},
				"go/src/archive/zip/b.go": {Primary: []Owner{joetsai}, Secondary: []Owner{bradfitz}},
			},
		},
		{
			"POST", "/owners/", http.StatusOK,
			[]string{
				"go/src/archive/zip/a.go",
				"crypto/chacha20poly1305/chacha20poly1305.go",
			},
			map[string]*Entry{
				"go/src/archive/zip/a.go":                     {Primary: []Owner{joetsai}, Secondary: []Owner{bradfitz}},
				"crypto/chacha20poly1305/chacha20poly1305.go": {Primary: []Owner{filippo}, Secondary: []Owner{agl}},
			},
		},
	}

	for _, tc := range testCases {
		var buf bytes.Buffer
		if tc.paths != nil {
			var oReq Request
			oReq.Payload.Paths = tc.paths
			if err := json.NewEncoder(&buf).Encode(oReq); err != nil {
				t.Errorf("could not encode request: %v", err)
				continue
			}
		}
		rStr := buf.String()
		if rStr == "" {
			rStr = "<empty>"
		}
		t.Logf("Request: %v", rStr)
		req, err := http.NewRequest(tc.method, tc.path, &buf)
		if err != nil {
			t.Errorf("http.NewRequest: %v", err)
			continue
		}
		w := httptest.NewRecorder()
		Handler(w, req)
		resp := w.Result()
		if got, want := resp.StatusCode, tc.code; got != want {
			t.Errorf("status code: got %v; want %v", got, want)
		}

		if tc.code != http.StatusOK {
			continue
		}
		var oResp Response
		if err := json.NewDecoder(resp.Body).Decode(&oResp); err != nil {
			t.Errorf("json decode: %v", err)
		}

		if diff := cmp.Diff(oResp.Payload.Entries, tc.entries); diff != "" {
			t.Errorf("%s %s: (-got +want)\n%s", tc.method, tc.path, diff)
		}
	}
}
