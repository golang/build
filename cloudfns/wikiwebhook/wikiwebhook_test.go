// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package wikiwebhook

import (
	"bytes"
	"errors"
	"io/ioutil"
	"net/http/httptest"
	"testing"
)

func TestValidSignature(t *testing.T) {
	testCases := []struct {
		body, key []byte
		sig       string
		matches   bool
	}{
		{[]byte("body"), []byte("key"), "sha1=70bbf6819d1037aa94ca7e7f537cbea25fe49283", true},
		{[]byte("body"), []byte("key"), "sha1=70bbf6819d1037aa94ca7e7f537cbea25fe49284", false},
		{[]byte{}, []byte{}, "", false},
		{[]byte{}, []byte{}, "sha1=not a valid hex string", false},
	}
	for _, tc := range testCases {
		if matches := validSignature(tc.body, tc.key, tc.sig); matches != tc.matches {
			t.Errorf("expected match = %v; got match = %v\nbody: %q, key: %q, sig: %q", tc.matches, matches, tc.body, tc.key, tc.sig)
		}
	}
}

func TestWebHook(t *testing.T) {
	testCases := []struct {
		desc       string
		body       []byte
		headers    map[string]string
		publishFn  func(string, []byte) (string, error)
		statusCode int
		respBody   []byte
	}{
		{
			"invalid signature",
			nil,
			map[string]string{
				"X-Hub-Signature": "sha1=invalid",
			},
			nil,
			401,
			[]byte("signature mismatch\n"),
		},
		{
			"ping event",
			nil,
			map[string]string{
				"X-Hub-Signature": "sha1=fbdb1d1b18aa6c08324b7d64b71fb76370690e1d",
				"X-GitHub-Event":  "ping",
			},
			nil,
			200,
			[]byte("pong"),
		},
		{
			"wiki change event",
			[]byte("body"),
			map[string]string{
				"X-Hub-Signature": "sha1=cc5e6b2b046bc7401d071a3d9be9a1cf1869376d",
				"X-GitHub-Event":  "gollum",
			},
			func(topic string, body []byte) (string, error) {
				if got, want := body, []byte("body"); !bytes.Equal(got, want) {
					t.Errorf("unexpected body: got %q; expected %q", got, want)
				}
				return "42", nil
			},
			200,
			[]byte("Message ID: 42\n"),
		},
		{
			"error publishing topic",
			nil,
			map[string]string{
				"X-Hub-Signature": "sha1=fbdb1d1b18aa6c08324b7d64b71fb76370690e1d",
				"X-GitHub-Event":  "gollum",
			},
			func(topic string, body []byte) (string, error) {
				return "", errors.New("publishToTopic error")
			},
			500,
			[]byte("publishToTopic error\n"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			oldFn := publishToTopic
			defer func() { publishToTopic = oldFn }()
			publishToTopic = tc.publishFn

			req := httptest.NewRequest("GET", "http://cloudfunctionz.com/func", bytes.NewReader(tc.body))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			GitHubWikiChangeWebHook(w, req)

			resp := w.Result()
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Errorf("ioutil.ReadAll: %v", err)
			}
			if got, want := resp.StatusCode, tc.statusCode; got != want {
				t.Errorf("Unexpected status code: got %d; want %d", got, want)
			}
			if !bytes.Equal(body, tc.respBody) {
				t.Errorf("Unexpected body: got %q; want %q", body, tc.respBody)
			}
		})
	}
}
