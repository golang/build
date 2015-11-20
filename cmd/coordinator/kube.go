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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/kubernetes"
	"golang.org/x/build/kubernetes/api"
	"golang.org/x/net/context"
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
	containerService, err = container.New(httpClient)
	if err != nil {
		return fmt.Errorf("could not create client for Google Container Engine")
	}

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

	go kubePool.pollCapacityLoop()
	return nil
}

var kubePool = &kubeBuildletPool{
	cpuCapacity:    api.NewQuantity(0, api.DecimalSI),
	cpuUsage:       api.NewQuantity(0, api.DecimalSI),
	memoryCapacity: api.NewQuantity(0, api.BinarySI),
	memoryUsage:    api.NewQuantity(0, api.BinarySI),
}

// kubeBuildletPool is the Kubernetes buildlet pool.
type kubeBuildletPool struct {
	mu sync.Mutex // guards all following

	pods           map[string]time.Time // pod instance name -> creationTime
	cpuCapacity    *api.Quantity        // cpu capacity as reported by the Kubernetes api
	memoryCapacity *api.Quantity
	cpuUsage       *api.Quantity
	memoryUsage    *api.Quantity
}

func (p *kubeBuildletPool) pollCapacityLoop() {
	ctx := context.Background()
	for {
		p.pollCapacity(ctx)
		time.Sleep(30 * time.Second)
	}
}

func (p *kubeBuildletPool) pollCapacity(ctx context.Context) {
	nodes, err := kubeClient.GetNodes(ctx)
	if err != nil {
		log.Printf("Failed to get Kubernetes cluster capacity for %s/%s: %v", projectID, projectRegion, err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	// Calculate the total CPU and memory capacity of the cluster
	var sumCPU = api.NewQuantity(0, api.DecimalSI)
	var sumMemory = api.NewQuantity(0, api.BinarySI)
	for _, n := range nodes {
		sumCPU.Add(n.Status.Capacity[api.ResourceCPU])
		sumMemory.Add(n.Status.Capacity[api.ResourceMemory])
	}
	p.cpuCapacity = sumCPU
	p.memoryCapacity = sumMemory
}

func (p *kubeBuildletPool) GetBuildlet(ctx context.Context, typ, rev string, el eventTimeLogger) (*buildlet.Client, error) {
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
	log.Printf("Creating Kubernetes pod %q for %s at %s", podName, typ, rev)

	bc, err := buildlet.StartPod(ctx, kubeClient, podName, typ, buildlet.PodOpts{
		ImageRegistry: registryPrefix,
		Description:   fmt.Sprintf("Go Builder for %s at %s", typ, rev),
		DeleteIn:      deleteIn,
		OnPodCreated: func() {
			el.logEventTime("pod_created")
			p.setPodUsed(podName, true)
			needDelete = true
		},
		OnGotPodInfo: func() {
			el.logEventTime("got_pod_info", "waiting_for_buildlet...")
		},
	})
	if err != nil {
		el.logEventTime("kube_buildlet_create_failure", fmt.Sprintf("%s: %v", podName, err))

		if needDelete {
			log.Printf("Deleting failed pod %q", podName)
			kubeClient.DeletePod(ctx, podName)
			p.setPodUsed(podName, false)
		}
		return nil, err
	}

	bc.SetDescription("Kube Pod: " + podName)

	// The build's context will be canceled when the build completes (successfully
	// or not), or if the buildlet becomes unavailable. In any case, delete the pod
	// running the buildlet.
	go func() {
		<-ctx.Done()
		log.Printf("Deleting pod %q after build context cancel received ", podName)
		// Giving DeletePod a new context here as the build ctx has been canceled
		kubeClient.DeletePod(context.Background(), podName)
		p.setPodUsed(podName, false)
	}()

	return bc, nil
}

func (p *kubeBuildletPool) WriteHTMLStatus(w io.Writer) {
	fmt.Fprintf(w, "<b>Kubernetes pool</b> capacity: %s", p.capacityString())
	const show = 6 // must be even
	active := p.podsActive()
	if len(active) > 0 {
		fmt.Fprintf(w, "<ul>")
		for i, pod := range active {
			if i < show/2 || i >= len(active)-(show/2) {
				fmt.Fprintf(w, "<li>%v, %v</li>\n", pod.name, time.Since(pod.creation))
			} else if i == show/2 {
				fmt.Fprintf(w, "<li>... %d of %d total omitted ...</li>\n", len(active)-show, len(active))
			}
		}
		fmt.Fprintf(w, "</ul>")
	}
}

func (p *kubeBuildletPool) capacityString() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("%v/%v CPUs; %v/%v Memory",
		p.cpuUsage, p.cpuCapacity,
		p.memoryUsage, p.memoryCapacity)
}

func (p *kubeBuildletPool) setPodUsed(podName string, used bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pods == nil {
		p.pods = make(map[string]time.Time)
	}
	if used {
		p.pods[podName] = time.Now()
		// Track cpu and memory usage
		p.cpuUsage.Add(buildlet.BuildletCPU)
		p.memoryUsage.Add(buildlet.BuildletMemory)

	} else {
		delete(p.pods, podName)
		// Track cpu and memory usage
		p.cpuUsage.Sub(buildlet.BuildletCPU)
		p.memoryUsage.Sub(buildlet.BuildletMemory)

	}
}

func (p *kubeBuildletPool) podUsed(podName string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.pods[podName]
	return ok
}

func (p *kubeBuildletPool) podsActive() (ret []resourceTime) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, create := range p.pods {
		ret = append(ret, resourceTime{
			name:     name,
			creation: create,
		})
	}
	sort.Sort(byCreationTime(ret))
	return ret
}

