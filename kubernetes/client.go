// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package kubernetes contains a minimal client for the Kubernetes API.
package kubernetes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/build/kubernetes/api"
)

const (
	// APIEndpoint defines the base path for kubernetes API resources.
	APIEndpoint  = "/api/v1"
	defaultPodNS = "/namespaces/default/pods"
)

// Client is a client for the Kubernetes master.
type Client struct {
	endpointURL string
	httpClient  *http.Client
}

// NewClient returns a new Kubernetes client.
// The provided host is an url (scheme://hostname[:port]) of a
// Kubernetes master without any path.
// The provided client is an authorized http.Client used to perform requests to the Kubernetes API master.
func NewClient(baseURL string, client *http.Client) (*Client, error) {
	validURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL %q: %v", baseURL, err)
	}
	return &Client{
		endpointURL: strings.TrimSuffix(validURL.String(), "/") + APIEndpoint,
		httpClient:  client,
	}, nil
}

// RunPod create a new pod resource in the default pod namespace with
// the given pod API specification.
// It returns the pod status once it is not pending anymore.
func (c *Client) Run(pod *api.Pod) (*api.PodStatus, error) {
	var podJSON bytes.Buffer
	if err := json.NewEncoder(&podJSON).Encode(pod); err != nil {
		return nil, fmt.Errorf("failed to encode pod in json: %v", err)
	}
	postURL := c.endpointURL + defaultPodNS
	r, err := http.NewRequest("POST", postURL, &podJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: POST %q : %v", postURL, err)
	}
	res, err := c.httpClient.Do(r)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: POST %q: %v", postURL, err)
	}
	body, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read request body for POST %q: %v", postURL, err)
	}
	if res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("http error: %d POST %q: %q: %v", res.StatusCode, postURL, string(body), err)
	}
	var podResult api.Pod
	if err := json.Unmarshal(body, &podResult); err != nil {
		return nil, fmt.Errorf("failed to decode pod resources: %v", err)
	}
	for podResult.Status.Phase == "Pending" {
		getURL := c.endpointURL + defaultPodNS + "/" + pod.Name
		r, err := http.NewRequest("GET", getURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: GET %q : %v", getURL, err)
		}
		res, err := c.httpClient.Do(r)
		if err != nil {
			return nil, fmt.Errorf("failed to make request: GET %q: %v", getURL, err)
		}
		body, err := ioutil.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read request body for GET %q: %v", getURL, err)
		}
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("http error %d GET %q: %q: %v", res.StatusCode, getURL, string(body), err)
		}
		if err := json.Unmarshal(body, &podResult); err != nil {
			return nil, fmt.Errorf("failed to decode pod resources: %v", err)
		}
		time.Sleep(1 * time.Second)
		// TODO(proppy): add a Cancel type to this func later
		// so this can select on it.
	}
	return &podResult.Status, nil
}
