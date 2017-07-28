// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main // import "golang.org/x/build/cmd/coordinator/buildongce"

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	monapi "cloud.google.com/go/monitoring/apiv3"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/cmd/coordinator/metrics"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	dm "google.golang.org/api/deploymentmanager/v2"
	monpb "google.golang.org/genproto/googleapis/monitoring/v3"
)

var (
	proj = flag.String("project", "", "Optional name of the Google Cloud Platform project to create the infrastructure in. If empty, the project defined in golang.org/x/build/buildenv is used, for either production or staging (if the -staging flag is used)")

	staging      = flag.Bool("staging", false, "If true, buildenv.Staging will be used to provide default configuration values. Otherwise, buildenv.Production is used.")
	makeClusters = flag.String("make-clusters", "go,buildlets", "comma-separated list of clusters to create. Empty means none.")
	makeDisks    = flag.Bool("make-basepin", false, "Create the basepin disk images for all builders, then stop. Does not create the VM.")
	makeMetrics  = flag.Bool("make-metrics", false, "Create the Stackdriver metrics for buildlet monitoring.")

	computeService    *compute.Service
	deploymentService *dm.Service
	oauthClient       *http.Client
	err               error
	buildEnv          *buildenv.Environment
)

// Deployment Manager V2 manifest for creating a Google Container Engine
// cluster to run buildlets, as well as an autoscaler attached to the
// cluster's instance group to add capacity based on CPU utilization
const kubeConfig = `
resources:
- name: "{{ .Kube.Name }}"
  type: container.v1.cluster
  properties:
    zone: "{{ .Env.Zone }}"
    cluster:
      initial_node_count: {{ .Kube.MinNodes }}
      network: "default"
      logging_service: "logging.googleapis.com"
      monitoring_service: "none"
      node_config:
        machine_type: "{{ .Kube.MachineType }}"
        oauth_scopes:
          - "https://www.googleapis.com/auth/cloud-platform"
          - "https://www.googleapis.com/auth/userinfo.email"
      master_auth:
        username: "admin"
        password: "{{ .Password }}"`

// Old autoscaler part:
/*
`
- name: autoscaler
  type: compute.v1.autoscaler
  properties:
    zone: "{{ .Zone }}"
    name: "{{ .KubeName }}"
    target: "$(ref.{{ .KubeName }}.instanceGroupUrls[0])"
    autoscalingPolicy:
      minNumReplicas: {{ .KubeMinNodes }}
      maxNumReplicas: {{ .KubeMaxNodes }}
      coolDownPeriodSec: 1200
      cpuUtilization:
        utilizationTarget: .6`
*/

func main() {
	buildEnv = buildenv.Production

	flag.Parse()

	if *staging {
		buildEnv = buildenv.Staging
	}
	if *proj != "" {
		buildEnv.ProjectName = *proj
	}

	// Brad is sick of google.DefaultClient giving him the
	// permissions from the instance via the metadata service. Use
	// the service account from disk if it exists instead:
	keyFile := filepath.Join(os.Getenv("HOME"), "keys", buildEnv.ProjectName+".key.json")
	if _, err := os.Stat(keyFile); err == nil {
		log.Printf("Using service account from %s", keyFile)
		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", keyFile)
	}

	oauthClient, err = google.DefaultClient(context.Background(), compute.CloudPlatformScope, compute.ComputeScope, compute.DevstorageFullControlScope)
	if err != nil {
		log.Fatalf("could not create oAuth client: %v", err)
	}

	computeService, err = compute.New(oauthClient)
	if err != nil {
		log.Fatalf("could not create client for Google Compute Engine: %v", err)
	}

	if *makeDisks {
		if err := makeBasepinDisks(computeService); err != nil {
			log.Fatalf("could not create basepin disks: %v", err)
		}
		return
	}

	for _, c := range []*buildenv.KubeConfig{&buildEnv.KubeBuild, &buildEnv.KubeTools} {
		err := createCluster(c)
		if err != nil {
			log.Fatalf("Error creating Kubernetes cluster %q: %v", c.Name, err)
		}
	}

	if *makeMetrics {
		if err := createMetrics(); err != nil {
			log.Fatalf("could not create metrics: %v", err)
		}
	}
}

func awaitOp(svc *compute.Service, op *compute.Operation) error {
	opName := op.Name
	log.Printf("Waiting on operation %v", opName)
	for {
		time.Sleep(2 * time.Second)
		op, err := svc.ZoneOperations.Get(buildEnv.ProjectName, buildEnv.Zone, opName).Do()
		if err != nil {
			return fmt.Errorf("Failed to get op %s: %v", opName, err)
		}
		switch op.Status {
		case "PENDING", "RUNNING":
			log.Printf("Waiting on operation %v", opName)
			continue
		case "DONE":
			if op.Error != nil {
				var last error
				for _, operr := range op.Error.Errors {
					log.Printf("Error: %+v", operr)
					last = fmt.Errorf("%v", operr)
				}
				return last
			}
			log.Printf("Success. %+v", op)
			return nil
		default:
			return fmt.Errorf("Unknown status %q: %+v", op.Status, op)
		}
	}
}

type deploymentTemplateData struct {
	Env      *buildenv.Environment
	Kube     *buildenv.KubeConfig
	Password string
}

func wantClusterCreate(name string) bool {
	for _, want := range strings.Split(*makeClusters, ",") {
		if want == name {
			return true
		}
	}
	return false
}

