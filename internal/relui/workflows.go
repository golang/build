// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"sync"

	"golang.org/x/build/internal/workflow"
)

var dmut sync.Mutex
var definitions = map[string]*workflow.Definition{
	"echo": newEchoWorkflow(),
}

// Definition returns the initialized workflow.Definition registered
// for a given name.
func Definition(name string) *workflow.Definition {
	dmut.Lock()
	defer dmut.Unlock()
	return definitions[name]
}

// RegisterDefinition registers a definition with a name.
func RegisterDefinition(name string, d *workflow.Definition) {
	dmut.Lock()
	defer dmut.Unlock()
	definitions[name] = d
}

// Definitions returns the names of all registered definitions.
func Definitions() map[string]*workflow.Definition {
	dmut.Lock()
	defer dmut.Unlock()
	defs := make(map[string]*workflow.Definition)
	for k, v := range definitions {
		defs[k] = v
	}
	return defs
}

// newEchoWorkflow returns a runnable workflow.Definition for
// development.
func newEchoWorkflow() *workflow.Definition {
	wd := workflow.New()
	wd.Output("greeting", wd.Task("greeting", echo, wd.Parameter("greeting")))
	wd.Output("farewell", wd.Task("farewell", echo, wd.Parameter("farewell")))
	return wd
}

func echo(ctx *workflow.TaskContext, arg string) (string, error) {
	ctx.Printf("echo(%v, %q)", ctx, arg)
	return arg, nil
}
