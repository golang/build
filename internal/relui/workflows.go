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
	version := workflow.Parameter{
		Name: "Version",
		Doc: `Version is the Go version that has been released.

The version string must use the same format as Go tags.`,
	}
	security := workflow.Parameter{
		Name: "Security (optional)",
		Doc: `Security is an optional sentence describing security fixes included in this release.

The empty string means there are no security fixes to highlight.

Past examples:
• "Includes a security fix for crypto/tls (CVE-2021-34558)."
• "Includes a security fix for the Wasm port (CVE-2021-38297)."
• "Includes security fixes for encoding/pem (CVE-2022-24675), crypto/elliptic (CVE-2022-28327), crypto/x509 (CVE-2022-27536)."`,
	}
	announcement := workflow.Parameter{
		Name:          "Announcement",
		ParameterType: workflow.URL,
		Doc: `Announcement is the announcement URL.

It's applicable to all release types other than major
(since major releases point to release notes instead).`,
		Example: "https://groups.google.com/g/golang-announce/c/wB1fph5RpsE/m/ZGwOsStwAwAJ",
	}

	{
		minorVersion := version
		minorVersion.Example = "go1.18.2"
		secondaryVersion := workflow.Parameter{
			Name:    "SecondaryVersion",
			Doc:     `SecondaryVersion is an older Go version that was also released.`,
			Example: "go1.17.10",
		}

		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-minor", func(ctx *workflow.TaskContext, v1, v2, sec, ann string) (string, error) {
			return task.TweetMinorRelease(ctx, task.ReleaseTweet{Version: v1, SecondaryVersion: v2, Security: sec, Announcement: ann}, e)
		}, wd.Parameter(minorVersion), wd.Parameter(secondaryVersion), wd.Parameter(security), wd.Parameter(announcement)))
		h.RegisterDefinition("tweet-minor", wd)
	}
	{
		betaVersion := version
		betaVersion.Example = "go1.19beta1"

		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-beta", func(ctx *workflow.TaskContext, v, sec, ann string) (string, error) {
			return task.TweetBetaRelease(ctx, task.ReleaseTweet{Version: v, Security: sec, Announcement: ann}, e)
		}, wd.Parameter(betaVersion), wd.Parameter(security), wd.Parameter(announcement)))
		h.RegisterDefinition("tweet-beta", wd)
	}
	{
		rcVersion := version
		rcVersion.Example = "go1.19rc1"

		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-rc", func(ctx *workflow.TaskContext, v, sec, ann string) (string, error) {
			return task.TweetRCRelease(ctx, task.ReleaseTweet{Version: v, Security: sec, Announcement: ann}, e)
		}, wd.Parameter(rcVersion), wd.Parameter(security), wd.Parameter(announcement)))
		h.RegisterDefinition("tweet-rc", wd)
	}
	{
		majorVersion := version
		majorVersion.Example = "go1.19"

		wd := workflow.New()
		wd.Output("TweetURL", wd.Task("tweet-major", func(ctx *workflow.TaskContext, v, sec string) (string, error) {
			return task.TweetMajorRelease(ctx, task.ReleaseTweet{Version: v, Security: sec}, e)
		}, wd.Parameter(majorVersion), wd.Parameter(security)))
		h.RegisterDefinition("tweet-major", wd)
	}
}

// newEchoWorkflow returns a runnable workflow.Definition for
// development.
func newEchoWorkflow() *workflow.Definition {
	wd := workflow.New()
	wd.Output("greeting", wd.Task("greeting", echo, wd.Parameter(workflow.Parameter{Name: "greeting"})))
	wd.Output("farewell", wd.Task("farewell", echo, wd.Parameter(workflow.Parameter{Name: "farewell"})))
	return wd
}

func echo(ctx *workflow.TaskContext, arg string) (string, error) {
	ctx.Printf("echo(%v, %q)", ctx, arg)
	return arg, nil
}
