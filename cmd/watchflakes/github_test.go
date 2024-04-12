// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"rsc.io/github"
)

func TestDefaultTransportWithRSCIOGitHubAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("not running test that uses internet in short mode")
	}

	if !reflect.ValueOf(*http.DefaultClient).IsZero() {
		t.Fatal("internal error: the initial value of *http.DefaultClient is unexpectedly non-zero")
	}
	http.DefaultClient.Transport = defaultTransportWithRSCIOGitHubAuth{
		GitHubToken: &oauth2.Token{AccessToken: "pretend-token"},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = nil })
	gh := new(github.Client)

	_, err := gh.GraphQLQuery(`query { viewer { login } }`, nil)
	if err == nil {
		t.Fatal("GraphQLQuery returned nil error, want non-nil error")
	} else if got, wantPrefix := err.Error(), "401 Unauthorized"; !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("GraphQLQuery returned %v, want prefix %v", got, wantPrefix)
	}
}
