// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/kubernetes/api"
)

/*
This file implements the Kubernetes-based buildlet pool.
*/

var kubePool = &kubeBuildletPool{}

// kubeBuildletPool is the Kubernetes buildlet pool.
type kubeBuildletPool struct {
	// ...
	mu sync.Mutex
}

func (p *kubeBuildletPool) GetBuildlet(cancel Cancel, machineType, rev string, el eventTimeLogger) (*buildlet.Client, error) {
	return nil, errors.New("TODO")
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

// uid is caller-generated random id for the build
func buildletPod(cfg dashboard.BuildConfig, uid string) (*api.Pod, error) {
	pn := fmt.Sprintf("%v-%v", cfg.Name, uid)
	p := &api.Pod{
		TypeMeta: api.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: api.ObjectMeta{
			Name: pn,
			Labels: map[string]string{
				"type": "buildlet",
				"name": pn,
			},
		},
		Spec: api.PodSpec{
			Containers: []api.Container{
				{
					Name:  pn,
					Image: cfg.KubeImage,
					Ports: []api.ContainerPort{
						{
							Name:          "buildlet",
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
	return p, nil
}

func buildletService(p *api.Pod) (*api.Service, error) {
	s := &api.Service{
		TypeMeta: api.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: api.ObjectMeta{
			Name: p.ObjectMeta.Name,
			Labels: map[string]string{
				"type": "buildlet-service",
				"name": p.ObjectMeta.Name,
			},
		},
		Spec: api.ServiceSpec{
			Selector: p.ObjectMeta.Labels,
			Type:     api.ServiceTypeNodePort,
			Ports: []api.ServicePort{
				{
					Protocol: api.ProtocolTCP,
				},
			},
		},
	}
	return s, nil
}
