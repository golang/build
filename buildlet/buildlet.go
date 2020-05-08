// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/service/ec2"
	"google.golang.org/api/compute/v1"
)

// VMOpts control how new VMs are started.
type VMOpts struct {
	// Zone is the GCE zone to create the VM in.
	// Optional; defaults to provided build environment's zone.
	Zone string

	// ProjectID is the GCE project ID (e.g. "foo-bar-123", not
	// the numeric ID).
	// Optional; defaults to provided build environment's project ID ("name").
	ProjectID string

	// TLS optionally specifies the TLS keypair to use.
	// If zero, http without auth is used.
	TLS KeyPair

	// Optional description of the VM.
	Description string

	// Optional metadata to put on the instance.
	Meta map[string]string

	// DeleteIn optionally specifies a duration at which
	// to delete the VM.
	// If zero, a reasonable default is used.
	// Negative means no deletion timeout.
	DeleteIn time.Duration

	// OnInstanceRequested optionally specifies a hook to run synchronously
	// after the computeService.Instances.Insert call, but before
	// waiting for its operation to proceed.
	OnInstanceRequested func()

	// OnInstanceCreated optionally specifies a hook to run synchronously
	// after the instance operation succeeds.
	OnInstanceCreated func()

	// OnInstanceCreated optionally specifies a hook to run synchronously
	// after the computeService.Instances.Get call.
	// Only valid for GCE resources.
	OnGotInstanceInfo func(*compute.Instance)

	// OnInstanceCreated optionally specifies a hook to run synchronously
	// after the EC2 instance information is retrieved.
	// Only valid for EC2 resources.
	OnGotEC2InstanceInfo func(*ec2.Instance)

	// OnBeginBuildletProbe optionally specifies a hook to run synchronously
	// before StartNewVM tries to hit buildletURL to see if it's up yet.
	OnBeginBuildletProbe func(buildletURL string)

	// OnEndBuildletProbe optionally specifies a hook to run synchronously
	// after StartNewVM tries to hit the buildlet's URL to see if it's up.
	// The hook parameters are the return values from http.Get.
	OnEndBuildletProbe func(*http.Response, error)

	// SkipEndpointVerification does not verify that the builder is listening
	// on port 80 or 443 before creating a buildlet client.
	SkipEndpointVerification bool
}

// buildletClient returns a buildlet client configured to speak to a VM via the buildlet
// URL. The communication will use TLS if one is provided in the vmopts. This will wait until
// it can connect with the endpoint before returning. The buildletURL is in the form of:
// "https://<ip>". The ipPort field is in the form of "<ip>:<port>". The function
// will attempt to connect to the buildlet for the lesser of: the default timeout period
// (5 minutes) or the timeout set in the passed in context.
func buildletClient(ctx context.Context, buildletURL, ipPort string, opts *VMOpts) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	try := 0
	for !opts.SkipEndpointVerification {
		try++
		if ctx.Err() != nil {
			return nil, fmt.Errorf("unable to probe buildet at %s after %d attempts", buildletURL, try)
		}
		err := probeBuildlet(ctx, buildletURL, opts)
		if err == nil {
			break
		}
		log.Printf("probing buildlet at %s with attempt %d failed: %s", buildletURL, try, err)
		time.Sleep(time.Second)
	}
	return NewClient(ipPort, opts.TLS), nil
}

// probeBuildlet attempts to the connect to a buildlet at the provided URL. An error
// is returned if it unable to connect to the buildlet. Each request is limited by either
// a five second limit or the timeout set in the context.
func probeBuildlet(ctx context.Context, buildletURL string, opts *VMOpts) error {
	cl := &http.Client{
		Transport: &http.Transport{
			Dial:              defaultDialer(),
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	if fn := opts.OnBeginBuildletProbe; fn != nil {
		fn(buildletURL)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildletURL, nil)
	if err != nil {
		return fmt.Errorf("error creating buildlet probe request: %w", err)
	}
	res, err := cl.Do(req)
	if fn := opts.OnEndBuildletProbe; fn != nil {
		fn(res, err)
	}
	if err != nil {
		return fmt.Errorf("error probe buildlet %s: %w", buildletURL, err)
	}
	ioutil.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("buildlet returned HTTP status code %d for %s", res.StatusCode, buildletURL)
	}
	return nil
}
