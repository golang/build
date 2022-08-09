// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package metrics provides a service for reporting metrics to
// Stackdriver, or locally during development.
package metrics

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/compute/metadata"
	"contrib.go.opencensus.io/exporter/prometheus"
	"contrib.go.opencensus.io/exporter/stackdriver"
	"go.opencensus.io/stats/view"
	"golang.org/x/build/buildenv"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
)

// NewService initializes a *Service.
//
// The Service returned is configured to send metric data to
// StackDriver. When not running on GCE, it will host metrics through
// a prometheus HTTP handler.
//
// views will be passed to view.Register for export to the metric
// service.
func NewService(resource *MonitoredResource, views []*view.View) (*Service, error) {
	err := view.Register(views...)
	if err != nil {
		return nil, err
	}

	if !metadata.OnGCE() {
		view.SetReportingPeriod(5 * time.Second)
		pe, err := prometheus.NewExporter(prometheus.Options{})
		if err != nil {
			return nil, fmt.Errorf("prometheus.NewExporter: %w", err)
		}
		view.RegisterExporter(pe)
		return &Service{pExporter: pe}, nil
	}

	projID, err := metadata.ProjectID()
	if err != nil {
		return nil, err
	}
	if resource == nil {
		return nil, errors.New("resource is required, got nil")
	}
	sde, err := stackdriver.NewExporter(stackdriver.Options{
		ProjectID:         projID,
		MonitoredResource: resource,
		ReportingInterval: time.Minute, // Minimum interval for Stackdriver is 1 minute.
	})
	if err != nil {
		return nil, err
	}

	// Minimum interval for Stackdriver is 1 minute.
	view.SetReportingPeriod(time.Minute)
	// Start the metrics exporter.
	if err := sde.StartMetricsExporter(); err != nil {
		return nil, err
	}

	return &Service{sdExporter: sde}, nil
}

// Service controls metric exporters.
type Service struct {
	sdExporter *stackdriver.Exporter
	pExporter  *prometheus.Exporter
}

func (m *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.pExporter != nil {
		m.pExporter.ServeHTTP(w, r)
		return
	}
	http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
}

// Stop flushes metrics and stops exporting. Stop should be called
// before exiting.
func (m *Service) Stop() {
	if sde := m.sdExporter; sde != nil {
		// Flush any unsent data before exiting.
		sde.Flush()

		sde.StopMetricsExporter()
	}
}

// MonitoredResource wraps a *mrpb.MonitoredResource to implement the
// monitoredresource.MonitoredResource interface.
type MonitoredResource mrpb.MonitoredResource

func (r *MonitoredResource) MonitoredResource() (resType string, labels map[string]string) {
	return r.Type, r.Labels
}

// GKEResource populates a MonitoredResource with GKE Metadata.
//
// The returned MonitoredResource will have the type set to "k8s_container".
func GKEResource(containerName string) (*MonitoredResource, error) {
	projID, err := metadata.ProjectID()
	if err != nil {
		return nil, err
	}
	// https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#gke_mds
	location, err := metadata.InstanceAttributeValue("cluster-location")
	if err != nil {
		return nil, err
	}
	// https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity#gke_mds
	clusterName, err := metadata.InstanceAttributeValue("cluster-name")
	if err != nil {
		return nil, err
	}
	podName, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	return (*MonitoredResource)(&mrpb.MonitoredResource{
		Type: "k8s_container", // See: https://cloud.google.com/monitoring/api/resources#tag_k8s_container
		Labels: map[string]string{
			"project_id":     projID,
			"location":       location,
			"cluster_name":   clusterName,
			"namespace_name": buildenv.ByProjectID(projID).KubeServices.Namespace,
			"pod_name":       podName,
			"container_name": containerName,
		},
	}), nil
}
