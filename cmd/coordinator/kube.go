// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/kubernetes"
	"golang.org/x/oauth2"
	container "google.golang.org/api/container/v1"
)

/*
This file implements the Kubernetes-based buildlet pool.
*/

// Initialized by initKube:
var (
	containerService *container.Service
	kubeClient       *kubernetes.Client
	kubeErr          error
	initKubeCalled   bool
	registryPrefix   = "gcr.io"
)

const (
	clusterName = "buildlets"
)

// initGCE must be called before initKube
func initKube() error {
	initKubeCalled = true

	// projectID was set by initGCE
	registryPrefix += "/" + projectID
	if !hasCloudPlatformScope() {
		return errors.New("coordinator not running with access to the Cloud Platform scope.")

	}
	httpClient := oauth2.NewClient(oauth2.NoContext, tokenSource)
	containerService, _ = container.New(httpClient)

	cluster, err := containerService.Projects.Zones.Clusters.Get(projectID, projectZone, clusterName).Do()
	if err != nil {
		return fmt.Errorf("cluster %q could not be found in project %q, zone %q: %v", clusterName, projectID, projectZone, err)
	}

	// Decode certs
	decode := func(which string, cert string) []byte {
		if err != nil {
			return nil
		}
		s, decErr := base64.StdEncoding.DecodeString(cert)
		if decErr != nil {
			err = fmt.Errorf("error decoding %s cert: %v", which, decErr)
		}
		return []byte(s)
	}
	clientCert := decode("client cert", cluster.MasterAuth.ClientCertificate)
	clientKey := decode("client key", cluster.MasterAuth.ClientKey)
	caCert := decode("cluster cert", cluster.MasterAuth.ClusterCaCertificate)
	if err != nil {
		return err
	}

	// HTTPS client
	cert, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		return fmt.Errorf("x509 client key pair could not be generated: %v", err)
	}

	// CA Cert from kube master
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM([]byte(caCert))

	// Setup TLS config
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}
	tlsConfig.BuildNameToCertificate()

	kubeHTTPClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	kubeClient, err = kubernetes.NewClient("https://"+cluster.Endpoint, kubeHTTPClient)
	if err != nil {
		return fmt.Errorf("kubernetes HTTP client could not be created: %v", err)
	}
	return nil
}

var kubePool = &kubeBuildletPool{}

// kubeBuildletPool is the Kubernetes buildlet pool.
type kubeBuildletPool struct {
	// ...
	mu sync.Mutex
}

func (p *kubeBuildletPool) GetBuildlet(cancel Cancel, typ, rev string, el eventTimeLogger) (*buildlet.Client, error) {
	conf, ok := dashboard.Builders[typ]
	if !ok || conf.KubeImage == "" {
		return nil, fmt.Errorf("kubepool: invalid builder type %q", typ)
	}
	if kubeErr != nil {
		return nil, kubeErr
	}
	if kubeClient == nil {
		panic("expect non-nil kubeClient")
	}

	deleteIn := podDeleteTimeout
	if strings.HasPrefix(rev, "user-") {
		// Created by gomote (see remote.go), so don't kill it in 45 minutes.
		// remote.go handles timeouts itself.
		deleteIn = 0
		rev = strings.TrimPrefix(rev, "user-")
	}

	// name is the cluster-wide unique name of the kubernetes pod. Max length
	// is not documented, but it's kept <= 61 bytes, in line with GCE
	revPrefix := rev
	if len(revPrefix) > 8 {
		revPrefix = rev[:8]
	}

	podName := "buildlet-" + typ + "-" + revPrefix + "-rn" + randHex(6)

	var needDelete bool

	el.logEventTime("creating_kube_pod", podName)
	log.Printf("Creating Kubernetes  pod %q for %s at %s", podName, typ, rev)

	bc, err := buildlet.StartPod(kubeClient, podName, typ, buildlet.PodOpts{
		ImageRegistry: registryPrefix,
		Description:   fmt.Sprintf("Go Builder for %s at %s", typ, rev),
		DeleteIn:      deleteIn,
		OnPodRequested: func() {
			el.logEventTime("pod_create_requested", podName)
			log.Printf("Pod %q starting", podName)
		},
		OnPodCreated: func() {
			el.logEventTime("pod_created")
			needDelete = true // redundant with OnPodRequested one, but fine.
		},
		OnGotPodInfo: func() {
			el.logEventTime("got_pod_info", "waiting_for_buildlet...")
		},
	})
	if err != nil {
		el.logEventTime("kube_buildlet_create_failure", fmt.Sprintf("%s: %v", podName, err))
		log.Printf("Failed to create kube pod for %s, %s: %v", typ, rev, err)
		if needDelete {
			//TODO(evanbrown): delete pod
		}
		//p.setInstanceUsed(instName, false)
		return nil, err
	}
	bc.SetDescription("Kube Pod: " + podName)
	return bc, nil
}

func (p *kubeBuildletPool) WriteHTMLStatus(w io.Writer) {
	io.WriteString(w, "<b>Kubernetes pool summary</b><ul><li>(TODO)</li></ul>")
}

func (p *kubeBuildletPool) String() string {
	p.mu.Lock()
	inUse := 0
	total := 0
	// ...
	p.mu.Unlock()
	return fmt.Sprintf("Kubernetes pool capacity: %d/%d", inUse, total)
}

func hasCloudPlatformScope() bool {
	return hasScope(container.CloudPlatformScope)
}
