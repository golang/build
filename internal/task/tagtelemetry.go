// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/semver"
)

// TagTelemetryTasks implements a new workflow definition to tag
// x/telemetry/config whenever the generated config.json changes.
type TagTelemetryTasks struct {
	Gerrit           GerritClient
	GerritURL        string
	CreateBuildlet   func(context.Context, string) (buildlet.RemoteClient, error)
	LatestGoBinaries func(context.Context) (string, error)
}

func (t *TagTelemetryTasks) NewDefinition() *wf.Definition {
	wd := wf.New()

	reviewers := wf.Param(wd, reviewersParam)
	changeID := wf.Task1(wd, "generate config CL", t.GenerateConfig, reviewers)
	submitted := wf.Action1(wd, "await config CL submission", t.AwaitSubmission, changeID)
	tag := wf.Task0(wd, "tag if appropriate", t.MaybeTag, wf.After(submitted))
	wf.Output(wd, "tag", tag)

	return wd
}

// GenerateConfig runs the upload config generator in a buildlet, extracts the
// resulting config.json, and creates a CL with the result if anything changed.
//
// It returns the change ID, or "" if the CL was not created.
func (t *TagTelemetryTasks) GenerateConfig(ctx *wf.TaskContext, reviewers []string) (string, error) {
	const clTitle = "config: regenerate upload config"

	// Query for an existing pending config CL, to avoid duplication.
	//
	// Only wait a week, because configs are volatile: we really want to update
	// them within a week.
	query := fmt.Sprintf(`message:%q status:open owner:gobot@golang.org repo:telemetry -age:7d`, clTitle)
	changes, err := t.Gerrit.QueryChanges(ctx, query)
	if err != nil {
		return "", err
	}
	if len(changes) > 0 {
		ctx.Printf("not creating CL: found existing CL %d", changes[0].ChangeNumber)
		return "", nil
	}

	binaries, err := t.LatestGoBinaries(ctx)
	if err != nil {
		return "", err
	}

	// linux-amd64 automatically disables outbound network access, unless explicitly specified by
	// setting GO_DISABLE_OUTBOUND_NETWORK=0. This has to be done every time Exec is called, since
	// once the network is disabled it cannot be undone. We could also use linux-amd64-longtest,
	// which does not have this property.
	bc, err := t.CreateBuildlet(ctx, "linux-amd64")
	if err != nil {
		return "", err
	}
	defer bc.Close()
	if err := bc.PutTarFromURL(ctx, binaries, ""); err != nil {
		return "", fmt.Errorf("putting Go binaries: %v", err)
	}
	tarURL := fmt.Sprintf("%s/%s/+archive/%s.tar.gz", t.GerritURL, "telemetry", "master")
	if err := bc.PutTarFromURL(ctx, tarURL, "telemetry"); err != nil {
		return "", fmt.Errorf("putting telemetry content: %v", err)
	}

	before, err := readBuildletFile(bc, "telemetry/config/config.json")
	if err != nil {
		return "", fmt.Errorf("reading initial config: %v", err)
	}

	logWriter := &LogWriter{Logger: ctx}
	go logWriter.Run(ctx)
	remoteErr, execErr := bc.Exec(ctx, "go/bin/go", buildlet.ExecOpts{
		Dir:      "telemetry",
		Args:     []string{"run", "./internal/configgen", "-w"},
		Output:   logWriter,
		ExtraEnv: []string{"GO_DISABLE_OUTBOUND_NETWORK=0"},
	})
	if execErr != nil {
		return "", fmt.Errorf("Exec failed: %v", execErr)
	}
	if remoteErr != nil {
		return "", fmt.Errorf("Command failed: %v", remoteErr)
	}

	after, err := readBuildletFile(bc, "telemetry/config/config.json")
	if err != nil {
		return "", fmt.Errorf("reading generated config: %v", err)
	}

	if bytes.Equal(before, after) {
		ctx.Printf("not creating CL: config has not changed")
		return "", nil
	}

	changeInput := gerrit.ChangeInput{
		Project: "telemetry",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the generated upload config.", clTitle),
		Branch:  "master",
	}
	files := map[string]string{
		"config/config.json": string(after),
	}
	return t.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, files)
}

// readBuildletFile reads a single file from the buildlet at the specified file
// path.
func readBuildletFile(bc buildlet.RemoteClient, file string) ([]byte, error) {
	dir, name := path.Split(file)
	tgz, err := bc.GetTar(context.Background(), dir)
	if err != nil {
		return nil, err
	}
	defer tgz.Close()

	gzr, err := gzip.NewReader(tgz)
	if err != nil {
		return nil, err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Name == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("file %q not found", file)
}

// AwaitSubmitted waits for the CL with the given change ID to be submitted.
//
// The return value is the submitted commit hash, or "" if changeID is "".
func (t *TagTelemetryTasks) AwaitSubmission(ctx *wf.TaskContext, changeID string) error {
	if changeID == "" {
		ctx.Printf("not awaiting: no CL was created")
		return nil
	}

	ctx.Printf("awaiting review/submit of %v", ChangeLink(changeID))
	_, err := AwaitCondition(ctx, 10*time.Second, func() (string, bool, error) {
		return t.Gerrit.Submitted(ctx, changeID, "")
	})
	return err
}

// MaybeTag tags x/telemetry/config with the next version if config/config.json
// has changed.
//
// It returns the tag that was created, or "" if no tagging occurred.
func (t *TagTelemetryTasks) MaybeTag(ctx *wf.TaskContext) (string, error) {
	latestTag, latestVersion, err := t.latestConfigVersion(ctx)
	if err != nil {
		return "", err
	}
	if latestTag == "" {
		ctx.Printf("not tagging: no existing release tag found, not tagging the initial version")
		return "", nil
	}

	latestConfig, err := t.Gerrit.ReadFile(ctx, "telemetry", latestTag, "config/config.json")
	if err != nil {
		return "", fmt.Errorf("reading config/config.json@latest: %v", err)
	}
	master, err := t.Gerrit.ReadBranchHead(ctx, "telemetry", "master")
	if err != nil {
		return "", fmt.Errorf("reading master commit: %v", err)
	}
	masterConfig, err := t.Gerrit.ReadFile(ctx, "telemetry", master, "config/config.json")
	if err != nil {
		return "", fmt.Errorf("reading config/config.json@master: %v", err)
	}

	if bytes.Equal(latestConfig, masterConfig) {
		ctx.Printf("not tagging: no change to config.json since latest tag")
		return "", nil
	}

	nextVer, err := nextMinor(latestVersion)
	if err != nil {
		return "", fmt.Errorf("couldn't pick next version: %v", err)
	}
	tag := "config/" + nextVer

	ctx.Printf("tagging x/telemetry/config at %v as %v", master, tag)
	if err := t.Gerrit.Tag(ctx, "telemetry", tag, master); err != nil {
		return "", fmt.Errorf("failed to tag: %v", err)
	}

	return tag, nil
}

func (t *TagTelemetryTasks) latestConfigVersion(ctx context.Context) (tag, version string, _ error) {
	tags, err := t.Gerrit.ListTags(ctx, "telemetry")
	if err != nil {
		return "", "", err
	}
	latestTag := ""
	latestRelease := ""
	for _, tag := range tags {
		ver, ok := strings.CutPrefix(tag, "config/")
		if !ok {
			continue
		}
		if semver.IsValid(ver) && semver.Prerelease(ver) == "" &&
			(latestRelease == "" || semver.Compare(latestRelease, ver) < 0) {
			latestTag = tag
			latestRelease = ver
		}
	}
	return latestTag, latestRelease, nil
}
