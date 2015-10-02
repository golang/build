// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/build/dashboard"
	"golang.org/x/build/kubernetes"
	"golang.org/x/build/kubernetes/api"
	"golang.org/x/net/context"
	"golang.org/x/net/context/ctxhttp"
)

// PodOpts control how new pods are started.
type PodOpts struct {
	// ImageRegistry specifies the Docker registry Kubernetes
	// will use to create the pod. Required.
	ImageRegistry string

	// TLS optionally specifies the TLS keypair to use.
	// If zero, http without auth is used.
	TLS KeyPair

	// Description optionally describes the pod.
	Description string

	// Labels optionally specify key=value strings that Kubernetes
	// can use to filter and group pods.
	Labels map[string]string

	// DeleteIn optionally specifies a duration at which
	// to delete the pod.
	DeleteIn time.Duration

	// OnInstanceRequested optionally specifies a hook to run synchronously
	// after the pod create call, but before
	// waiting for its operation to proceed.
	OnPodRequested func()

	// OnPodCreated optionally specifies a hook to run synchronously
	// after the pod operation succeeds.
	OnPodCreated func()

	// OnPodCreated optionally specifies a hook to run synchronously
	// after the pod Get call.
	OnGotPodInfo func()
}

// StartPod creates a new pod on a Kubernetes cluster and returns a buildlet client
// configured to speak to it.
func StartPod(ctx context.Context, kubeClient *kubernetes.Client, podName, builderType string, opts PodOpts) (*Client, error) {
	conf, ok := dashboard.Builders[builderType]
	if !ok || conf.KubeImage == "" {
		return nil, fmt.Errorf("invalid builder type %q", builderType)
	}
	pod := &api.Pod{
		TypeMeta: api.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: api.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"name": podName,
				"type": builderType,
				"role": "buildlet",
			},
		},
		Spec: api.PodSpec{
			RestartPolicy: api.RestartPolicyNever,
			Containers: []api.Container{
				{
					Name:            "buildlet",
					Image:           imageID(opts.ImageRegistry, conf.KubeImage),
					ImagePullPolicy: api.PullAlways,
					Command:         []string{"/usr/local/bin/stage0"},
					Ports: []api.ContainerPort{
						{
							ContainerPort: 80,
						},
					},
					Env: []api.EnvVar{
						{
							Name:  "IN_KUBERNETES",
							Value: "1",
						},
					},
				},
			},
		},
	}
	addEnv := func(name, value string) {
		for i, _ := range pod.Spec.Containers {
			pod.Spec.Containers[i].Env = append(pod.Spec.Containers[i].Env, api.EnvVar{
				Name:  name,
				Value: value,
			})
		}
	}
	// The buildlet-binary-url is the URL of the buildlet binary
	// which the pods are configured to download at boot and run.
	// This lets us/ update the buildlet more easily than
	// rebuilding the whole pod image.
	addEnv("META_BUILDLET_BINARY_URL", conf.BuildletBinaryURL())
	addEnv("META_BUILDER_TYPE", builderType)
	if !opts.TLS.IsZero() {
		addEnv("META_TLS_CERT", opts.TLS.CertPEM)
		addEnv("META_TLS_KEY", opts.TLS.KeyPEM)
		addEnv("META_PASSWORD", opts.TLS.Password())
	}

	if opts.DeleteIn != 0 {
		// In case the pod gets away from us (generally: if the
		// coordinator dies while a build is running), then we
		// set this attribute of when it should be killed so
		// we can kill it later when the coordinator is
		// restarted. The cleanUpOldPods goroutine loop handles
		// that killing.
		addEnv("META_DELETE_AT", fmt.Sprint(time.Now().Add(opts.DeleteIn).Unix()))
	}

	status, err := kubeClient.Run(ctx, pod)
	if err != nil {
		return nil, fmt.Errorf("pod could not be created: %v", err)
	}
	// The new pod must be in Running phase. Possible phases are described at
	// http://releases.k8s.io/HEAD/docs/user-guide/pod-states.md#pod-phase
	if status.Phase != api.PodRunning {
		return nil, fmt.Errorf("pod is in invalid state %q: %v", status.Phase, status.Message)
	}

	// Wait for the pod to boot and its buildlet to come up.
	var buildletURL string
	var ipPort string
	if !opts.TLS.IsZero() {
		buildletURL = "https://" + status.PodIP
		ipPort = status.PodIP + ":443"
	} else {
		buildletURL = "http://" + status.PodIP
		ipPort = status.PodIP + ":80"
	}
	condRun(opts.OnGotPodInfo)

	impatientClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Dial:              defaultDialer(),
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	c := make(chan error, 1)
	go func() {
		defer close(c)
		try := 0
		for {
			try++
			// Make sure pod is still running
			podStatus, err := kubeClient.PodStatus(ctx, pod.Name)
			if err != nil {
				c <- fmt.Errorf("polling the buildlet pod for its status failed: %v", err)
				return
			}
			if podStatus.Phase != api.PodRunning {
				podLog, err := kubeClient.PodLog(ctx, pod.Name)
				if err != nil {
					log.Printf("failed to retrieve log for pod %q: %v", pod.Name, err)
					c <- fmt.Errorf("buildlet pod left the Running phase and entered phase %q", podStatus.Phase)
					return
				}
				log.Printf("log from pod %q: %v", pod.Name, podLog)
				c <- fmt.Errorf("buildlet pod left the Running phase and entered phase %q", podStatus.Phase)
				return
			}

			res, err := ctxhttp.Get(ctx, impatientClient, buildletURL)
			if err != nil {
				time.Sleep(1 * time.Second)
				continue
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				c <- fmt.Errorf("buildlet returned HTTP status code %d on try number %d", res.StatusCode, try)
			}
			return
		}
	}()

	// Wait for the buildlet to respond to an HTTP request. If the timeout happens first, or
	// if the buildlet pod leaves the running state, return an error.
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err = <-c:
			if err != nil {
				return nil, err
			}
			return NewClient(ipPort, opts.TLS), nil
		}
	}
}

func imageID(registry, image string) string {
	// Sanitize the registry and image names
	registry = strings.TrimRight(registry, "/")
	image = strings.TrimLeft(image, "/")
	return registry + "/" + image
}
