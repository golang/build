// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package buildenv contains definitions for the
// environments the Go build system can run in.
package buildenv

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	oauth2api "google.golang.org/api/oauth2/v2"
)

const (
	prefix = "https://www.googleapis.com/compute/v1/projects/"
)

// KubeConfig describes the configuration of a Kubernetes cluster.
type KubeConfig struct {
	// The zone of the cluster. Autopilot clusters have no single zone.
	Zone string

	// The region of the cluster.
	Region string

	// Name is the name of the Kubernetes cluster that will be used.
	Name string

	// Namespace is the Kubernetes namespace to use within the cluster.
	Namespace string
}

// Location returns the zone or if unset, the region of the cluster.
// This is the string to use as the "zone" of the cluster when connecting to it
// with kubectl.
func (kc KubeConfig) Location() string {
	if kc.Zone != "" {
		return kc.Zone
	}
	if kc.Region != "" {
		return kc.Region
	}
	panic(fmt.Sprintf("KubeConfig has neither zone nor region: %#v", kc))
}

// Environment describes the configuration of the infrastructure for a
// coordinator and its buildlet resources running on Google Cloud Platform.
// Staging and Production are the two common build environments.
type Environment struct {
	// The GCP project name that the build infrastructure will be provisioned in.
	// This field may be overridden as necessary without impacting other fields.
	ProjectName string

	// ProjectNumber is the GCP build infrastructure project's number, as visible
	// in the admin console. This is used for things such as constructing the
	// "email" of the default service account.
	ProjectNumber int64

	// The GCP project name for the Go project, where build status is stored.
	// This field may be overridden as necessary without impacting other fields.
	GoProjectName string

	// The IsProd flag indicates whether production functionality should be
	// enabled. When true, GCE and Kubernetes builders are enabled and the
	// coordinator serves on 443. Otherwise, GCE and Kubernetes builders are
	// disabled and the coordinator serves on 8119.
	IsProd bool

	// VMRegion is the region we deploy build VMs to.
	VMRegion string

	// VMZones are the GCE zones that the VMs will be deployed to. These
	// GCE zones will be periodically cleaned by deleting old VMs. The zones
	// should all exist within VMRegion.
	VMZones []string

	// StaticIP is the public, static IP address that will be attached to the
	// coordinator instance. The zero value means the address will be looked
	// up by name. This field is optional.
	StaticIP string

	// KubeServices is the cluster that runs the coordinator and other services.
	KubeServices KubeConfig

	// DashURL is the base URL of the build dashboard, ending in a slash.
	DashURL string

	// PerfDataURL is the base URL of the benchmark storage server.
	PerfDataURL string

	// CoordinatorName is the hostname of the coordinator instance.
	CoordinatorName string

	// BuildletBucket is the GCS bucket that stores buildlet binaries.
	// TODO: rename. this is not just for buildlets; also for bootstrap.
	BuildletBucket string

	// LogBucket is the GCS bucket to which logs are written.
	LogBucket string

	// SnapBucket is the GCS bucket to which snapshots of
	// completed builds (after make.bash, before tests) are
	// written.
	SnapBucket string

	// MaxBuilds is the maximum number of concurrent builds that
	// can run. Zero means unlimited. This is typically only used
	// in a development or staging environment.
	MaxBuilds int

	// COSServiceAccount (Container Optimized OS) is the service
	// account that will be assigned to a VM instance that hosts
	// a container when the instance is created.
	COSServiceAccount string

	// AWSSecurityGroup is the security group name that any VM instance
	// created on EC2 should contain. These security groups are
	// collections of firewall rules to be applied to the VM.
	AWSSecurityGroup string

	// AWSRegion is the region where AWS resources are deployed.
	AWSRegion string

	// iapServiceIDs is a map of service-backends to service IDs for the backend
	// services used by IAP enabled HTTP paths.
	// map[backend-service-name]service_id
	iapServiceIDs map[string]string

	// GomoteTransferBucket is the bucket used by the gomote GRPC service
	// to transfer files between gomote clients and the gomote instances.
	GomoteTransferBucket string
}

// ComputePrefix returns the URI prefix for Compute Engine resources in a project.
func (e Environment) ComputePrefix() string {
	return prefix + e.ProjectName
}

