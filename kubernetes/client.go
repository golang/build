// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package kubernetes contains a minimal client for the Kubernetes API.
package kubernetes

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/build/kubernetes/api"
	"golang.org/x/net/context"
	"golang.org/x/net/context/ctxhttp"
)

const (
	// APIEndpoint defines the base path for kubernetes API resources.
	APIEndpoint       = "/api/v1"
	defaultPodNS      = "/namespaces/default/pods"
	defaultSecretNS   = "/namespaces/default/secrets"
	defaultWatchPodNS = "/watch/namespaces/default/pods"
	nodes             = "/nodes"
)

// ErrSecretNotFound is returned by GetSecret when a secret is not found.
var ErrSecretNotFound = errors.New("kubernetes: secret not found")

// APIError is returned by Client methods when an API call failed.
type APIError struct {
	StatusCode int
	Body       string
	Header     http.Header
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %q", e.StatusCode, e.Body)
}

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

// RunLongLivedPod creates a new pod resource in the default pod namespace with
// the given pod API specification. It assumes the pod runs a
// long-lived server (i.e. if the container exit quickly quickly, even
// with success, then that is an error).
//
// It returns the pod status once it has entered the Running phase.
// An error is returned if the pod can not be created, or if ctx.Done
// is closed.
func (c *Client) RunLongLivedPod(ctx context.Context, pod *api.Pod) (*api.PodStatus, error) {
	var podResult api.Pod
	if err := c.do(ctx, &podResult, "POST", defaultPodNS, pod); err != nil {
		return nil, err
	}

	for {
		// TODO(bradfitz,evanbrown): pass podResult.ObjectMeta.ResourceVersion to PodStatus?
		ps, err := c.PodStatus(ctx, podResult.Name)
		if err != nil {
			return nil, err
		}
		switch ps.Phase {
		case api.PodPending:
			// The main phase we're waiting on
			break
		case api.PodRunning:
			return ps, nil
		case api.PodSucceeded, api.PodFailed:
			return nil, fmt.Errorf("pod entered phase %q", ps.Phase)
		default:
			log.Printf("RunLongLivedPod poll loop: pod %q in unexpected phase %q; sleeping", podResult.Name, ps.Phase)
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			// The pod did not leave the pending
			// state. Try to clean it up.
			go c.DeletePod(context.Background(), podResult.Name)
			return nil, ctx.Err()
		}
	}
}

// GetPods returns all pods in the cluster, regardless of status.
func (c *Client) GetPods(ctx context.Context) ([]api.Pod, error) {
	var res api.PodList
	if err := c.do(ctx, &res, "GET", c.endpointURL+defaultPodNS, nil); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// PodDelete deletes the specified Kubernetes pod.
func (c *Client) DeletePod(ctx context.Context, podName string) error {
	return c.do(ctx, nil, "DELETE", defaultPodNS+"/"+podName, nil)
}

// TODO(bradfitz): WatchPod is unreliable, so this is disabled.
//
// AwaitPodNotPending will return a pod's status in a
// podStatusResult when the pod is no longer in the pending
// state.
// The podResourceVersion is required to prevent a pod's entire
// history from being retrieved when the watch is initiated.
// If there is an error polling for the pod's status, or if
// ctx.Done is closed, podStatusResult will contain an error.
func (c *Client) _AwaitPodNotPending(ctx context.Context, podName, podResourceVersion string) (*api.Pod, error) {
	if podResourceVersion == "" {
		return nil, fmt.Errorf("resourceVersion for pod %v must be provided", podName)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	podStatusUpdates, err := c._WatchPod(ctx, podName, podResourceVersion)
	if err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case psr := <-podStatusUpdates:
			if psr.Err != nil {
				// If the context is done, prefer its error:
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return nil, psr.Err
				}
			}
			if psr.Pod.Status.Phase != api.PodPending {
				return psr.Pod, nil
			}
		}
	}
}

// PodStatusResult wraps an api.PodStatus and error.
type PodStatusResult struct {
	Pod  *api.Pod
	Type string
	Err  error
}

type watchPodStatus struct {
	// The type of watch update contained in the message
	Type string `json:"type"`
	// Pod details
	Object api.Pod `json:"object"`
}

