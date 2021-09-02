// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package secret provides a client interface for interacting
// with the GCP Secret Management service.
package secret

import (
	"context"
	"io"
	"log"
	"path"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	gax "github.com/googleapis/gax-go/v2"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

const (
	// NameBuilderMasterKey is the secret name for the builder master key.
	NameBuilderMasterKey = "builder-master-key"

	// NameFarmerRunBench is the secret name for farmer run bench.
	NameFarmerRunBench = "farmer-run-bench"

	// NameGerritbotGitCookies is the secret name for Gerritbot Git cookies.
	NameGerritbotGitCookies = "gerritbot-gitcookies"

	// NameGitHubSSH is the secret name for GitHub SSH key.
	NameGitHubSSH = "github-ssh"

	// NameGitHubSSHKey is the secret name for the GitHub SSH private key.
	NameGitHubSSHKey = "github-ssh-private-key"

	// NameGobotPassword is the secret name for the Gobot password.
	NameGobotPassword = "gobot-password"

	// NameGomoteSSHPublicKey is the secret name for the gomote SSH public key.
	NameGomoteSSHPublicKey = "gomote-ssh-public-key"

	// NameMaintnerGitHubToken is the secret name for the Maintner GitHub token.
	NameMaintnerGitHubToken = "maintner-github-token"

	// NameGitHubWebhookSecret is the secret name for a golang/go GitHub webhook secret.
	NameGitHubWebhookSecret = "github-webhook-secret"

	// NamePubSubHelperWebhook is the secret name for the pubsub helper webhook secret.
	NamePubSubHelperWebhook = "pubsubhelper-webhook-secret"

	// NameAWSAccessKey is the secret name for the AWS access key.
	NameAWSAccessKey = "aws-access-key"

	// NameAWSKeyID is the secret name for the AWS key id.
	NameAWSKeyID = "aws-key-id"

	// NameSendGridAPIKey is the secret name for a Go project SendGrid API key.
	// This API key only allows sending email.
	NameSendGridAPIKey = "sendgrid-sendonly-api-key"
)

type secretClient interface {
	AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
	io.Closer
}

// Client is used to interact with the GCP Secret Management service.
type Client struct {
	client    secretClient
	projectID string // projectID specifies the ID of the GCP project where secrets are retreived from.
}

// NewClient creates a Secret Manager Client
// that targets the current GCP instance's project ID.
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

// NewClientInProject creates a Secret Manager Client
// that targets the specified GCP project ID.
func NewClientInProject(projectID string) (*Client, error) {
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

// MustNewClient instantiates an instance of the Secret Manager Client. If there is an error
// this fuction will exit.
func MustNewClient() *Client {
	c, err := NewClient()
	if err != nil {
		log.Fatalf("unable to create secret client %v", err)
	}
	return c
}
