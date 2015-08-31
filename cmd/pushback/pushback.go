// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command pushback is a service that monitors a set of GitHub repositories
// for incoming Pull Requests, replies with contribution instructions, and
// closes the request. This is for projects that don't use Pull Requests.
package main

// TODO(adg): periodically poll repositories for pull requests in case we
// dropped any webhook calls.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"google.golang.org/cloud/compute/metadata"
)

const (
	authMetadataKey   = "pushback-credentials"
	secretMetadataKey = "pushback-webhook-secret"
)

func main() {
	http.HandleFunc("/webhook", webhook)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

const debug = false

func logErr(w io.Writer, desc string, err error) {
	log.Println(desc, err)
	if debug {
		fmt.Fprintln(w, desc, err)
	}
}

func webhook(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Github-Event") != "pull_request" {
		// Only handle pull request notifications.
		return
	}
	body, err := validate(r)
	if err != nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		logErr(w, "Error validating request:", err)
		return
	}
	var req struct {
		Action     string
		Number     int
		Repository struct {
			Full_Name string
		}
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		logErr(w, "Error decoding request:", err)
		return
	}
	if req.Action != "opened" {
		// Only handle "opened" actions.
		return
	}
	if err := closePR(req.Repository.Full_Name, req.Number); err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		logErr(w, "Error closing PR:", err)
		return
	}
	fmt.Fprintln(w, "OK")
}

// validate compares the signature in the request header with the body.
func validate(r *http.Request) (body []byte, err error) {
	// Decode signature header.
	sigHeader := r.Header.Get("X-Hub-Signature")
	sigParts := strings.SplitN(sigHeader, "=", 2)
	if len(sigParts) != 2 {
		return nil, fmt.Errorf("Bad signature header: %q", sigHeader)
	}
	var h func() hash.Hash
	switch alg := sigParts[0]; alg {
	case "sha1":
		h = sha1.New
	case "sha256":
		h = sha256.New
	default:
		return nil, fmt.Errorf("Unsupported hash algorithm: %q", alg)
	}
	gotSig, err := hex.DecodeString(sigParts[1])
	if err != nil {
		return nil, err
	}

	// Compute expected signature.
	key, err := metadata.ProjectAttributeValue(secretMetadataKey)
	if err != nil {
		return nil, err
	}
	body, err = ioutil.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(h, []byte(key))
	mac.Write(body)
	expectSig := mac.Sum(nil)

	if !hmac.Equal(gotSig, expectSig) {
		return nil, fmt.Errorf("Invalid signature %X, want %x", gotSig, expectSig)
	}
	return body, nil
}

// closePR posts a helpful message before closing the specified pull request.
func closePR(repo string, id int) error {
	// Post the comment.
	url := fmt.Sprintf("https://api.github.com/repos/%v/issues/%v/comments", repo, id)
	if err := doRequest("POST", url, bytes.NewReader(messageJSON)); err != nil {
		return fmt.Errorf("POST to %v: %v", url, err)
	}

	// Close the issue.
	url = fmt.Sprintf("https://api.github.com/repos/%v/pulls/%v", repo, id)
	if err := doRequest("PATCH", url, strings.NewReader(`{"state":"closed"}`)); err != nil {
		return fmt.Errorf("PATCH to %v: %v", url, err)
	}

	return nil
}

// doRequest makes an authenticated request to the GitHub API.
func doRequest(method, url string, body io.Reader) error {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}

	// These values are cached, so we can fetch them every time.
	userpass, err := metadata.ProjectAttributeValue(authMetadataKey)
	if err != nil {
		return err
	}
	p := strings.SplitN(userpass, ":", 2)
	if len(p) != 2 {
		return errors.New("bad authentication data")
	}
	req.SetBasicAuth(p[0], p[1])

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	respBody, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Bad response: %v\nBody:\n%s", resp.Status, respBody)
	}
	return nil

}

const message = `
Hi! Thanks for the PR!

However, our project doesn't use GitHub's Pull Requests,
so we can't accept your contribution this way.

We instead use a code review system called Gerrit.
You can find the details in our Contribution Guidelines:
https://golang.org/doc/contribute.html

I'm going to close this Pull Request now.
Please consider sending this change using Gerrit.
`

var messageJSON []byte

func init() {
	var err error
	messageJSON, err = json.Marshal(struct {
		Body string `json:"body"`
	}{message})
	if err != nil {
		panic(err)
	}
}