// RandomVMZone returns a randomly selected zone from the zones in VMZones.
func (e Environment) RandomVMZone() string {
	return e.VMZones[rand.Intn(len(e.VMZones))]
}

// SnapshotURL returns the absolute URL of the .tar.gz containing a
// built Go tree for the builderType and Go rev (40 character Git
// commit hash). The tarball is suitable for passing to
// (buildlet.Client).PutTarFromURL.
func (e Environment) SnapshotURL(builderType, rev string) string {
	return fmt.Sprintf("https://storage.googleapis.com/%s/go/%s/%s.tar.gz", e.SnapBucket, builderType, rev)
}

// DashBase returns the base URL of the build dashboard, ending in a slash.
func (e Environment) DashBase() string {
	// TODO(quentin): Should we really default to production? That's what the old code did.
	if e.DashURL != "" {
		return e.DashURL
	}
	return Production.DashURL
}

// Credentials returns the credentials required to access the GCP environment
// with the necessary scopes.
func (e Environment) Credentials(ctx context.Context) (*google.Credentials, error) {
	// TODO: this method used to do much more. maybe remove it
	// when TODO below is addressed, pushing scopes to caller? Or
	// add a Scopes func/method somewhere instead?
	scopes := []string{
		// Cloud Platform should include all others, but the
		// old code duplicated compute and the storage full
		// control scopes, so I leave them here for now. They
		// predated the all-encompassing "cloud platform"
		// scope anyway.
		// TODO: remove compute and DevstorageFullControlScope once verified to work
		// without.
		compute.CloudPlatformScope,
		compute.ComputeScope,
		compute.DevstorageFullControlScope,

		// The coordinator needed the userinfo email scope for
		// reporting to the perf dashboard running on App
		// Engine at one point. The perf dashboard is down at
		// the moment, but when it's back up we'll need this,
		// and if we do other authenticated requests to App
		// Engine apps, this would be useful.
		oauth2api.UserinfoEmailScope,
	}
	creds, err := google.FindDefaultCredentials(ctx, scopes...)
	if err != nil {
		CheckUserCredentials()
		return nil, err
	}
	creds.TokenSource = diagnoseFailureTokenSource{creds.TokenSource}
	return creds, nil
}

// IAPServiceID returns the service id for the backend service. If a path does not exist for a
// backend, the service id will be an empty string.
func (e Environment) IAPServiceID(backendServiceName string) string {
	if v, ok := e.iapServiceIDs[backendServiceName]; ok {
		return v
	}
	return ""
}

// ByProjectID returns an Environment for the specified
// project ID. It is currently limited to the symbolic-datum-552
// and go-dashboard-dev projects.
// ByProjectID will panic if the project ID is not known.
func ByProjectID(projectID string) *Environment {
	var envKeys []string

	for k := range possibleEnvs {
		envKeys = append(envKeys, k)
	}

	var env *Environment
	env, ok := possibleEnvs[projectID]
	if !ok {
		panic(fmt.Sprintf("Can't get buildenv for unknown project %q. Possible envs are %s", projectID, envKeys))
	}

	return env
}

// Staging defines the environment that the coordinator and build
// infrastructure is deployed to before it is released to production.
// For local dev, override the project with the program's flag to set
// a custom project.
var Staging = &Environment{
	ProjectName:   "go-dashboard-dev",
	ProjectNumber: 302018677728,
	GoProjectName: "go-dashboard-dev",
	IsProd:        true,
	VMRegion:      "us-central1",
	VMZones:       []string{"us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"},
	StaticIP:      "104.154.113.235",
	KubeServices: KubeConfig{
		Zone:      "us-central1-f",
		Region:    "us-central1",
		Name:      "go",
		Namespace: "default",
	},
	DashURL:           "https://build-staging.golang.org/",
	PerfDataURL:       "https://perfdata.golang.org",
	CoordinatorName:   "farmer",
	BuildletBucket:    "dev-go-builder-data",
	LogBucket:         "dev-go-build-log",
	SnapBucket:        "dev-go-build-snap",
	COSServiceAccount: "linux-cos-builders@go-dashboard-dev.iam.gserviceaccount.com",
	AWSSecurityGroup:  "staging-go-builders",
	AWSRegion:         "us-east-1",
	iapServiceIDs:     map[string]string{},
}

