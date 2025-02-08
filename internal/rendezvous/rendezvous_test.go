// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rendezvous

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/build/revdial/v2"
)

func TestNew(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = New(ctx)
}

func TestPurgeExpiredRegistrations(t *testing.T) {
	rdv := &Rendezvous{
		m: make(map[string]*entry),
	}
	rdv.m["test"] = &entry{
		deadline: time.Unix(0, 0),
		ch:       make(chan *result, 1),
	}
	rdv.purgeExpiredRegistrations()
	if len(rdv.m) != 0 {
		t.Errorf("purgeExpiredRegistrations() did not purge expired entries: want 0 got %d", len(rdv.m))
	}
}

func TestRegisterInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rdv := New(ctx)
	rdv.RegisterInstance(ctx, "sample-1", time.Minute)
	if len(rdv.m) != 1 {
		t.Errorf("RegisterInstance: want 1, got %d", len(rdv.m))
	}
}

func TestWaitForInstanceError(t *testing.T) {
	testCases := []struct {
		desc           string
		headers        map[string]string
		wantStatusCode int
	}{
		{desc: "missing host header", headers: map[string]string{HeaderID: "test-id", HeaderToken: "test-token"}, wantStatusCode: 400},
		{desc: "missing id header", headers: map[string]string{HeaderToken: "test-token", HeaderHostname: "test-hostname"}, wantStatusCode: 400},
		{desc: "missing auth token", headers: map[string]string{HeaderID: "test-id", HeaderHostname: "test-hostname"}, wantStatusCode: 400},
		{desc: "missing registration", headers: map[string]string{HeaderID: "test-id", HeaderToken: "test-token", HeaderHostname: "test-hostname"}, wantStatusCode: 412},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			rdv := &Rendezvous{
				m: make(map[string]*entry),
				validator: func(ctx context.Context, jwt string) bool {
					return true
				},
			}
			ts := httptest.NewTLSServer(http.HandlerFunc(rdv.HandleReverse))
			defer ts.Close()
			client := ts.Client()
			req, err := http.NewRequest("GET", ts.URL, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("client.Get(%s): %s", ts.URL, err)
			}
			if resp.StatusCode != tc.wantStatusCode {
				t.Fatalf("resp.StatusCode: got %d, want %d", resp.StatusCode, tc.wantStatusCode)
			}
		})
	}
}

func TestWaitForInstaceErrorNonTLS(t *testing.T) {
	rdv := &Rendezvous{
		m: make(map[string]*entry),
		validator: func(ctx context.Context, jwt string) bool {
			return true
		},
	}
	ts := httptest.NewServer(http.HandlerFunc(rdv.HandleReverse))
	defer ts.Close()
	client := ts.Client()
	req, err := http.NewRequest("GET", ts.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("client.Get(%s): %s", ts.URL, err)
	}
	if resp.StatusCode != 500 {
		t.Fatalf("resp.StatusCode: got %d, want %d", resp.StatusCode, 500)
	}
}

func TestWaitForInstaceRevdialError(t *testing.T) {
	rdv := &Rendezvous{
		m: make(map[string]*entry),
		validator: func(ctx context.Context, jwt string) bool {
			return true
		},
	}
	instanceID := "test-id-3"
	ctx := context.Background()
	rdv.RegisterInstance(ctx, instanceID, 15*time.Second)
	mux := http.NewServeMux()
	mux.HandleFunc("/reverse", rdv.HandleReverse)
	mux.Handle("/revdial", revdial.ConnHandler())
	ts := httptest.NewTLSServer(mux)
	defer ts.Close()
	client := ts.Client()
	req, err := http.NewRequest("GET", ts.URL+"/reverse", nil)
	req.Header.Set(HeaderID, instanceID)
	req.Header.Set(HeaderToken, "test-token")
	req.Header.Set(HeaderHostname, "test-hostname")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		_, _ = client.Do(req)
	}()
	_, err = rdv.WaitForInstance(ctx, instanceID)
	if err == nil {
		// expect a missing status endpoint
		t.Fatal("WaitForInstance(): got nil, want error")
	}
	wg.Wait()
}

func TestDeregisterInstance(t *testing.T) {
	rdv := &Rendezvous{
		m: make(map[string]*entry),
	}
	id := "test-xyz"
	rdv.m[id] = &entry{}
	rdv.DeregisterInstance(context.Background(), id)
	if len(rdv.m) != 0 {
		t.Errorf("/deregusterInstance() did not remove the entry: want 0 got %d", len(rdv.m))
	}
}
