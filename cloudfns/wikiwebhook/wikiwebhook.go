// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package wikiwebhook implements an Google Cloud Function HTTP handler that
// expects GitHub webhook change events. Specifically, it reacts to wiki change
// events and posts the payload to a pubsub topic.
package wikiwebhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"cloud.google.com/go/pubsub"
)

var (
	githubSecret = os.Getenv("GITHUB_WEBHOOK_SECRET")
	projectID    = os.Getenv("GCP_PROJECT")
	pubsubTopic  = os.Getenv("PUBSUB_TOPIC")
)

func GitHubWikiChangeWebHook(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not read request body: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !validSignature(body, []byte(githubSecret), r.Header.Get("X-Hub-Signature")) {
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return
	}

	evt := r.Header.Get("X-GitHub-Event")
	// Ping event is sent upon initial setup of the webhook.
	if evt == "ping" {
		fmt.Fprintf(w, "pong")
		return
	}
	// See https://developer.github.com/v3/activity/events/types/#gollumevent.
	if evt != "gollum" {
		http.Error(w, fmt.Sprintf("incorrect event type %q", evt), http.StatusBadRequest)
		return
	}

	id, err := publishToTopic(pubsubTopic, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to publish to topic: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "Message ID: %s\n", id)
}

// publishToTopic publishes body to the given topic.
// It returns the ID of the message published.
var publishToTopic = func(topic string, body []byte) (string, error) {
	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("pubsub.NewClient: %v", err)
	}

	t := client.Topic(topic)
	resp := t.Publish(ctx, &pubsub.Message{Data: body})
	id, err := resp.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("topic.Publish: %v", err)
	}
	return id, nil
}

// validSignature reports whether the HMAC-SHA1 of body with key matches sig,
// which is in the form "sha1=<HMAC-SHA1 in hex>".
func validSignature(body, key []byte, sig string) bool {
	const prefix = "sha1="
	if len(sig) < len(prefix) {
		return false
	}
	sig = sig[len(prefix):]
	mac := hmac.New(sha1.New, key)
	mac.Write(body)
	b, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}

	// Use hmac.Equal to avoid timing attacks.
	return hmac.Equal(mac.Sum(nil), b)
}
