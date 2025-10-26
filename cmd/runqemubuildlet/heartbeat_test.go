// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestCheckBuildletHealth(t *testing.T) {
	cases := []struct {
		desc     string
		respCode int
		wantErr  bool
	}{
		{
			desc:     "success",
			respCode: http.StatusOK,
		},
		{
			desc:     "failure",
			respCode: http.StatusBadGateway,
			wantErr:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			m := http.NewServeMux()
			m.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(c.respCode)
				fmt.Fprintln(w, "ok")
			})
			s := httptest.NewServer(m)
			defer s.Close()
			u, err := url.Parse(s.URL)
			if err != nil {
				t.Fatalf("url.Parse(%q) = %v, wanted no error", s.URL, err)
			}
			u.Path = "/healthz"

			if err := checkBuildletHealth(context.Background(), u.String()); (err != nil) != c.wantErr {
				t.Errorf("checkBuildletHealth(_, %q) = %v, wantErr: %t", s.URL, err, c.wantErr)
			}
		})
	}
}

func TestHeartbeatContext(t *testing.T) {
	ctx := context.Background()

	didWork := make(chan interface{}, 2)
	done := make(chan interface{})
	ctx, cancel := heartbeatContext(ctx, time.Millisecond, 100*time.Millisecond, func(context.Context) error {
		select {
		case <-done:
			return errors.New("heartbeat stopped")
		case didWork <- nil:
		default:
		}
		return nil
	})
	defer cancel()

	select {
	case <-time.After(5 * time.Second):
		t.Errorf("heatbeatContext() never called f, wanted at least one call")
	case <-didWork:
	}

	select {
	case <-done:
		t.Errorf("heartbeatContext() finished early, wanted it to still be testing")
	case <-didWork:
		close(done)
	}

	select {
	case <-time.After(5 * time.Second):
		t.Errorf("heartbeatContext() did not timeout, wanted timeout after failing over %v", time.Second)
	case <-ctx.Done():
		// heartbeatContext() successfully timed out after failing
	}
}