// TODO(bradfitz): WatchPod is unreliable and sometimes hangs forever
// without closing and sometimes ends prematurely, so this API is
// disabled.
//
// WatchPod long-polls the Kubernetes watch API to be notified
// of changes to the specified pod. Changes are sent on the returned
// PodStatusResult channel as they are received.
// The podResourceVersion is required to prevent a pod's entire
// history from being retrieved when the watch is initiated.
// The provided context must be canceled or timed out to stop the watch.
// If any error occurs communicating with the Kubernetes API, the
// error will be sent on the returned PodStatusResult channel and
// it will be closed.
func (c *Client) _WatchPod(ctx context.Context, podName, podResourceVersion string) (<-chan PodStatusResult, error) {
	if podResourceVersion == "" {
		return nil, fmt.Errorf("resourceVersion for pod %v must be provided", podName)
	}
	statusChan := make(chan PodStatusResult, 1)

	go func() {
		defer close(statusChan)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Make request to Kubernetes API
		getURL := c.endpointURL + defaultWatchPodNS + "/" + podName
		req, err := http.NewRequest("GET", getURL, nil)
		req.URL.Query().Add("resourceVersion", podResourceVersion)
		if err != nil {
			statusChan <- PodStatusResult{Err: fmt.Errorf("failed to create request: GET %q : %v", getURL, err)}
			return
		}
		res, err := ctxhttp.Do(ctx, c.httpClient, req)
		if err != nil {
			statusChan <- PodStatusResult{Err: err}
			return
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			statusChan <- PodStatusResult{Err: fmt.Errorf("WatchPod status %v", res.Status)}
			return
		}
		reader := bufio.NewReader(res.Body)

		// bufio.Reader.ReadBytes is blocking, so we watch for
		// context timeout or cancellation in a goroutine
		// and close the response body when see see it. The
		// response body is also closed via defer when the
		// request is made, but closing twice is OK.
		go func() {
			<-ctx.Done()
			res.Body.Close()
		}()

		const backupPollDuration = 30 * time.Second
		backupPoller := time.AfterFunc(backupPollDuration, func() {
			log.Printf("kubernetes: backup poller in WatchPod checking on %q", podName)
			st, err := c.PodStatus(ctx, podName)
			log.Printf("kubernetes: backup poller in WatchPod PodStatus(%q) = %v, %v", podName, st, err)
			if err != nil {
				// Some error.
				cancel()
			}
		})
		defer backupPoller.Stop()

		for {
			line, err := reader.ReadBytes('\n')
			log.Printf("kubernetes WatchPod status line of %q: %q, %v", podName, line, err)
			backupPoller.Reset(backupPollDuration)
			if err != nil {
				statusChan <- PodStatusResult{Err: fmt.Errorf("error reading streaming response body: %v", err)}
				return
			}
			var wps watchPodStatus
			if err := json.Unmarshal(line, &wps); err != nil {
				statusChan <- PodStatusResult{Err: fmt.Errorf("failed to decode watch pod status: %v", err)}
				return
			}
			statusChan <- PodStatusResult{Pod: &wps.Object, Type: wps.Type}
		}
	}()
	return statusChan, nil
}

// Retrieve the status of a pod synchronously from the Kube
// API server.
func (c *Client) PodStatus(ctx context.Context, podName string) (*api.PodStatus, error) {
	var pod api.Pod
	if err := c.do(ctx, &pod, "GET", defaultPodNS+"/"+podName, nil); err != nil {
		return nil, err
	}
	return &pod.Status, nil
}

// PodLog retrieves the container log for the first container
// in the pod.
func (c *Client) PodLog(ctx context.Context, podName string) (string, error) {
	// TODO(evanbrown): support multiple containers
	var logs string
	if err := c.do(ctx, &logs, "GET", defaultPodNS+"/"+podName+"/log", nil); err != nil {
		return "", err
	}
	return logs, nil
}

// PodNodes returns the list of nodes that comprise the Kubernetes cluster
func (c *Client) GetNodes(ctx context.Context) ([]api.Node, error) {
	var res api.NodeList
	if err := c.do(ctx, &res, "GET", nodes, nil); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// CreateSecret creates a new secret resource in the default secret namespace with
// the given secret.
// It returns a new secret instance corresponding to the server side representation.
func (c *Client) CreateSecret(ctx context.Context, secret *api.Secret) (*api.Secret, error) {
	var res api.Secret
	if err := c.do(ctx, &res, "POST", defaultSecretNS, secret); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetSecret returns the specified secret from the default secret namespace.
// If the secret is not found, the err will be ErrSecretNotFound.
func (c *Client) GetSecret(ctx context.Context, name string) (*api.Secret, error) {
	var res api.Secret
	if err := c.do(ctx, &res, "GET", defaultSecretNS+"/"+name, nil); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) do(ctx context.Context, dst interface{}, method string, path string, payload interface{}) error {
	var body io.Reader
	if payload != nil {
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			return fmt.Errorf("failed encode json payload: %v", err)
		}
		body = buf
	}
	req, err := http.NewRequest(method, c.endpointURL+path, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %s %q : %v", method, path, err)
	}
	resp, err := ctxhttp.Do(ctx, c.httpClient, req)
	if err != nil {
		return fmt.Errorf("failed to perform request: %s %q: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := ioutil.ReadAll(resp.Body)
		return &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			Header:     resp.Header,
		}
	}

	switch dst := dst.(type) {
	case nil:
		return nil
	case *string:
		// string dest
		bs, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read raw body: %v", err)
		}
		*dst = string(bs)
	default:
		// json dest
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return fmt.Errorf("failed to decode API response: %v", err)
		}
	}
	return nil
}
