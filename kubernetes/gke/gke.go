// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package gke contains code for interacting with Google Container Engine (GKE),
// the hosted version of Kubernetes on Google Cloud Platform.
//
// The API is not subject to the Go 1 compatibility promise and may change at
// any time. Users should vendor this package and deal with API changes.
package gke

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"

	"cloud.google.com/go/compute/metadata"

	"golang.org/x/build/kubernetes"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/container/v1"
)

// ClientOpt represents an option that can be passed to the Client function.
type ClientOpt interface {
	modify(*clientOpt)
}

type clientOpt struct {
	Project     string
	TokenSource oauth2.TokenSource
	Namespace   string
}

type clientOptFunc func(*clientOpt)

func (f clientOptFunc) modify(o *clientOpt) { f(o) }

// OptProject returns an option setting the GCE Project ID to projectName.
// This is the named project ID, not the numeric ID.
// If unspecified, the current active project ID is used, if the program is running
// on a GCE instance.
func OptProject(projectName string) ClientOpt {
	return clientOptFunc(func(o *clientOpt) {
		o.Project = projectName
	})
}

// OptTokenSource sets the oauth2 token source for making
// authenticated requests to the GKE API. If unset, the default token
// source is used (https://godoc.org/golang.org/x/oauth2/google#DefaultTokenSource).
func OptTokenSource(ts oauth2.TokenSource) ClientOpt {
	return clientOptFunc(func(o *clientOpt) {
		o.TokenSource = ts
	})
}

// OptNamespace sets the Kubernetes namespace to look in.
func OptNamespace(namespace string) ClientOpt {
	return clientOptFunc(func(o *clientOpt) {
		o.Namespace = namespace
	})
}

// NewClient returns an Kubernetes client to a GKE cluster.
func NewClient(ctx context.Context, clusterName string, location string, opts ...ClientOpt) (*kubernetes.Client, error) {
	opt := clientOpt{Namespace: "default"}
	for _, o := range opts {
		o.modify(&opt)
	}
	if opt.TokenSource == nil {
		var err error
		opt.TokenSource, err = google.DefaultTokenSource(ctx, compute.CloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("failed to get a token source: %v", err)
		}
	}
	if opt.Project == "" {
		proj, err := metadata.ProjectID()
		if err != nil {
			return nil, fmt.Errorf("metadata.ProjectID: %v", err)
		}
		opt.Project = proj
	}

	httpClient := oauth2.NewClient(ctx, opt.TokenSource)
	containerService, err := container.New(httpClient)
	if err != nil {
		return nil, fmt.Errorf("could not create client for Google Container Engine: %v", err)
	}

	cluster, err := containerService.Projects.Locations.Clusters.Get(fmt.Sprintf("projects/%s/locations/%s/clusters/%s", opt.Project, location, clusterName)).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("cluster %q could not be found in project %q, location %q: %v", clusterName, opt.Project, location, err)
	}

	// Connect to Kubernetes using OAuth authentication, trusting its CA.
	caPool := x509.NewCertPool()
	caCertPEM, err := base64.StdEncoding.DecodeString(cluster.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 in ClusterCaCertificate: %v", err)
	}
	caPool.AppendCertsFromPEM(caCertPEM)
	kubeHTTPClient := &http.Client{
		Transport: &oauth2.Transport{
			Source: opt.TokenSource,
			Base: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs: caPool,
				},
			},
		},
	}
	kubeClient, err := kubernetes.NewClient("https://"+cluster.Endpoint, opt.Namespace, kubeHTTPClient)
	if err != nil {
		return nil, fmt.Errorf("kubernetes HTTP client could not be created: %v", err)
	}
	return kubeClient, nil
}
