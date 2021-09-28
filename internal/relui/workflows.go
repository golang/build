// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"golang.org/x/build/internal/workflow"
)

// Definitions is a list of all initialized Definition that can be
// created.
var Definitions = map[string]*workflow.Definition{
	"echo": newEchoWorkflow(),
}

// newEchoWorkflow returns a runnable workflow.Definition for
// development.
func newEchoWorkflow() *workflow.Definition {
	wd := workflow.New()
	greeting := wd.Task("greeting", echo, wd.Parameter("greeting"))
	wd.Output("greeting", greeting)
	wd.Output("farewell", wd.Task("farewell", echo, wd.Parameter("farewell")))
	return wd
}

func echo(ctx *workflow.TaskContext, arg string) (string, error) {
	ctx.Printf("echo(%v, %q)", ctx, arg)
	return arg, nil
}
