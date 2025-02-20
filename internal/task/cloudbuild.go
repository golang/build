// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"io/fs"
	"math/rand"
	"regexp"
	"strings"
	"time"

	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/storage"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/gcsfs"
	"golang.org/x/build/internal/secret"
	wf "golang.org/x/build/internal/workflow"
)

const gitGenerateVersion = "v0.0.0-20240603191855-5c202b9c66be"

type CloudBuildClient interface {
	// RunBuildTrigger runs an existing trigger in project with the given
	// substitutions.
	RunBuildTrigger(ctx context.Context, project, trigger string, substitutions map[string]string) (CloudBuild, error)

	// GenerateAutoSubmitChange generates a change with the given metadata and
	// contents generated via the [git-generate] script that must be in the commit message,
	// starts trybots with auto-submit enabled, and returns its change ID.
	// If the requested contents match the state of the repository, no change
	// is created and the returned change ID will be empty.
	// Reviewers is the username part of a golang.org or google.com email address.
	GenerateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string) (changeID string, _ error)

	// RunScript runs the given script under bash -eux -o pipefail in
	// ScriptProject. Outputs are collected into the build's ResultURL,
	// readable with ResultFS. The script will have the latest version of Go
	// and some version of gsutil on $PATH.
	// If gerritProject is provided, the script operates within a checkout of the
	// latest commit on the default branch of that repository.
	RunScript(ctx context.Context, script string, gerritProject string, outputs []string) (CloudBuild, error)
	// RunCustomSteps is a low-level API that provides direct control over
	// individual Cloud Build steps. It creates a random result directory
	// and provides that as a parameter to the steps function, so that it
	// may write output to it with 'gsutil cp' for accessing via ResultFS.
	// Prefer RunScript for simpler scenarios.
	// Reference: https://cloud.google.com/build/docs/build-config-file-schema
	RunCustomSteps(ctx context.Context, steps func(resultURL string) []*cloudbuildpb.BuildStep, opts *CloudBuildOptions) (CloudBuild, error)

	// Completed reports whether a build has finished, returning an error if
	// it's failed. It's suitable for use with AwaitCondition.
	Completed(ctx context.Context, build CloudBuild) (detail string, completed bool, _ error)
	// ResultFS returns an FS that contains the results of the given build.
	// The build must've been created by RunScript or RunCustomSteps.
	ResultFS(ctx context.Context, build CloudBuild) (fs.FS, error)
}

// CloudBuildOptions allows to customize CloudBuild configurations.
type CloudBuildOptions struct {
	AvailableSecrets *cloudbuildpb.Secrets
}

type RealCloudBuildClient struct {
	BuildClient   *cloudbuild.Client
	StorageClient *storage.Client
	ScriptProject string
	ScriptAccount string
	ScratchURL    string
}

// CloudBuild represents a Cloud Build that can be queried with the status
// methods on CloudBuildClient.
type CloudBuild struct {
	Project, ID string
	ResultURL   string
}

func (c *RealCloudBuildClient) RunBuildTrigger(ctx context.Context, project, trigger string, substitutions map[string]string) (CloudBuild, error) {
	op, err := c.BuildClient.RunBuildTrigger(ctx, &cloudbuildpb.RunBuildTriggerRequest{
		ProjectId: project,
		TriggerId: trigger,
		Source: &cloudbuildpb.RepoSource{
			Substitutions: substitutions,
		},
	})
	if err != nil {
		return CloudBuild{}, err
	}
	if _, err = op.Poll(ctx); err != nil {
		return CloudBuild{}, err
	}
	meta, err := op.Metadata()
	if err != nil {
		return CloudBuild{}, err
	}
	return CloudBuild{Project: project, ID: meta.Build.Id}, nil
}

const cloudBuildClientScriptPrefix = `#!/usr/bin/env bash
set -eux
set -o pipefail
export PATH=/workspace/released_go/bin:$PATH
`

