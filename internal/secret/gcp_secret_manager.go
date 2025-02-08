// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package secret provides a client interface for interacting
// with the GCP Secret Management service.
package secret

import (
	"context"
	"fmt"
	"io"
	"log"
	"path"

	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"
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

	// NameGobotPassword is the secret name for the gobot@golang.org Gerrit account password.
	NameGobotPassword = "gobot-password"

	// NameGomoteSSHCAPrivateKey is the secret name for the gomote SSH certificate authority private key.
	NameGomoteSSHCAPrivateKey = "gomote-ssh-ca-private-key"

	// NameGomoteSSHCAPublicKey is the secret name for the gomote SSH certificate authority public key.
	NameGomoteSSHCAPublicKey = "gomote-ssh-ca-public-key"

	// NameGomoteSSHPrivateKey is the secret name for the gomote SSH private key.
	NameGomoteSSHPrivateKey = "gomote-ssh-private-key"

	// NameGomoteSSHPublicKey is the secret name for the gomote SSH public key.
	NameGomoteSSHPublicKey = "gomote-ssh-public-key"

	// NameMaintnerGitHubToken is the secret name for the Maintner GitHub token.
	NameMaintnerGitHubToken = "maintner-github-token"

	// NameWatchflakesGitHubToken is the secret name for the watchflakes GitHub token.
	NameWatchflakesGitHubToken = "watchflakes-github-token"

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

	// NameTwitterAPISecret is the secret name for Twitter API credentials for
	// posting tweets from the Go project's Twitter account (twitter.com/golang).
	//
	// The secret value encodes relevant keys and their secrets as
	// a JSON object that can be unmarshaled into TwitterCredentials:
	//
	// 	{
	// 		"ConsumerKey":       "...",
	// 		"ConsumerSecret":    "...",
	// 		"AccessTokenKey":    "...",
	// 		"AccessTokenSecret": "..."
	// 	}
	NameTwitterAPISecret = "twitter-api-secret"
	// NameStagingTwitterAPISecret is the secret name for Twitter API credentials
	// for posting tweets using a staging test Twitter account.
	//
	// This secret is available in the Secret Manager of the x/build staging GCP project.
	//
	// The secret value encodes relevant keys and their secrets as
	// a JSON object that can be unmarshaled into TwitterCredentials.
	NameStagingTwitterAPISecret = "staging-" + NameTwitterAPISecret

	// NameMastodonAPISecret is the secret name for Mastodon API credentials
	// for posting to Hachyderm.io/@golang.  The secret value is a JSON
	// encoding of the MastodonCredentials.
	NameMastodonAPISecret = "mastodon-api-secret"

	// NameMacServiceAPIKey is the secret name for the MacService API key.
	NameMacServiceAPIKey = "macservice-api-key"

	// NameVSCodeMarketplacePublishToken is the secret name for VS Code
	// Marketplace publisher key.
	NameVSCodeMarketplacePublishToken = "vscode-marketplace-token"
)

// TwitterCredentials holds Twitter API credentials.
type TwitterCredentials struct {
	ConsumerKey       string
	ConsumerSecret    string
	AccessTokenKey    string
	AccessTokenSecret string
}

type MastodonCredentials struct {
	// Log in to <Instance> as your bot account,
	// navigate to Profile -> Development,
	// Click on <Application> in the Application column,
	// and it will reveal Client Key, Client Secret, and Access Token
	Instance      string // Instance (e.g. "botsin.space")
	Application   string // Application name (e.g. ""Go benchmarking bot"")
	ClientKey     string // Client Key
	ClientSecret  string // Client secret
	AccessToken   string // Access token
	TestRecipient string // For testing only, ignored by non-test API
}

func (t TwitterCredentials) String() string {
	return fmt.Sprintf("{%s (redacted) %s (redacted)}", t.ConsumerKey, t.AccessTokenKey)
}
func (t TwitterCredentials) GoString() string {
	return fmt.Sprintf("secret.TwitterCredentials{ConsumerKey:%q ConsumerSecret:(redacted) AccessTokenKey:%q AccessTokenSecret:(redacted)}", t.ConsumerKey, t.AccessTokenKey)
}

func (t MastodonCredentials) String() string {
	return fmt.Sprintf("{%s %s (redacted) (redacted) (redacted)}", t.Instance, t.Application)
}
func (t MastodonCredentials) GoString() string {
	return fmt.Sprintf("secret.MastodonCredentials{Instance:%q Application:%q ClientKey:(redacted) ClientSecret:(redacted) AccessToken:(redacted)}", t.Instance, t.Application)
}

type secretClient interface {
	AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
	io.Closer
}

// Client is used to interact with the GCP Secret Management service.
type Client struct {
	client    secretClient
	projectID string // projectID specifies the ID of the GCP project where secrets are retrieved from.
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
// this function will exit.
func MustNewClient() *Client {
	c, err := NewClient()
	if err != nil {
		log.Fatalf("unable to create secret client %v", err)
	}
	return c
}
