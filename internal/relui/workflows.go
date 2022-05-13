// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relui

import (
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"path"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/gcsfs"
	"golang.org/x/build/internal/releasetargets"
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

// RegisterMailDLCLDefinition registers a workflow definition for mailing a golang.org/dl CL
// onto h, using e for the external service configuration.
func RegisterMailDLCLDefinition(h *DefinitionHolder, e task.ExternalConfig) {
	versions := workflow.Parameter{
		Name:          "Versions",
		ParameterType: workflow.SliceShort,
		Doc: `Versions are the Go versions that have been released.

The versions must use the same format as Go tags,
and the list must contain one or two versions.

For example:
• "go1.18.2" and "go1.17.10" for a minor Go release
• "go1.19" for a major Go release
• "go1.19beta1" or "go1.19rc1" for a pre-release`,
	}

	wd := workflow.New()
	wd.Output("ChangeURL", wd.Task("mail-dl-cl", func(ctx *workflow.TaskContext, versions []string) (string, error) {
		return task.MailDLCL(ctx, versions, e)
	}, wd.Parameter(versions)))
	h.RegisterDefinition("mail-dl-cl", wd)
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

func (tasks *BuildReleaseTasks) RegisterBuildReleaseWorkflows(h *DefinitionHolder) {
	go117, err := tasks.newBuildReleaseWorkflow("go1.17")
	if err != nil {
		panic(err)
	}
	h.RegisterDefinition("Release Go 1.17", go117)
	go118, err := tasks.newBuildReleaseWorkflow("go1.18")
	if err != nil {
		panic(err)
	}
	h.RegisterDefinition("Release Go 1.18", go118)

}

func (tasks *BuildReleaseTasks) newBuildReleaseWorkflow(majorVersion string) (*workflow.Definition, error) {
	wd := workflow.New()
	targets, ok := releasetargets.TargetsForVersion(majorVersion)
	if !ok {
		return nil, fmt.Errorf("malformed/unknown version %q", majorVersion)
	}
	version := wd.Parameter(workflow.Parameter{Name: "Version", Example: "go1.10.1"})
	revision := wd.Parameter(workflow.Parameter{Name: "Revision", Example: "release-branch.go1.10"})
	skipTests := wd.Parameter(workflow.Parameter{Name: "Targets to skip testing (space-separated target names or 'all') (optional)"})

	source := wd.Task("Build source archive", tasks.buildSource, revision, version)
	// Artifact file paths.
	artifacts := []workflow.Value{source}
	// Empty values that represent the dependency on tests passing.
	var testResults []workflow.Value
	for _, target := range targets {
		targetVal := wd.Constant(target)
		taskName := func(step string) string { return target.Name + ": " + step }

		// Build release artifacts for the platform.
		bin := wd.Task(taskName("Build binary archive"), tasks.buildBinary, targetVal, source)
		if target.GOOS == "windows" {
			zip := wd.Task(taskName("Convert to .zip"), tasks.convertToZip, targetVal, bin)
			msi := wd.Task(taskName("Build MSI"), tasks.buildMSI, targetVal, bin)
			artifacts = append(artifacts, msi, zip)
		} else {
			artifacts = append(artifacts, bin)
		}

		if target.BuildOnly {
			continue
		}
		short := wd.Task(taskName("Run short tests"), tasks.runTests, targetVal, wd.Constant(target.Builder), skipTests, bin)
		testResults = append(testResults, short)
		if target.LongTestBuilder != "" {
			long := wd.Task(taskName("Run long tests"), tasks.runTests, targetVal, wd.Constant(target.LongTestBuilder), skipTests, bin)
			testResults = append(testResults, long)
		}
	}
	// Eventually we need to sign artifacts and perhaps summarize test results.
	// For now, just mush them all together.
	stagedArtifacts := wd.Task("Stage artifacts for signing", tasks.copyToStaging, version, wd.Slice(artifacts))
	wd.Output("Staged artifacts", stagedArtifacts)
	results := wd.Task("Combine results", combineResults, stagedArtifacts, wd.Slice(testResults))
	wd.Output("Build results", results)
	return wd, nil
}

// BuildReleaseTasks serves as an adapter to the various build tasks in the task package.
type BuildReleaseTasks struct {
	GerritURL              string
	GCSClient              *storage.Client
	ScratchURL, StagingURL string
	CreateBuildlet         func(string) (buildlet.Client, error)
}

func (b *BuildReleaseTasks) buildSource(ctx *workflow.TaskContext, revision, version string) (artifact, error) {
	return b.runBuildStep(ctx, nil, "", artifact{}, "src.tar.gz", func(_ *task.BuildletStep, _ io.Reader, w io.Writer) error {
		return task.WriteSourceArchive(ctx, b.GerritURL, revision, version, w)
	})
}

func (b *BuildReleaseTasks) buildBinary(ctx *workflow.TaskContext, target *releasetargets.Target, source artifact) (artifact, error) {
	return b.runBuildStep(ctx, target, target.Builder, source, "tar.gz", func(bs *task.BuildletStep, r io.Reader, w io.Writer) error {
		return bs.BuildBinary(ctx, r, w)
	})
}

func (b *BuildReleaseTasks) buildMSI(ctx *workflow.TaskContext, target *releasetargets.Target, binary artifact) (artifact, error) {
	return b.runBuildStep(ctx, target, target.Builder, binary, "msi", func(bs *task.BuildletStep, r io.Reader, w io.Writer) error {
		return bs.BuildMSI(ctx, r, w)
	})
}

func (b *BuildReleaseTasks) convertToZip(ctx *workflow.TaskContext, target *releasetargets.Target, binary artifact) (artifact, error) {
	return b.runBuildStep(ctx, target, "", binary, "zip", func(_ *task.BuildletStep, r io.Reader, w io.Writer) error {
		return task.ConvertTGZToZIP(r, w)
	})
}

func (b *BuildReleaseTasks) runTests(ctx *workflow.TaskContext, target *releasetargets.Target, buildlet, skipTests string, binary artifact) (string, error) {
	skipped := skipTests == "all"
	skipTargets := strings.Fields(skipTests)
	for _, skip := range skipTargets {
		if target.Name == skip {
			skipped = true
		}
	}
	if skipped {
		ctx.Printf("Skipping test")
		return "skipped", nil
	}
	_, err := b.runBuildStep(ctx, target, buildlet, binary, "", func(bs *task.BuildletStep, r io.Reader, _ io.Writer) error {
		return bs.TestTarget(ctx, r)
	})
	return "", err
}

// runBuildStep is a convenience function that manages resources a build step might need.
// If target and buildlet name are specified, a BuildletStep will be passed to f.
// If inputName is specified, it will be opened and passed as a Reader to f.
// If outputSuffix is specified, a unique filename will be generated based off
// it (and the target name, if any), the file will be opened and passed as a
// Writer to f, and an artifact representing it will be returned as the result.
func (b *BuildReleaseTasks) runBuildStep(
	ctx *workflow.TaskContext,
	target *releasetargets.Target,
	buildletName string,
	input artifact,
	outputSuffix string,
	f func(*task.BuildletStep, io.Reader, io.Writer) error,
) (artifact, error) {
	var step *task.BuildletStep
	if buildletName != "" {
		if target == nil {
			return artifact{}, fmt.Errorf("target must be specified to use a buildlet")
		}
		ctx.Printf("Creating buildlet %v.", buildletName)
		client, err := b.CreateBuildlet(buildletName)
		if err != nil {
			return artifact{}, err
		}
		defer client.Close()
		buildConfig, ok := dashboard.Builders[buildletName]
		if !ok {
			return artifact{}, fmt.Errorf("unknown builder: %v", buildConfig)
		}
		step = &task.BuildletStep{
			Target:      target,
			Buildlet:    client,
			BuildConfig: buildConfig,
			Watch:       true,
		}
		ctx.Printf("Buildlet ready.")
	}

	scratchFS, err := gcsfs.FromURL(ctx, b.GCSClient, b.ScratchURL)
	if err != nil {
		return artifact{}, err
	}
	var in io.ReadCloser
	if input.scratchPath != "" {
		in, err = scratchFS.Open(input.scratchPath)
		if err != nil {
			return artifact{}, err
		}
		defer in.Close()
	}
	var out io.WriteCloser
	var scratchPath string
	hash := sha256.New()
	size := &sizeWriter{}
	var multiOut io.Writer
	if outputSuffix != "" {
		scratchName := outputSuffix
		if target != nil {
			scratchName = target.Name + "." + outputSuffix
		}
		scratchPath = fmt.Sprintf("%v/%v-%v", ctx.WorkflowID.String(), scratchName, rand.Int63())
		out, err = gcsfs.Create(scratchFS, scratchPath)
		if err != nil {
			return artifact{}, err
		}
		defer out.Close()
		multiOut = io.MultiWriter(out, hash, size)
	}
	// Hide in's Close method from the task, which may assert it to Closer.
	nopIn := io.NopCloser(in)
	if err := f(step, nopIn, multiOut); err != nil {
		return artifact{}, err
	}
	if step != nil {
		if err := step.Buildlet.Close(); err != nil {
			return artifact{}, err
		}
	}
	if in != nil {
		if err := in.Close(); err != nil {
			return artifact{}, err
		}
	}
	if out != nil {
		if err := out.Close(); err != nil {
			return artifact{}, err
		}
	}
	return artifact{
		target:      target,
		scratchPath: scratchPath,
		suffix:      outputSuffix,
		sha256:      fmt.Sprintf("%x", string(hash.Sum([]byte(nil)))),
		size:        size.size,
	}, nil
}

type artifact struct {
	// The target platform of this artifact, or nil for source.
	target *releasetargets.Target
	// The scratch path of this artifact.
	scratchPath string
	// The path the artifact was staged to for the signing process.
	stagingPath string
	// The filename suffix of the artifact, e.g. "tar.gz" or "src.tar.gz",
	// combined with the version and target name to produce filename.
	suffix string
	// The final filename of this artifact as it will be downloaded.
	filename string
	sha256   string
	size     int
}

type sizeWriter struct {
	size int
}

func (w *sizeWriter) Write(p []byte) (n int, err error) {
	w.size += len(p)
	return len(p), nil
}

func combineResults(ctx *workflow.TaskContext, artifacts []artifact, tests []string) (string, error) {
	return fmt.Sprintf("%#v\n\n", artifacts) + strings.Join(tests, "\n"), nil
}

func (tasks *BuildReleaseTasks) copyToStaging(ctx *workflow.TaskContext, version string, artifacts []artifact) ([]artifact, error) {
	scratchFS, err := gcsfs.FromURL(ctx, tasks.GCSClient, tasks.ScratchURL)
	if err != nil {
		return nil, err
	}
	stagingFS, err := gcsfs.FromURL(ctx, tasks.GCSClient, tasks.StagingURL)
	if err != nil {
		return nil, err
	}
	var stagedArtifacts []artifact
	for _, a := range artifacts {
		staged := a
		if a.target != nil {
			staged.filename = version + "." + a.target.Name + "." + a.suffix
		} else {
			staged.filename = version + "." + a.suffix
		}
		staged.stagingPath = path.Join(version, staged.filename)
		stagedArtifacts = append(stagedArtifacts, staged)

		in, err := scratchFS.Open(a.scratchPath)
		if err != nil {
			return nil, err
		}
		out, err := gcsfs.Create(stagingFS, staged.stagingPath)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(out, in); err != nil {
			return nil, err
		}
		if err := in.Close(); err != nil {
			return nil, err
		}
		if err := out.Close(); err != nil {
			return nil, err
		}
	}
	return stagedArtifacts, nil
}