const cloudBuildClientDownloadGoScript = `#!/usr/bin/env bash
set -eux
archive=$(wget -qO - 'https://go.dev/dl/?mode=json' | grep -Eo 'go.*linux-amd64.tar.gz' | head -n 1)
wget -qO - https://go.dev/dl/${archive} | tar -xz
mv go /workspace/released_go
`

func (c *RealCloudBuildClient) RunScript(ctx context.Context, script string, gerritProject string, outputs []string) (CloudBuild, error) {
	steps := func(resultURL string) []*cloudbuildpb.BuildStep {
		// Cloud build loses directory structure when it saves artifacts, which is
		// a problem since (e.g.) we have multiple files named go.mod in the
		// tagging tasks. It's not very complicated, so reimplement it ourselves.
		var saveOutputsScript strings.Builder
		saveOutputsScript.WriteString(cloudBuildClientScriptPrefix)
		for _, out := range outputs {
			saveOutputsScript.WriteString(fmt.Sprintf("gsutil cp %q %q\n", out, resultURL+"/"+strings.TrimPrefix(out, "./")))
		}

		var steps []*cloudbuildpb.BuildStep
		var dir string
		if gerritProject != "" {
			steps = append(steps, &cloudbuildpb.BuildStep{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"clone", "https://go.googlesource.com/" + gerritProject, "checkout"},
			})
			dir = "checkout"
		}
		steps = append(steps,
			&cloudbuildpb.BuildStep{
				Name:   "bash",
				Script: cloudBuildClientDownloadGoScript,
			},
			&cloudbuildpb.BuildStep{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: cloudBuildClientScriptPrefix + script,
				Dir:    dir,
			},
			&cloudbuildpb.BuildStep{
				Name:   "gcr.io/cloud-builders/gsutil",
				Script: saveOutputsScript.String(),
				Dir:    dir,
			},
		)

		return steps
	}

	return c.RunCustomSteps(ctx, steps, nil)
}