// Production defines the environment that the coordinator and build
// infrastructure is deployed to for production usage at build.golang.org.
var Production = &Environment{
	ProjectName:   "symbolic-datum-552",
	ProjectNumber: 872405196845,
	GoProjectName: "golang-org",
	IsProd:        true,
	VMRegion:      "us-central1",
	VMZones:       []string{"us-central1-a", "us-central1-b", "us-central1-f"},
	StaticIP:      "107.178.219.46",
	KubeServices: KubeConfig{
		Region:    "us-central1",
		Name:      "services",
		Namespace: "prod",
	},
	DashURL:           "https://build.golang.org/",
	PerfDataURL:       "https://perfdata.golang.org",
	CoordinatorName:   "farmer",
	BuildletBucket:    "go-builder-data",
	LogBucket:         "go-build-log",
	SnapBucket:        "go-build-snap",
	COSServiceAccount: "linux-cos-builders@symbolic-datum-552.iam.gserviceaccount.com",
	AWSSecurityGroup:  "go-builders",
	AWSRegion:         "us-east-2",
	iapServiceIDs: map[string]string{
		"coordinator-internal-iap": "7963570695201399464",
		"relui-internal":           "155577380958854618",
	},
	GomoteTransferBucket: "gomote-transfer",
}

var LUCIProduction = &Environment{
	ProjectName:   "golang-ci-luci",
	ProjectNumber: 257595674695,
	IsProd:        true,
}

var Development = &Environment{
	GoProjectName: "golang-org",
	IsProd:        false,
	StaticIP:      "127.0.0.1",
	PerfDataURL:   "http://localhost:8081",
}

// possibleEnvs enumerate the known buildenv.Environment definitions.
var possibleEnvs = map[string]*Environment{
	"dev":                Development,
	"symbolic-datum-552": Production,
	"go-dashboard-dev":   Staging,
}

var (
	stagingFlag     bool
	localDevFlag    bool
	registeredFlags bool
)

// RegisterFlags registers the "staging" and "localdev" flags.
func RegisterFlags() {
	if registeredFlags {
		panic("duplicate call to RegisterFlags or RegisterStagingFlag")
	}
	flag.BoolVar(&localDevFlag, "localdev", false, "use the localhost in-development coordinator")
	RegisterStagingFlag()
	registeredFlags = true
}

// RegisterStagingFlag registers the "staging" flag.
func RegisterStagingFlag() {
	if registeredFlags {
		panic("duplicate call to RegisterFlags or RegisterStagingFlag")
	}
	flag.BoolVar(&stagingFlag, "staging", false, "use the staging build coordinator and buildlets")
	registeredFlags = true
}

// FromFlags returns the build environment specified from flags,
// as registered by RegisterFlags or RegisterStagingFlag.
// By default it returns the production environment.
func FromFlags() *Environment {
	if !registeredFlags {
		panic("FromFlags called without RegisterFlags")
	}
	if localDevFlag {
		return Development
	}
	if stagingFlag {
		return Staging
	}
	return Production
}

// warnCredsOnce guards CheckUserCredentials spamming stderr. Once is enough.
var warnCredsOnce sync.Once

// CheckUserCredentials warns if the gcloud Application Default Credentials file doesn't exist
// and says how to log in properly.
func CheckUserCredentials() {
	adcJSON := filepath.Join(os.Getenv("HOME"), ".config/gcloud/application_default_credentials.json")
	if _, err := os.Stat(adcJSON); os.IsNotExist(err) {
		warnCredsOnce.Do(func() {
			log.Printf("warning: file %s does not exist; did you run 'gcloud auth application-default login' ? (The 'application-default' part matters, confusingly.)", adcJSON)
		})
	}
}

// diagnoseFailureTokenSource is an oauth2.TokenSource wrapper that,
// upon failure, diagnoses why the token acquistion might've failed.
type diagnoseFailureTokenSource struct {
	ts oauth2.TokenSource
}

func (ts diagnoseFailureTokenSource) Token() (*oauth2.Token, error) {
	t, err := ts.ts.Token()
	if err != nil {
		CheckUserCredentials()
		return nil, err
	}
	return t, nil
}
