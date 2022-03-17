// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This program runs in the InfluxDB container, performs initial setup of the
// database, and publishes access secrets to secret manager. If the database is
// already set up, it just sets up certificates and starts InfluxDB.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/domain"
	"cloud.google.com/go/compute/metadata"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

const influxURL = "https://localhost:443"

func main() {
	if err := run(); err != nil {
		log.Printf("Error completing setup: %v", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// Connecting via localhost with self-signed certs, so no cert checks.
	options := influxdb2.DefaultOptions()
	options.SetTLSConfig(&tls.Config{InsecureSkipVerify: true})
	client := influxdb2.NewClientWithOptions(influxURL, "", options)
	defer client.Close()

	log.Printf("Waiting for influx to start...")
	for {
		_, err := client.Ready(ctx)
		if err != nil {
			log.Printf("Influx not ready: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}

	log.Printf("Influx ready!")

	allowed, err := setupAllowed(ctx)
	if err != nil {
		return fmt.Errorf("error checking setup: %w", err)
	}
	if !allowed {
		log.Printf("Influx already set up!")
		return nil
	}

	secrets, err := setupUsers(ctx, client)
	if err != nil {
		return fmt.Errorf("error setting up users: %w", err)
	}

	if err := secrets.recordOrLog(ctx); err != nil {
		return fmt.Errorf("error recording secrets: %w", err)
	}

	log.Printf("Influx setup complete!")
	return nil
}

// Setup is the response to Influx GET /api/v2/setup.
type Setup struct {
	Allowed bool `json:"allowed"`
}

// setupAllowed returns true if Influx setup is allowed. i.e., the server has
// not already been set up.
//
// The Influx Go client unfortunately doesn't expose a method to query this, so
// we must access the API directly.
func setupAllowed(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", influxURL+"/api/v2/setup", nil)
	if err != nil {
		return false, fmt.Errorf("error creating request: %w", err)
	}

	// Connecting via localhost with self-signed certs, so no cert checks.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("error send request: %w", err)
	}
	defer resp.Body.Close()

	var s Setup
	d := json.NewDecoder(resp.Body)
	if err := d.Decode(&s); err != nil {
		return false, fmt.Errorf("error decoding response: %w", err)
	}

	return s.Allowed, nil
}

type influxSecrets struct {
	adminPass   string
	adminToken  string
	readerPass  string
	readerToken string
}

const (
	adminPassSecretName   = "influx-admin-pass"
	adminTokenSecretName  = "influx-admin-token"
	readerPassSecretName  = "influx-reader-pass"
	readerTokenSecretName = "influx-reader-token"
)

// recordOrLog saves the secrets to Secret Manager, if available, or simply
// logs them when not running on GCP.
func (i *influxSecrets) recordOrLog(ctx context.Context) error {
	projectID, err := metadata.ProjectID()
	if err != nil {
		log.Printf("Error fetching GCP project ID: %v", err)
		log.Printf("Assuming I am running locally.")
		log.Printf("Admin password: %s", i.adminPass)
		log.Printf("Admin token: %s", i.adminToken)
		log.Printf("Reader password: %s", i.readerPass)
		log.Printf("Reader token: %s", i.readerToken)
		return nil
	}

	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("error creating secret manager client: %w", err)
	}
	defer client.Close()

	addSecretVersion := func(name, data string) error {
		parent := fmt.Sprintf("projects/%s/secrets/%s", projectID, name)
		req := &secretmanagerpb.AddSecretVersionRequest{
			Parent: parent,
			Payload: &secretmanagerpb.SecretPayload{
				Data: []byte(data),
			},
		}

		if _, err := client.AddSecretVersion(ctx, req); err != nil {
			return fmt.Errorf("add secret version error: %w", err)
		}

		log.Printf("Secret added to %s", parent)

		return nil
	}

	if err := addSecretVersion(adminPassSecretName, i.adminPass); err != nil {
		return fmt.Errorf("error adding admin password secret: %w", err)
	}
	if err := addSecretVersion(adminTokenSecretName, i.adminToken); err != nil {
		return fmt.Errorf("error adding admin token secret: %w", err)
	}
	if err := addSecretVersion(readerPassSecretName, i.readerPass); err != nil {
		return fmt.Errorf("error adding reader password secret: %w", err)
	}
	if err := addSecretVersion(readerTokenSecretName, i.readerToken); err != nil {
		return fmt.Errorf("error adding reader token secret: %w", err)
	}

	log.Printf("Secrets added to secret manager")

	return nil
}

// setupUsers sets up an 'admin' and 'reader' user on a new InfluxDB instance.
func setupUsers(ctx context.Context, client influxdb2.Client) (influxSecrets, error) {
	adminPass, err := generatePassword()
	if err != nil {
		return influxSecrets{}, fmt.Errorf("error generating 'admin' password: %w", err)
	}

	// Initial instance setup; creates admin user.
	onboard, err := client.Setup(ctx, "admin", adminPass, "golang", "perf", 0)
	if err != nil {
		return influxSecrets{}, fmt.Errorf("influx setup error: %w", err)
	}

	// Create a read-only user.
	reader, err := client.UsersAPI().CreateUserWithName(ctx, "reader")
	if err != nil {
		return influxSecrets{}, fmt.Errorf("error creating user 'reader': %w", err)
	}

	readerPass, err := generatePassword()
	if err != nil {
		return influxSecrets{}, fmt.Errorf("error generating 'reader' password: %w", err)
	}

	if err := client.UsersAPI().UpdateUserPassword(ctx, reader, readerPass); err != nil {
		return influxSecrets{}, fmt.Errorf("error setting 'reader' password: %w", err)
	}

	// Add 'reader' to 'golang' org.
	if _, err := client.OrganizationsAPI().AddMember(ctx, onboard.Org, reader); err != nil {
		return influxSecrets{}, fmt.Errorf("error adding 'reader' to org 'golang': %w", err)
	}

	// Grant read access to buckets and dashboards.
	newAuth := &domain.Authorization{
		OrgID:  onboard.Org.Id,
		UserID: reader.Id,
		Permissions: &[]domain.Permission{
			{
				Action: domain.PermissionActionRead,
				Resource: domain.Resource{
					Type: domain.ResourceTypeBuckets,
				},
			},
			{
				Action: domain.PermissionActionRead,
				Resource: domain.Resource{
					Type: domain.ResourceTypeDashboards,
				},
			},
		},
	}
	auth, err := client.AuthorizationsAPI().CreateAuthorization(ctx, newAuth)
	if err != nil {
		return influxSecrets{}, fmt.Errorf("error granting access to 'reader': %w", err)
	}

	return influxSecrets{
		adminPass:   adminPass,
		adminToken:  *onboard.Auth.Token,
		readerPass:  readerPass,
		readerToken: *auth.Token,
	}, nil
}

func generatePassword() (string, error) {
	const passwordCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789~!@#$%^&*()_+`-={}|[]\\:\"<>?,./"
	const length = 64

	b := make([]byte, 0, length)
	max := big.NewInt(int64(len(passwordCharacters) - 1))
	for i := 0; i < length; i++ {
		j, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("error generating random number: %w", err)
		}
		b = append(b, passwordCharacters[j.Int64()])
	}

	return string(b), nil
}
