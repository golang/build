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