func (c *RealCloudBuildClient) GenerateAutoSubmitChange(ctx *wf.TaskContext, input gerrit.ChangeInput, reviewers []string) (changeID string, _ error) {
	if input.Project == "" {
		return "", fmt.Errorf("input.Project must be specified")
	} else if input.Branch == "" {
		return "", fmt.Errorf("input.Branch must be specified")
	} else if !strings.Contains(input.Subject, "\n[git-generate]\n") {
		return "", fmt.Errorf("a commit message with a [git-generate] script must be provided")
	}

	// Add a Change-Id trailer to the commit message if it's not already present.
	var changeIDTrailers int
	if strings.HasPrefix(input.Subject, "Change-Id: ") {
		changeIDTrailers++
	}
	changeIDTrailers += strings.Count(input.Subject, "\nChange-Id: ")
	if changeIDTrailers > 1 {
		return "", fmt.Errorf("multiple Change-Id lines")
	}
	if changeIDTrailers == 0 {
		// randomBytes returns 20 random bytes suitable for use in a Change-Id line.
		randomBytes := func() []byte { var id [20]byte; cryptorand.Read(id[:]); return id[:] }

		// endsWithMetadataLine reports whether the given commit message ends with a
		// metadata line such as "Bug: #42" or "Signed-off-by: Al <al@example.com>".
		metadataLineRE := regexp.MustCompile(`^[a-zA-Z0-9-]+: `)
		endsWithMetadataLine := func(msg string) bool {
			i := strings.LastIndexByte(msg, '\n')
			return i >= 0 && metadataLineRE.MatchString(msg[i+1:])
		}

		msg := strings.TrimRight(input.Subject, "\n")
		sep := "\n\n"
		if endsWithMetadataLine(msg) {
			sep = "\n"
		}
		input.Subject += fmt.Sprintf("%sChange-Id: I%x", sep, randomBytes())
	}

	refspec := fmt.Sprintf("HEAD:refs/for/%s%%l=Auto-Submit,l=Commit-Queue+1", input.Branch)
	reviewerEmails, err := coordinatorEmails(reviewers)
	if err != nil {
		return "", err
	}
	for _, r := range reviewerEmails {
		refspec += ",r=" + r
	}

	// Create a Cloud Build that will generate and mail the CL.
	//
	// To remove the possibility of mailing multiple CLs due to
	// automated retries, allow only manual retries from this point.
	ctx.DisableRetries()
	op, err := c.BuildClient.CreateBuild(ctx, &cloudbuildpb.CreateBuildRequest{
		ProjectId: c.ScriptProject,
		Build: &cloudbuildpb.Build{
			Steps: []*cloudbuildpb.BuildStep{
				{
					Name: "bash", Script: cloudBuildClientDownloadGoScript,
				},
				{
					Name: "gcr.io/cloud-builders/git",
					Args: []string{"clone", "--branch=" + input.Branch, "--depth=1", "--",
						"https://go.googlesource.com/" + input.Project, "checkout"},
				},
				{
					Name: "gcr.io/cloud-builders/git",
					Args: []string{"-c", "user.name=Gopher Robot", "-c", "user.email=gobot@golang.org",
						"commit", "--allow-empty", "-m", input.Subject},
					Dir: "checkout",
				},
				{
					Name:       "gcr.io/cloud-builders/git",
					Entrypoint: "/workspace/released_go/bin/go",
					Args:       []string{"run", "rsc.io/rf/git-generate@" + gitGenerateVersion},
					Dir:        "checkout",
				},
				{
					Name: "gcr.io/cloud-builders/git",
					Args: []string{"-c", "user.name=Gopher Robot", "-c", "user.email=gobot@golang.org",
						"commit", "--amend", "--no-edit"},
					Dir: "checkout",
				},
				{
					Name: "gcr.io/cloud-builders/git",
					Args: []string{"show", "HEAD"},
					Dir:  "checkout",
				},
				{
					Name: "bash", Args: []string{"-c", `touch .gitcookies && chmod 0600 .gitcookies && printf ".googlesource.com\tTRUE\t/\tTRUE\t2147483647\to\tgit-gobot.golang.org=$$GOBOT_TOKEN\n" >> .gitcookies`},
					SecretEnv: []string{"GOBOT_TOKEN"},
				},
				{
					Name:       "gcr.io/cloud-builders/git",
					Entrypoint: "bash",
					Args:       []string{"-c", `git -c http.cookieFile=../.gitcookies push origin ` + refspec + ` 2>&1 | tee "$$BUILDER_OUTPUT/output"`},
					Dir:        "checkout",
				},
				{
					Name: "bash", Args: []string{"-c", "rm .gitcookies"},
				},
			},
			Options: &cloudbuildpb.BuildOptions{
				MachineType: cloudbuildpb.BuildOptions_E2_HIGHCPU_8,
				Logging:     cloudbuildpb.BuildOptions_CLOUD_LOGGING_ONLY,
			},
			ServiceAccount: c.ScriptAccount,
			AvailableSecrets: &cloudbuildpb.Secrets{
				SecretManager: []*cloudbuildpb.SecretManagerSecret{
					{
						VersionName: "projects/" + c.ScriptProject + "/secrets/" + secret.NameGobotPassword + "/versions/latest",
						Env:         "GOBOT_TOKEN",
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating build: %w", err)
	}
	if _, err = op.Poll(ctx); err != nil {
		return "", fmt.Errorf("polling: %w", err)
	}
	meta, err := op.Metadata()
	if err != nil {
		return "", fmt.Errorf("reading metadata: %w", err)
	}
	build := CloudBuild{Project: c.ScriptProject, ID: meta.Build.Id}

	// Await the Cloud Build and extract the ID of the CL that was mailed.
	ctx.Printf("Awaiting completion of build %q in %s.", build.ID, build.Project)
	return AwaitCondition(ctx, 30*time.Second, func() (changeID string, completed bool, _ error) {
		return c.completedGeneratingCL(ctx, build)
	})
}

// completedGeneratingCL reports whether a build has finished,
// returning the change ID that the given build generated.
// The build must've been created by GenerateAutoSubmitChange.
// It's suitable for use with AwaitCondition.
func (c *RealCloudBuildClient) completedGeneratingCL(ctx context.Context, build CloudBuild) (changeID string, completed bool, _ error) {
	b, err := c.BuildClient.GetBuild(ctx, &cloudbuildpb.GetBuildRequest{
		ProjectId: build.Project,
		Id:        build.ID,
	})
	if err != nil {
		return "", false, err
	}
	if b.FinishTime == nil {
		return "", false, nil
	}
	if b.Status != cloudbuildpb.Build_SUCCESS {
		return "", false, fmt.Errorf("build %q failed, see %v: %v", build.ID, b.LogUrl, b.FailureInfo)
	}

	// Extract the CL number from the output using a simple regexp.
	re := regexp.MustCompile(`https:\/\/go-review\.googlesource\.com\/c\/([a-zA-Z0-9_\-]+)\/\+\/(\d+)`)
	gitPushOutput := bytes.Join(b.GetResults().GetBuildStepOutputs(), nil)
	if matches := re.FindSubmatch(gitPushOutput); len(matches) == 3 {
		changeID = fmt.Sprintf("%s~%s", matches[1], matches[2])
	} else {
		return "", false, fmt.Errorf("no match for successful mail of generated CL in git push output:\n%s", gitPushOutput)
	}

	return changeID, true, nil
}

func (c *RealCloudBuildClient) RunCustomSteps(ctx context.Context, steps func(resultURL string) []*cloudbuildpb.BuildStep, options *CloudBuildOptions) (CloudBuild, error) {
	resultURL := fmt.Sprintf("%v/script-build-%v", c.ScratchURL, rand.Int63())
	build := &cloudbuildpb.Build{
		Steps: steps(resultURL),
		Options: &cloudbuildpb.BuildOptions{
			MachineType: cloudbuildpb.BuildOptions_E2_HIGHCPU_8,
			Logging:     cloudbuildpb.BuildOptions_CLOUD_LOGGING_ONLY,
		},
		ServiceAccount: c.ScriptAccount,
	}
	if options != nil {
		build.AvailableSecrets = options.AvailableSecrets
	}
	op, err := c.BuildClient.CreateBuild(ctx, &cloudbuildpb.CreateBuildRequest{
		ProjectId: c.ScriptProject,
		Build:     build,
	})
	if err != nil {
		return CloudBuild{}, fmt.Errorf("creating build: %w", err)
	}
	if _, err = op.Poll(ctx); err != nil {
		return CloudBuild{}, fmt.Errorf("polling: %w", err)
	}
	meta, err := op.Metadata()
	if err != nil {
		return CloudBuild{}, fmt.Errorf("reading metadata: %w", err)
	}
	return CloudBuild{Project: c.ScriptProject, ID: meta.Build.Id, ResultURL: resultURL}, nil
}

func (c *RealCloudBuildClient) Completed(ctx context.Context, build CloudBuild) (string, bool, error) {
	b, err := c.BuildClient.GetBuild(ctx, &cloudbuildpb.GetBuildRequest{
		ProjectId: build.Project,
		Id:        build.ID,
	})
	if err != nil {
		return "", false, err
	}
	if b.FinishTime == nil {
		return "", false, nil
	}
	if b.Status != cloudbuildpb.Build_SUCCESS {
		return "", false, fmt.Errorf("build %q failed, see %v: %v", build.ID, build.ResultURL, b.FailureInfo)
	}
	return b.StatusDetail, true, nil
}

func (c *RealCloudBuildClient) ResultFS(ctx context.Context, build CloudBuild) (fs.FS, error) {
	return gcsfs.FromURL(ctx, c.StorageClient, build.ResultURL)
}
