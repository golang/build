// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package kubernetes

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// Dialer dials Kubernetes pods.
//
// TODO: services also.
type Dialer struct {
	kc *Client
}

func NewDialer(kc *Client) *Dialer {
	return &Dialer{kc: kc}
}

func (d *Dialer) Dial(ctx context.Context, podName string, port int) (net.Conn, error) {
	status, err := d.kc.PodStatus(ctx, podName)
	if err != nil {
		return nil, fmt.Errorf("PodStatus of %q: %v", podName, err)
	}
	if status.Phase != "Running" {
		return nil, fmt.Errorf("pod %q in state %q", podName, status.Phase)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(status.PodIP, strconv.Itoa(port)))
}
