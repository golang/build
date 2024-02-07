// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package macservice defines the client API for MacService.
package macservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const baseURL = "https://macservice-pa.googleapis.com/v1alpha1/"

// Client is a MacService client.
type Client struct {
	apiKey string

	client *http.Client
}

// NewClient creates a MacService client, authenticated with the provided API
// key.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		client: http.DefaultClient,
	}
}

func (c *Client) do(method, endpoint string, input, output any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(input); err != nil {
		return fmt.Errorf("error encoding request: %w", err)
	}

	req, err := http.NewRequest(method, baseURL+endpoint, &buf)
	if err != nil {
		return fmt.Errorf("error building request: %w", err)
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("x-goog-api-key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("response error %s: %s", resp.Status, body)
	}

	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("error decoding response: %w; body: %s", err, body)
	}

	return nil
}

// Lease creates a new lease.
func (c *Client) Lease(req LeaseRequest) (LeaseResponse, error) {
	var resp LeaseResponse
	if err := c.do("POST", "leases:create", req, &resp); err != nil {
		return LeaseResponse{}, fmt.Errorf("error sending request: %w", err)
	}
	return resp, nil
}

// Renew updates the expiration time of a lease. Note that
// RenewRequest.Duration is the lease duration from now, not from the current
// lease expiration time.
func (c *Client) Renew(req RenewRequest) (RenewResponse, error) {
	var resp RenewResponse
	if err := c.do("POST", "leases:renew", req, &resp); err != nil {
		return RenewResponse{}, fmt.Errorf("error sending request: %w", err)
	}
	return resp, nil
}

// Vacate vacates a lease.
func (c *Client) Vacate(req VacateRequest) error {
	var resp struct{} // no response body
	if err := c.do("POST", "leases:vacate", req, &resp); err != nil {
		return fmt.Errorf("error sending request: %w", err)
	}
	return nil
}

// Find searches for leases.
func (c *Client) Find(req FindRequest) (FindResponse, error) {
	var resp FindResponse
	if err := c.do("POST", "leases:find", req, &resp); err != nil {
		return FindResponse{}, fmt.Errorf("error sending request: %w", err)
	}
	return resp, nil
}