func createCluster(kube *buildenv.KubeConfig) error {
	if !wantClusterCreate(kube.Name) {
		log.Printf("skipping kubernetes cluster %q per flag", kube.Name)
		return nil
	}
	log.Printf("Creating Kubernetes cluster: %v", kube.Name)
	deploymentService, err = dm.New(oauthClient)
	if err != nil {
		return fmt.Errorf("could not create client for Google Cloud Deployment Manager: %v", err)
	}

	if kube.MaxNodes == 0 || kube.MinNodes == 0 {
		return fmt.Errorf("MaxNodes/MinNodes values cannot be 0")
	}

	tpl, err := template.New("kube").Parse(kubeConfig)
	if err != nil {
		return fmt.Errorf("could not parse Deployment Manager template: %v", err)
	}

	var result bytes.Buffer
	err = tpl.Execute(&result, deploymentTemplateData{
		Env:      buildEnv,
		Kube:     kube,
		Password: randomPassword(),
	})
	if err != nil {
		return fmt.Errorf("could not execute Deployment Manager template: %v", err)
	}

	deployment := &dm.Deployment{
		Name: kube.Name,
		Target: &dm.TargetConfiguration{
			Config: &dm.ConfigFile{
				Content: result.String(),
			},
		},
	}
	op, err := deploymentService.Deployments.Insert(buildEnv.ProjectName, deployment).Do()
	if err != nil {
		return fmt.Errorf("Failed to create cluster with Deployment Manager: %v", err)
	}
	opName := op.Name
	log.Printf("Created. Waiting on operation %v", opName)
OpLoop:
	for {
		time.Sleep(2 * time.Second)
		op, err := deploymentService.Operations.Get(buildEnv.ProjectName, opName).Do()
		if err != nil {
			return fmt.Errorf("Failed to get op %s: %v", opName, err)
		}
		switch op.Status {
		case "PENDING", "RUNNING":
			log.Printf("Waiting on operation %v", opName)
			continue
		case "DONE":
			// If no errors occurred, op.StatusMessage is empty.
			if op.StatusMessage != "" {
				log.Printf("Error: %+v", op.StatusMessage)
				return fmt.Errorf("Failed to create.")
			}
			log.Printf("Success.")
			break OpLoop
		default:
			return fmt.Errorf("Unknown status %q: %+v", op.Status, op)
		}
	}
	return nil
}

func randomPassword() string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("randomPassword: %v", err)
	}
	return fmt.Sprintf("%x", buf)
}

func makeBasepinDisks(svc *compute.Service) error {
	// Try to find it by name.
	imList, err := svc.Images.List(buildEnv.ProjectName).Do()
	if err != nil {
		return fmt.Errorf("Error listing images for %s: %v", buildEnv.ProjectName, err)
	}
	if imList.NextPageToken != "" {
		return errors.New("too many images; pagination not supported")
	}
	diskList, err := svc.Disks.List(buildEnv.ProjectName, buildEnv.Zone).Do()
	if err != nil {
		return err
	}
	if diskList.NextPageToken != "" {
		return errors.New("too many disks; pagination not supported (yet?)")
	}

	need := make(map[string]*compute.Image) // keys like "https://www.googleapis.com/compute/v1/projects/symbolic-datum-552/global/images/linux-buildlet-arm"
	for _, im := range imList.Items {
		if strings.Contains(im.SelfLink, "-debug") {
			continue
		}
		need[im.SelfLink] = im
	}

	for _, d := range diskList.Items {
		if !strings.HasPrefix(d.Name, "basepin-") {
			continue
		}
		if si, ok := need[d.SourceImage]; ok && d.SourceImageId == fmt.Sprint(si.Id) {
			log.Printf("Have %s: %s (%v)\n", d.Name, d.SourceImage, d.SourceImageId)
			delete(need, d.SourceImage)
		}
	}

	var needed []string
	for imageName := range need {
		needed = append(needed, imageName)
	}
	sort.Strings(needed)
	for _, n := range needed {
		log.Printf("Need %v", n)
	}
	for i, imName := range needed {
		im := need[imName]
		log.Printf("(%d/%d) Creating %s ...", i+1, len(needed), im.Name)
		op, err := svc.Disks.Insert(buildEnv.ProjectName, buildEnv.Zone, &compute.Disk{
			Description:   "zone-cached basepin image of " + im.Name,
			Name:          "basepin-" + im.Name + "-" + fmt.Sprint(im.Id),
			SizeGb:        im.DiskSizeGb,
			SourceImage:   im.SelfLink,
			SourceImageId: fmt.Sprint(im.Id),
			Type:          "https://www.googleapis.com/compute/v1/projects/" + buildEnv.ProjectName + "/zones/" + buildEnv.Zone + "/diskTypes/pd-ssd",
		}).Do()
		if err != nil {
			return err
		}
		if err := awaitOp(svc, op); err != nil {
			log.Fatalf("failed to create: %v", err)
		}
	}
	return nil
}

// createMetrics creates the Stackdriver metric types required to monitor
// buildlets on Stackdriver.
func createMetrics() error {
	ctx := context.Background()
	c, err := monapi.NewMetricClient(ctx)
	if err != nil {
		return err
	}

	for _, m := range metrics.Metrics {
		if _, err = c.CreateMetricDescriptor(ctx, &monpb.CreateMetricDescriptorRequest{
			Name:             m.DescriptorPath(buildEnv.ProjectName),
			MetricDescriptor: m.Descriptor,
		}); err != nil {
			return err
		}
	}

	return nil
}
