// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gke_test

import (
	"context"
	"strings"
	"testing"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/build/kubernetes"
	"golang.org/x/build/kubernetes/gke"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	container "google.golang.org/api/container/v1"
)

// Tests NewClient and also Dialer.
func TestNewClient(t *testing.T) {
	if !metadata.OnGCE() {
		t.Skip("not on GCE; skipping")
	}
	ctx := context.Background()
	ts, err := google.DefaultTokenSource(ctx, compute.CloudPlatformScope)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := oauth2.NewClient(ctx, ts)
	containerService, err := container.New(httpClient)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := metadata.ProjectID()
	if err != nil {
		t.Fatal(err)
	}

	clusters, err := containerService.Projects.Zones.Clusters.List(proj, "-").Context(ctx).Do()
	if err != nil {
		t.Fatal(err)
	}

	if len(clusters.Clusters) == 0 {
		t.Skip("no GKE clusters")
	}
	var candidates int
	for _, cl := range clusters.Clusters {
		kc, err := gke.NewClient(ctx, cl.Name, gke.OptZone(cl.Zone))
		if err != nil {
			t.Fatal(err)
		}
		defer kc.Close()

		pods, err := kc.GetPods(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, pod := range pods {
			if pod.Status.Phase != "Running" {
				continue
			}
			for _, container := range pod.Spec.Containers {
				name := container.Name
				for _, port := range container.Ports {
					if strings.ToLower(string(port.Protocol)) == "udp" || port.ContainerPort == 0 {
						continue
					}
					candidates++
					d := kubernetes.NewDialer(kc)
					c, err := d.Dial(ctx, name, port.ContainerPort)
					if err != nil {
						t.Logf("Dial %q/%q/%d: %v", cl.Name, name, port.ContainerPort, err)
						continue
					}
					c.Close()
					t.Logf("Dialed %q/%q/%d.", cl.Name, name, port.ContainerPort)
					return
				}
			}
		}
	}
	if candidates == 0 {
		t.Skip("no pods to dial")
	}
	t.Errorf("dial failures")
}
