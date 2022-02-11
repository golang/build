// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"sync"

	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
)

// DefinitionHolder holds workflow definitions.
type DefinitionHolder struct {
	mu          sync.Mutex
	definitions map[string]*workflow.Definition
}

// NewDefinitionHolder creates a new DefinitionHolder,
// initialized with a sample "echo" workflow.
func NewDefinitionHolder() *DefinitionHolder {
	return &DefinitionHolder{definitions: map[string]*workflow.Definition{
		"echo": newEchoWorkflow(),
	}}
}

// Definition returns the initialized workflow.Definition registered
// for a given name.
func (h *DefinitionHolder) Definition(name string) *workflow.Definition {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.definitions[name]
}

// RegisterDefinition registers a definition with a name.
// If a definition with the same name already exists, RegisterDefinition panics.
func (h *DefinitionHolder) RegisterDefinition(name string, d *workflow.Definition) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exist := h.definitions[name]; exist {
		panic("relui: multiple registrations for " + name)
	}
	h.definitions[name] = d
}

// Definitions returns the names of all registered definitions.
func (h *DefinitionHolder) Definitions() map[string]*workflow.Definition {
	h.mu.Lock()
	defer h.mu.Unlock()
	defs := make(map[string]*workflow.Definition)
	for k, v := range h.definitions {
		defs[k] = v
	}
	return defs
}

// RegisterTweetDefinitions registers workflow definitions involving tweeting
// onto h, using e for the external service configuration.
func RegisterTweetDefinitions(h *DefinitionHolder, e task.ExternalConfig) {
	{
		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-minor", func(ctx *workflow.TaskContext, v1, v2, sec, ann string) (string, error) {
			return task.TweetMinorRelease(ctx, task.ReleaseTweet{Version: v1, SecondaryVersion: v2, Security: sec, Announcement: ann}, e)
		}, wd.Parameter("Version"), wd.Parameter("SecondaryVersion"), wd.Parameter("Security (optional)"), wd.Parameter("Announcement")))
		h.RegisterDefinition("tweet-minor", wd)
	}
	{
		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-beta", func(ctx *workflow.TaskContext, v, sec, ann string) (string, error) {
			return task.TweetBetaRelease(ctx, task.ReleaseTweet{Version: v, Security: sec, Announcement: ann}, e)
		}, wd.Parameter("Version"), wd.Parameter("Security (optional)"), wd.Parameter("Announcement")))
		h.RegisterDefinition("tweet-beta", wd)
	}
	{
		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-rc", func(ctx *workflow.TaskContext, v, sec, ann string) (string, error) {
			return task.TweetRCRelease(ctx, task.ReleaseTweet{Version: v, Security: sec, Announcement: ann}, e)
		}, wd.Parameter("Version"), wd.Parameter("Security (optional)"), wd.Parameter("Announcement")))
		h.RegisterDefinition("tweet-rc", wd)
	}
	{
		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-major", func(ctx *workflow.TaskContext, v, sec string) (string, error) {
			return task.TweetMajorRelease(ctx, task.ReleaseTweet{Version: v, Security: sec}, e)
		}, wd.Parameter("Version"), wd.Parameter("Security (optional)")))
		h.RegisterDefinition("tweet-major", wd)
	}
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
