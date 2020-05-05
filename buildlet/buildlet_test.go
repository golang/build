// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestBuildletClient(t *testing.T) {
	var httpCalled, OnBeginBuildletProbeCalled, OnEndBuildletProbeCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		fmt.Fprintln(w, "buildlet endpoint reached")
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("unable to parse http server url %s", err)
	}

	kp, err := NewKeyPair()
	if err != nil {
		t.Fatalf("unable to create key pair %s", err)
	}

	opt := &VMOpts{
		TLS:                  kp,
		OnBeginBuildletProbe: func(string) { OnBeginBuildletProbeCalled = true },
		OnEndBuildletProbe:   func(*http.Response, error) { OnEndBuildletProbeCalled = true },
	}

	gotClient, gotErr := buildletClient(context.Background(), ts.URL, u.Host, opt)
	if gotErr != nil {
		t.Errorf("buildletClient(ctx, %s, %s, %v) error %s", ts.URL, u.Host, opt, gotErr)
	}
	if gotClient == nil {
		t.Errorf("client should not be nil")
	}
	if !httpCalled {
		t.Error("http endpoint never called")
	}
	if !OnBeginBuildletProbeCalled {
		t.Error("OnBeginBuildletProbe() was not called")
	}
	if !OnEndBuildletProbeCalled {
		t.Error("OnEndBuildletProbe() was not called")
	}
}

func TestBuildletClientError(t *testing.T) {
	var httpCalled, OnBeginBuildletProbeCalled, OnEndBuildletProbeCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		fmt.Fprintln(w, "buildlet endpoint reached")
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("unable to parse http server url %s", err)
	}

	kp, err := NewKeyPair()
	if err != nil {
		t.Fatalf("unable to create key pair %s", err)
	}

	opt := &VMOpts{
		TLS:                  kp,
		OnBeginBuildletProbe: func(string) { OnBeginBuildletProbeCalled = true },
		OnEndBuildletProbe:   func(*http.Response, error) { OnEndBuildletProbeCalled = true },
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gotClient, gotErr := buildletClient(ctx, ts.URL, u.Host, opt)
	if gotErr == nil {
		t.Errorf("buildletClient(ctx, %s, %s, %v) error %s", ts.URL, u.Host, opt, gotErr)
	}
	if gotClient != nil {
		t.Errorf("client should be nil")
	}
	if httpCalled {
		t.Error("http endpoint called")
	}
	if OnBeginBuildletProbeCalled {
		t.Error("OnBeginBuildletProbe() was called")
	}
	if OnEndBuildletProbeCalled {
		t.Error("OnEndBuildletProbe() was called")
	}
}

func TestProbeBuildlet(t *testing.T) {
	var httpCalled, OnBeginBuildletProbeCalled, OnEndBuildletProbeCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		fmt.Fprintln(w, "buildlet endpoint reached")
	}))
	defer ts.Close()
	opt := &VMOpts{
		OnBeginBuildletProbe: func(string) { OnBeginBuildletProbeCalled = true },
		OnEndBuildletProbe:   func(*http.Response, error) { OnEndBuildletProbeCalled = true },
	}
	gotErr := probeBuildlet(context.Background(), ts.URL, opt)
	if gotErr != nil {
		t.Errorf("probeBuildlet(ctx, %q, %+v) = %s; want no error", ts.URL, opt, gotErr)
	}
	if !httpCalled {
		t.Error("http endpoint never called")
	}
	if !OnBeginBuildletProbeCalled {
		t.Error("OnBeginBuildletProbe() was not called")
	}
	if !OnEndBuildletProbeCalled {
		t.Error("OnEndBuildletProbe() was not called")
	}
}

func TestProbeBuildletError(t *testing.T) {
	var httpCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		http.Error(w, "all types of broken", http.StatusInternalServerError)
	}))
	defer ts.Close()
	opt := &VMOpts{}
	gotErr := probeBuildlet(context.Background(), ts.URL, opt)
	if gotErr == nil {
		t.Errorf("probeBuildlet(ctx, %q, %+v) = nil; want error", ts.URL, opt)
	}
	if !httpCalled {
		t.Error("http endpoint never called")
	}
}
