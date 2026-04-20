// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"strings"

	"golang.org/x/build/cmd/pubsubhelper/pubsubtypes"
)

func handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(w, "HTTPS required", http.StatusBadRequest)
		return
	}
	body, err := validateGitHubRequest(w, r)
	if err != nil {
		log.Printf("failed to validate github webhook request: %v", err)
		// But send a 200 OK anyway, so they don't queue up on
		// GitHub's side if they're real.
		return
	}

	var payload githubWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("error unmarshalling payload: %v; payload=%s", err, body)
		// But send a 200 OK anyway. Our fault.
		return
	}
	back, _ := json.MarshalIndent(payload, "", "\t")
	log.Printf("github verified webhook: %s", back)

	if payload.Repository == nil || (payload.Issue == nil && payload.PullRequest == nil) {
		// Ignore.
		return
	}

	f := strings.SplitN(payload.Repository.FullName, "/", 3)
	if len(f) != 2 {
		log.Printf("bogus repository name %q", payload.Repository.FullName)
		return
	}
	owner, repo := f[0], f[1]

	var issueNumber int
	if payload.Issue != nil {
		issueNumber = payload.Issue.Number
	}
	var prNumber int
	if payload.PullRequest != nil {
		prNumber = payload.PullRequest.Number
	}

	publish(&pubsubtypes.Event{
		GitHub: &pubsubtypes.GitHubEvent{
			Action:            payload.Action,
			RepoOwner:         owner,
			Repo:              repo,
			IssueNumber:       issueNumber,
			PullRequestNumber: prNumber,
		},
	})
}

// validateGitHubRequest compares the signature in the request header with the body.
func validateGitHubRequest(w http.ResponseWriter, r *http.Request) (body []byte, err error) {
	// Decode signature header.
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	sigAlg, sigHex, ok := strings.Cut(sigHeader, "=")
	if !ok {
		return nil, fmt.Errorf("Bad signature header: %q", sigHeader)
	}
	var h func() hash.Hash
	switch sigAlg {
	case "sha256":
		h = sha256.New
	default:
		return nil, fmt.Errorf("Unsupported hash algorithm: %q", sigAlg)
	}
	gotSig, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, err
	}

	body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	// TODO(golang/go#37171): find a cleaner solution than using a global
	mac := hmac.New(h, []byte(*webhookSecret))
	mac.Write(body)
	expectSig := mac.Sum(nil)

	if !hmac.Equal(gotSig, expectSig) {
		return nil, fmt.Errorf("Invalid signature %X, want %x", gotSig, expectSig)
	}
	return body, nil
}

type githubWebhookPayload struct {
	Action      string             `json:"action"`
	Repository  *githubRepository  `json:"repository"`
	Issue       *githubIssue       `json:"issue"`
	PullRequest *githubPullRequest `json:"pull_request"`
}

type githubRepository struct {
	FullName string `json:"full_name"` // "golang/go"
}

type githubIssue struct {
	URL    string `json:"url"`    // https://api.github.com/repos/baxterthehacker/public-repo/issues/2
	Number int    `json:"number"` // 2
}

type githubPullRequest struct {
	URL    string `json:"url"`    // https://api.github.com/repos/baxterthehacker/public-repo/pulls/8
	Number int    `json:"number"` // 8
}
