// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package issueswebhook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/ioutil"
	"net/http/httptest"
	"testing"
)

func TestWebHook(t *testing.T) {
	testCases := []struct {
		desc              string
		body              []byte
		headers           map[string]string
		newObjectWriterFn func(context.Context, string) (io.WriteCloser, error)
		statusCode        int
		respBody          []byte // only checked on 2xx
	}{
		{
			desc: "ping event",
			headers: map[string]string{
				"X-GitHub-Event": "ping",
			},
			statusCode: 200,
			respBody:   []byte("pong"),
		},
		{
			desc: "issues event",
			body: []byte("body"),
			headers: map[string]string{
				"X-GitHub-Delivery": "42",
			},
			newObjectWriterFn: func(ctx context.Context, id string) (io.WriteCloser, error) {
				return &testWriteCloser{}, nil
			},
			statusCode: 200,
			respBody:   []byte("Message ID: 42\n"),
		},
		{
			desc: "error writing to GCS",
			headers: map[string]string{
				"X-GitHub-Delivery": "42",
			},
			newObjectWriterFn: func(ctx context.Context, id string) (io.WriteCloser, error) {
				return &testWriteCloser{writeErr: errors.New("test error on Write")}, nil
			},
			statusCode: 500,
		},
		{
			desc: "error closing GCS writer",
			headers: map[string]string{
				"X-GitHub-Delivery": "42",
			},
			newObjectWriterFn: func(ctx context.Context, id string) (io.WriteCloser, error) {
				return &testWriteCloser{closeErr: errors.New("test error on Close")}, nil
			},
			statusCode: 500,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			oldFn := newObjectWriter
			defer func() { newObjectWriter = oldFn }()
			newObjectWriter = tc.newObjectWriterFn

			req := httptest.NewRequest("GET", "http://cloudfunctionz.com/func", bytes.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			GitHubIssueChangeWebHook(w, req)

			resp := w.Result()
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Errorf("ioutil.ReadAll: %v", err)
			}
			if got, want := resp.StatusCode, tc.statusCode; got != want {
				t.Errorf("Unexpected status code: got %d; want %d", got, want)
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 && !bytes.Equal(body, tc.respBody) {
				t.Errorf("Unexpected body: got %q; want %q", body, tc.respBody)
			}
		})
	}
}

type testWriteCloser struct {
	writeErr error
	closeErr error
}

func (wc *testWriteCloser) Write(b []byte) (int, error) {
	return len(b), wc.writeErr
}

func (wc *testWriteCloser) Close() error {
	return wc.closeErr
}
