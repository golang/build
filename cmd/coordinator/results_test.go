// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/build/cmd/coordinator/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

// fakeDashboard implements a fake version of the Build Dashboard API for testing.
// TODO(golang.org/issue/34744) - Remove with build dashboard API client removal.
type fakeDashboard struct {
	returnBody   string
	returnStatus int
}

func (f *fakeDashboard) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(rw, "method must be POST", http.StatusBadRequest)
		return
	}
	if r.URL.Path != "/clear-results" {
		http.NotFound(rw, r)
		return
	}
	if f.returnStatus != 0 && f.returnStatus != http.StatusOK {
		http.Error(rw, `{"Error": "`+http.StatusText(f.returnStatus)+`"}`, f.returnStatus)
		return
	}
	r.ParseForm()
	if r.FormValue("builder") == "" || r.FormValue("hash") == "" || r.FormValue("key") == "" {
		http.Error(rw, `{"Error": "missing builder, hash, or key"}`, http.StatusBadRequest)
		return
	}
	if f.returnBody == "" {
		rw.Write([]byte("{}"))
		return
	}
	rw.Write([]byte(f.returnBody))
	return
}

func TestClearResults(t *testing.T) {
	req := &protos.ClearResultsRequest{Builder: "somebuilder", Hash: "somehash"}
	fd := new(fakeDashboard)
	s := httptest.NewServer(fd)
	defer s.Close()

	md := metadata.New(map[string]string{"authorization": "builder mykey"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	gs := &gRPCServer{dashboardURL: s.URL}
	_, err := gs.ClearResults(ctx, req)
	if err != nil {
		t.Errorf("cli.ClearResults(%v, %v) = _, %v, wanted no error", ctx, req, err)
	}

	if grpcstatus.Code(err) != codes.OK {
		t.Errorf("cli.ClearResults(%v, %v) = _, %v, wanted %v", ctx, req, err, codes.OK)
	}
}

func TestClearResultsErrors(t *testing.T) {
	cases := []struct {
		desc     string
		key      string
		req      *protos.ClearResultsRequest
		apiCode  int
		apiResp  string
		wantCode codes.Code
	}{
		{
			desc: "missing key",
			req: &protos.ClearResultsRequest{
				Builder: "local",
				Hash:    "ABCDEF1234567890",
			},
			wantCode: codes.Unauthenticated,
		},
		{
			desc: "missing builder",
			key:  "somekey",
			req: &protos.ClearResultsRequest{
				Hash: "ABCDEF1234567890",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			desc: "missing hash",
			key:  "somekey",
			req: &protos.ClearResultsRequest{
				Builder: "local",
			},
			wantCode: codes.InvalidArgument,
		},
		{
			desc: "dashboard API error",
			key:  "somekey",
			req: &protos.ClearResultsRequest{
				Builder: "local",
				Hash:    "ABCDEF1234567890",
			},
			apiCode:  http.StatusBadRequest,
			wantCode: codes.InvalidArgument,
		},
		{
			desc: "dashboard API unknown status",
			key:  "somekey",
			req: &protos.ClearResultsRequest{
				Builder: "local",
				Hash:    "ABCDEF1234567890",
			},
			apiCode:  http.StatusPermanentRedirect,
			wantCode: codes.Internal,
		},
		{
			desc: "dashboard API retryable error",
			key:  "somekey",
			req: &protos.ClearResultsRequest{
				Builder: "local",
				Hash:    "ABCDEF1234567890",
			},
			apiCode:  http.StatusOK,
			apiResp:  `{"Error": "datastore: concurrent transaction"}`,
			wantCode: codes.Aborted,
		},
		{
			desc: "dashboard API other error",
			key:  "somekey",
			req: &protos.ClearResultsRequest{
				Builder: "local",
				Hash:    "ABCDEF1234567890",
			},
			apiCode:  http.StatusOK,
			apiResp:  `{"Error": "no matching builder found"}`,
			wantCode: codes.FailedPrecondition,
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			fd := &fakeDashboard{returnStatus: c.apiCode, returnBody: c.apiResp}
			s := httptest.NewServer(fd)
			defer s.Close()

			md := metadata.New(map[string]string{"authorization": "builder " + c.key})
			ctx := metadata.NewIncomingContext(context.Background(), md)
			gs := &gRPCServer{dashboardURL: s.URL}
			_, err := gs.ClearResults(ctx, c.req)

			if grpcstatus.Code(err) != c.wantCode {
				t.Errorf("cli.ClearResults(%v, %v) = _, %v, wanted %v", ctx, c.req, err, c.wantCode)
			}
		})
	}
}
