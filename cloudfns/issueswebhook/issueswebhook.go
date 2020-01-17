// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package issueswebhook implements a Google Cloud Function HTTP handler that
// expects GitHub webhook change events. Specifically, it reacts to issue change
// events and saves the data to a GCS bucket.
package issueswebhook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"cloud.google.com/go/storage"
	"github.com/google/go-github/v28/github"
)

var (
	githubSecret = os.Getenv("GITHUB_WEBHOOK_SECRET")
	bucket       = os.Getenv("GCS_BUCKET")
)

func GitHubIssueChangeWebHook(w http.ResponseWriter, r *http.Request) {
	b, err := github.ValidatePayload(r, []byte(githubSecret))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not validate payload: %v\n", err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Ping event is sent upon initial setup of the webhook.
	if github.WebHookType(r) == "ping" {
		fmt.Fprintf(w, "pong")
		return
	}

	id := r.Header.Get("X-GitHub-Delivery")
	if id == "" {
		fmt.Fprintf(os.Stderr, "X-GitHub-Delivery header not present\n")
		http.Error(w, "X-GitHub-Delivery header not present", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	wc, err := newObjectWriter(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not create bucket object writer: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := wc.Write(b); err != nil {
		fmt.Fprintf(os.Stderr, "Write: could not copy body to bucket object writer: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := wc.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Close: could not copy body to bucket object writer: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "Message ID: %s\n", id)
}

var newObjectWriter = func(ctx context.Context, id string) (io.WriteCloser, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage.NewClient: %v", err)
	}

	wc := client.Bucket(bucket).Object(id + ".json").NewWriter(ctx)
	wc.ContentType = "application/json"
	return wc, nil
}
