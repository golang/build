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
	monitoring "google.golang.org/api/cloudmonitoring/v2beta2"
	container "google.golang.org/api/container/v1"
	googleapi "google.golang.org/api/googleapi"
)

/*
This file implements the Kubernetes-based buildlet pool.
*/

// Initialized by initKube:
var (
	containerService  *container.Service
	monService        *monitoring.Service
	tsService         *monitoring.TimeseriesService
	metricDescService *monitoring.MetricDescriptorsService
	kubeClient        *kubernetes.Client
	kubeErr           error
	initKubeCalled    bool
	registryPrefix    = "gcr.io"
	kubeCluster       *container.Cluster
)

const (
	clusterName         = "buildlets"
	cpuUsedMetric       = "custom.cloudmonitoring.googleapis.com/cluster/cpu_used"    // % of available CPU in the cluster that is scheduled
	memoryUsedMetric    = "custom.cloudmonitoring.googleapis.com/cluster/memory_used" // % of available memory in the cluster that is scheduled
	serviceLabelKey     = "cloud.googleapis.com/service"                              // allow selection of custom metric based on service name
	clusterNameLabelKey = "custom.cloudmonitoring.googleapis.com/cluster_name"        // allow selection of custom metric based on cluster name
)

// initGCE must be called before initKube
func initKube() error {
	initKubeCalled = true

	// projectID was set by initGCE
	registryPrefix += "/" + buildEnv.ProjectName
	if !hasCloudPlatformScope() {
		return errors.New("coordinator not running with access to the Cloud Platform scope.")
	}
	httpClient := oauth2.NewClient(oauth2.NoContext, tokenSource)
	var err error

	containerService, err = container.New(httpClient)
	if err != nil {
		return fmt.Errorf("could not create client for Google Container Engine: %v", err)
	}

	monService, err = monitoring.New(httpClient)
	if err != nil {
		return fmt.Errorf("could not create client for Google Cloud Monitoring: %v", err)
	}
	tsService = monitoring.NewTimeseriesService(monService)
	metricDescService = monitoring.NewMetricDescriptorsService(monService)

	kubeCluster, err = containerService.Projects.Zones.Clusters.Get(buildEnv.ProjectName, buildEnv.Zone, clusterName).Do()
	if err != nil {
		return fmt.Errorf("cluster %q could not be found in project %q, zone %q: %v", clusterName, buildEnv.ProjectName, buildEnv.Zone, err)
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
	clientCert := decode("client cert", kubeCluster.MasterAuth.ClientCertificate)
	clientKey := decode("client key", kubeCluster.MasterAuth.ClientKey)
	caCert := decode("cluster cert", kubeCluster.MasterAuth.ClusterCaCertificate)
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

	kubeClient, err = kubernetes.NewClient("https://"+kubeCluster.Endpoint, kubeHTTPClient)
	if err != nil {
		return fmt.Errorf("kubernetes HTTP client could not be created: %v", err)
	}

	// Create Google Cloud Monitoring metrics
	tryCreateMetrics()

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

func tryCreateMetrics() {
	metric := &monitoring.MetricDescriptor{
		Description: "Kubernetes Percent CPU Scheduled",
		Name:        cpuUsedMetric,
		Labels: []*monitoring.MetricDescriptorLabelDescriptor{
			{Key: clusterNameLabelKey},
			{Key: serviceLabelKey},
		},
		Project: buildEnv.ProjectName,
		TypeDescriptor: &monitoring.MetricDescriptorTypeDescriptor{
			MetricType: "gauge",
			ValueType:  "double",
		},
	}
	_, err := metricDescService.Create(buildEnv.ProjectName, metric).Do()
	if err != nil {
		if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == 403 {
			log.Printf("Error creating CPU metric: could not authenticate to Google Cloud Monitoring. If you are running the coordinator on a local machine in dev mode, configure service account credentials for authentication as described at https://cloud.google.com/monitoring/api/authentication#service_account_authorization. Error message: %v\n", err)
		} else {
			log.Fatalf("Failed to create CPU metric for project. Ensure the Google Cloud Monitoring API is enabled for project %v: %v.", buildEnv.ProjectName, err)
		}
	}

	metric = &monitoring.MetricDescriptor{
		Description: "Kubernetes Percent Memory Scheduled",
		Name:        memoryUsedMetric,
		Labels: []*monitoring.MetricDescriptorLabelDescriptor{
			{Key: clusterNameLabelKey},
			{Key: serviceLabelKey},
		},
		Project: buildEnv.ProjectName,
		TypeDescriptor: &monitoring.MetricDescriptorTypeDescriptor{
			MetricType: "gauge",
			ValueType:  "double",
		},
	}
	_, err = metricDescService.Create(buildEnv.ProjectName, metric).Do()
	if err != nil {
		if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == 403 {
			log.Printf("Error creating memory metric: could not authenticate to Google Cloud Monitoring. If you are running the coordinator on a local machine in dev mode, configure service account credentials for authentication as described at https://cloud.google.com/monitoring/api/authentication#service_account_authorization. Error message: %v\n", err)
		} else {
			log.Fatalf("Failed to create memory metric for project. Ensure the Google Cloud Monitoring API is enabled for project %v: %v.", buildEnv.ProjectName, err)
		}
	}
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
		log.Printf("failed to retrieve nodes to calculate cluster capacity for %s/%s: %v", buildEnv.ProjectName, buildEnv.Region(), err)
		return
	}
	pods, err := kubeClient.GetPods(ctx)
	if err != nil {
		log.Printf("failed to retrieve pods to calculate cluster capacity for %s/%s: %v", buildEnv.ProjectName, buildEnv.Region(), err)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Calculate the total CPU and memory used by pods in the cluster
	var sumCPUUsed = api.NewQuantity(0, api.DecimalSI)
	var sumMemoryUsed = api.NewQuantity(0, api.BinarySI)
	for _, p := range pods {
		for _, c := range p.Spec.Containers {
			sumCPUUsed.Add(c.Resources.Requests[api.ResourceCPU])
			sumMemoryUsed.Add(c.Resources.Requests[api.ResourceMemory])
		}
	}
	p.cpuUsage = sumCPUUsed
	p.memoryUsage = sumMemoryUsed

	// Calculate the total CPU and memory capacity of the cluster
	var sumCPUCapacity = api.NewQuantity(0, api.DecimalSI)
	var sumMemoryCapacity = api.NewQuantity(0, api.BinarySI)
	for _, n := range nodes {
		sumCPUCapacity.Add(n.Status.Capacity[api.ResourceCPU])
		sumMemoryCapacity.Add(n.Status.Capacity[api.ResourceMemory])
	}
	p.cpuCapacity = sumCPUCapacity
	p.memoryCapacity = sumMemoryCapacity

	// Calculate the % of CPU and memory consumed
	pctCPUConsumed := float64(p.cpuUsage.Value()) / float64(p.cpuCapacity.Value())
	pctMemoryConsumed := float64(p.memoryUsage.Value()) / float64(p.memoryCapacity.Value())
	t := time.Now().Format(time.RFC3339)

	wtr := monitoring.WriteTimeseriesRequest{
		Timeseries: []*monitoring.TimeseriesPoint{
			{
				Point: &monitoring.Point{
					DoubleValue: &pctCPUConsumed,
					Start:       t,
					End:         t,
				},
				TimeseriesDesc: &monitoring.TimeseriesDescriptor{
					Metric:  cpuUsedMetric,
					Project: buildEnv.ProjectName,
					Labels: map[string]string{
						clusterNameLabelKey: clusterName,
						serviceLabelKey:     "container",
					},
				},
			},
			{
				Point: &monitoring.Point{
					DoubleValue: &pctMemoryConsumed,
					Start:       t,
					End:         t,
				},
				TimeseriesDesc: &monitoring.TimeseriesDescriptor{
					Metric:  memoryUsedMetric,
					Project: buildEnv.ProjectName,
					Labels: map[string]string{
						clusterNameLabelKey: clusterName,
						serviceLabelKey:     "container",
					},
				},
			},
		},
	}

	_, err = tsService.Write(buildEnv.ProjectName, &wtr).Do()
	if err != nil {
		log.Printf("custom cluster utilization metric could not be written to Google Cloud Monitoring: %v", err)
	}
}

func (p *kubeBuildletPool) GetBuildlet(ctx context.Context, typ string, el eventTimeLogger) (*buildlet.Client, error) {
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

	deleteIn, ok := ctx.Value(buildletTimeoutOpt{}).(time.Duration)
	if !ok {
		deleteIn = podDeleteTimeout
	}

	podName := "buildlet-" + typ + "-rn" + randHex(7)

	var needDelete bool

	el.logEventTime("creating_kube_pod", podName)
	log.Printf("Creating Kubernetes pod %q for %s", podName, typ)

	bc, err := buildlet.StartPod(ctx, kubeClient, podName, typ, buildlet.PodOpts{
		ProjectID:     buildEnv.ProjectName,
		ImageRegistry: registryPrefix,
		Description:   fmt.Sprintf("Go Builder for %s at %s", typ),
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
