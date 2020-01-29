// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package secret provides a client interface for interacting
// with the GCP Secret Management service.
package secret

import (
	"context"
	"io"
	"path"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1beta1"
	gax "github.com/googleapis/gax-go/v2"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1beta1"
)

type secretClient interface {
	AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
	io.Closer
}

// Client is used to interact with the GCP Secret Management service.
type Client struct {
	client    secretClient
	projectID string
}

// NewClient instantiates an instance of the Secret Manager Client.
func NewClient() (*Client, error) {
	projectID, err := metadata.ProjectID()
	if err != nil {
		return nil, err
	}

	// The default client configuration includes retries on transient failures.
	// It is a non-blocking blocking call which is why we do not set a timeout on
	// the context.
	client, err := secretmanager.NewClient(context.Background())
	if err != nil {
		return nil, err
	}

	return &Client{
		client:    client,
		projectID: projectID,
	}, nil
}

// Retrieve the named secret from the Secret Management service.
func (smc *Client) Retrieve(ctx context.Context, name string) (string, error) {
	r, err := smc.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: buildNamePath(smc.projectID, name, "latest"),
	})
	if err != nil {
		return "", err
	}
	return string(r.Payload.GetData()), nil
}

// Close closes the connection to the Secret Management service.
func (smc *Client) Close() error {
	return smc.client.Close()
}

// buildNamePath creates the name path required by the Secret Management service to
// query for a secret.
func buildNamePath(projectID, name, version string) string {
	return path.Join("projects", projectID, "secrets", name, "versions", version)
}
