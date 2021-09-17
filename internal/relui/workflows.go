// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"context"

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
	wd.Output("greeting", wd.Task("echo", echo, wd.Parameter("greeting")))
	return wd
}

func echo(_ context.Context, arg string) (string, error) {
	return arg, nil
}
