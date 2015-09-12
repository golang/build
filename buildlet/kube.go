// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildlet

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/build/dashboard"
	"golang.org/x/build/kubernetes"
	"golang.org/x/build/kubernetes/api"
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
func StartPod(kubeClient *kubernetes.Client, podName, builderType string, opts PodOpts) (*Client, error) {
	conf, ok := dashboard.Builders[builderType]
	if !ok || conf.KubeImage == "" {
		return nil, fmt.Errorf("invalid builder type %q", builderType)
	}
	p := &api.Pod{
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
			RestartPolicy: "Never",
			Containers: []api.Container{
				{
					Name:            "buildlet",
					Image:           opts.ImageRegistry + conf.KubeImage,
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
						{
							Name:  "META_BUILDLET_BINARY_URL",
							Value: conf.BuildletBinaryURL(),
						},
					},
				},
			},
		},
	}

	if _, err := kubeClient.Run(p); err != nil {
		return nil, fmt.Errorf("pod could not be created: %v", err)
	}
	return nil, errors.New("TODO")
}
