// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.13
// +build linux darwin

// Code related to the Build Results API.

package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/build/cmd/coordinator/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

type gRPCServer struct {
	// embed an UnimplementedCoordinatorServer to avoid errors when adding new RPCs to the proto.
	*protos.UnimplementedCoordinatorServer

	// dashboardURL is the base URL of the Dashboard service (https://build.golang.org)
	dashboardURL string
}

// ClearResults implements the ClearResults RPC call from the CoordinatorService.
//
// It currently hits the build Dashboard service to clear a result.
// TODO(golang.org/issue/34744) - Change to wipe build status from the Coordinator itself after findWork
// starts using maintner.
func (g *gRPCServer) ClearResults(ctx context.Context, req *protos.ClearResultsRequest) (*protos.ClearResultsResponse, error) {
	key, err := keyFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetBuilder() == "" || req.GetHash() == "" {
		return nil, grpcstatus.Error(codes.InvalidArgument, "Builder and Hash must be provided")
	}
	if err := g.clearFromDashboard(ctx, req.GetBuilder(), req.GetHash(), key); err != nil {
		return nil, err
	}
	return &protos.ClearResultsResponse{}, nil
}

// clearFromDashboard calls the dashboard API to remove a build.
// TODO(golang.org/issue/34744) - Remove after switching to wiping in the Coordinator.
func (g *gRPCServer) clearFromDashboard(ctx context.Context, builder, hash, key string) error {
	u, err := url.Parse(g.dashboardURL)
	if err != nil {
		log.Printf("gRPCServer.ClearResults: Error parsing dashboardURL %q: %v", g.dashboardURL, err)
		return grpcstatus.Error(codes.Internal, codes.Internal.String())
	}
	u.Path = "/clear-results"
	form := url.Values{
		"builder": {builder},
		"hash":    {hash},
		"key":     {key},
	}
	u.RawQuery = form.Encode() // The Dashboard API does not read the POST body.
	clearReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		log.Printf("gRPCServer.ClearResults: error creating http request: %v", err)
		return grpcstatus.Error(codes.Internal, codes.Internal.String())
	}
	resp, err := http.DefaultClient.Do(clearReq)
	if err != nil {
		log.Printf("gRPCServer.ClearResults: error performing wipe for %q/%q: %v", builder, hash, err)
		return grpcstatus.Error(codes.Internal, codes.Internal.String())
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		log.Printf("gRPCServer.ClearResults: error reading response body for %q/%q: %v", builder, hash, err)
		return grpcstatus.Error(codes.Internal, codes.Internal.String())
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("gRPCServer.ClearResults: bad status from dashboard: %v (%q)", resp.StatusCode, resp.Status)
		code, ok := statusToCode[resp.StatusCode]
		if !ok {
			code = codes.Internal
		}
		return grpcstatus.Error(code, code.String())
	}
	if len(body) == 0 {
		return nil
	}
	dr := new(dashboardResponse)
	if err := json.Unmarshal(body, dr); err != nil {
		log.Printf("gRPCServer.ClearResults: error parsing response body for %q/%q: %v", builder, hash, err)
		return grpcstatus.Error(codes.Internal, codes.Internal.String())
	}
	if dr.Error == "datastore: concurrent transaction" {
		return grpcstatus.Error(codes.Aborted, dr.Error)
	}
	if dr.Error != "" {
		return grpcstatus.Error(codes.FailedPrecondition, dr.Error)
	}
	return nil
}

// dashboardResponse mimics the dashResponse struct from app/appengine.
// TODO(golang.org/issue/34744) - Remove after switching to wiping in the Coordinator.
type dashboardResponse struct {
	// Error is an error string describing the API response. The dashboard API semantics are to always return a
	// 200, and populate this field with details.
	Error string `json:"Error"`
	// Response a human friendly response from the API. It is not populated for build status clear responses.
	Response string `json:"Response"`
}

// statusToCode maps HTTP status codes to gRPC codes. It purposefully only contains statuses we care to map.
// TODO(golang.org/issue/34744) - Move to shared file or library.
var statusToCode = map[int]codes.Code{
	http.StatusOK:                  codes.OK,
	http.StatusBadRequest:          codes.InvalidArgument,
	http.StatusUnauthorized:        codes.Unauthenticated,
	http.StatusForbidden:           codes.PermissionDenied,
	http.StatusNotFound:            codes.NotFound,
	http.StatusConflict:            codes.Aborted,
	http.StatusGone:                codes.DataLoss,
	http.StatusTooManyRequests:     codes.ResourceExhausted,
	http.StatusInternalServerError: codes.Internal,
	http.StatusNotImplemented:      codes.Unimplemented,
	http.StatusServiceUnavailable:  codes.Unavailable,
	http.StatusGatewayTimeout:      codes.DeadlineExceeded,
}

// keyFromContext loads a builder key from request metadata.
//
// The metadata format is prefixed with "builder " to avoid collisions with OAuth:
//    authorization: builder MYKEY
//
// TODO(golang.org/issue/34744) - Move to shared file or library. This would make a nice UnaryServerInterceptor.
// TODO(golang.org/issue/34744) - Currently allows the Build Dashboard to validate tokens, but we should validate here.
func keyFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", grpcstatus.Error(codes.Internal, codes.Internal.String())
	}
	auth := md.Get("authorization")
	if len(auth) == 0 || len(auth[0]) < 9 || !strings.HasPrefix(auth[0], "builder ") {
		return "", grpcstatus.Error(codes.Unauthenticated, codes.Unauthenticated.String())
	}
	key := auth[0][8:len(auth[0])]
	return key, nil
}