func (p *kubeBuildletPool) String() string {
	p.mu.Lock()
	inUse := 0
	total := 0
	// ...
	p.mu.Unlock()
	return fmt.Sprintf("Kubernetes pool capacity: %d/%d", inUse, total)
}

// cleanUpOldPods loops forever and periodically enumerates pods
// and deletes those which have expired.
//
// A Pod is considered expired if it has a "delete-at" metadata
// attribute having a unix timestamp before the current time.
//
// This is the safety mechanism to delete pods which stray from the
// normal deleting process. Pods are created to run a single build and
// should be shut down by a controlling process. Due to various types
// of failures, they might get stranded. To prevent them from getting
// stranded and wasting resources forever, we instead set the
// "delete-at" metadata attribute on them when created to some time
// that's well beyond their expected lifetime.
func (p *kubeBuildletPool) cleanUpOldPods(ctx context.Context) {
	if containerService == nil {
		return
	}
	for {
		pods, err := kubeClient.GetPods(ctx)
		if err != nil {
			log.Printf("Error cleaning pods: %v", err)
			return
		}
		for _, pod := range pods {
			if pod.ObjectMeta.Annotations == nil {
				// Defensive. Not seen in practice.
				continue
			}
			sawDeleteAt := false
			for k, v := range pod.ObjectMeta.Annotations {
				if k == "delete-at" {
					sawDeleteAt = true
					if v == "" {
						log.Printf("missing delete-at value; ignoring")
						continue
					}
					unixDeadline, err := strconv.ParseInt(v, 10, 64)
					if err != nil {
						log.Printf("invalid delete-at value %q seen; ignoring", v)
					}
					if err == nil && time.Now().Unix() > unixDeadline {
						log.Printf("Deleting expired pod %q in zone %q ...", pod.Name)
						err = kubeClient.DeletePod(ctx, pod.Name)
						if err != nil {
							log.Printf("problem deleting pod: %v", err)
						}
					}
				}
			}
			// Delete buildlets (things we made) from previous
			// generations. Only deleting things starting with "buildlet-"
			// is a historical restriction, but still fine for paranoia.
			if sawDeleteAt && strings.HasPrefix(pod.Name, "buildlet-") && !p.podUsed(pod.Name) {
				log.Printf("Deleting pod %q from an earlier coordinator generation ...", pod.Name)
				err = kubeClient.DeletePod(ctx, pod.Name)
				if err != nil {
					log.Printf("problem deleting pod: %v", err)
				}
			}
		}
		time.Sleep(time.Minute)
	}
}

func hasCloudPlatformScope() bool {
	return hasScope(container.CloudPlatformScope)
}
