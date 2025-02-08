// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/relui/groups"
	wf "golang.org/x/build/internal/workflow"
)

// This file contains a workflow definition for updating the X.509 root bundle
// in golang.org/x/crypto/x509roots. It is intended to be recurring, using the
// cron mechanism, in order to keep the bundle up to date with the upstream
// Mozilla NSS source.

type BundleNSSRootsTask struct {
	Gerrit     GerritClient
	CloudBuild CloudBuildClient
}

func (x *BundleNSSRootsTask) NewDefinition() *wf.Definition {
	wd := wf.New(wf.ACL{Groups: []string{groups.ReleaseTeam, groups.SecurityTeam}})
	reviewers := wf.Param(wd, reviewersParam)

	done := wf.Task1(wd, "Update bundle", x.UpdateBundle, reviewers)

	// TODO(roland): In the future we may want to block this workflow on the
	// submission of the resulting CL (if there is one), and then tag the
	// x/crypto/x509roots submodule, and possibly also publish a vulndb entry in
	// order to force pickup of the new version. At that point we probably want
	// to use the existing AwaitCL functionality.

	wf.Output(wd, "done", done)

	return wd
}

const clTitle = "x509roots/fallback: update bundle"

func (x *BundleNSSRootsTask) UpdateBundle(ctx *wf.TaskContext, reviewers []string) (string, error) {
	query := fmt.Sprintf(`message:%q status:open owner:gobot@golang.org repo:crypto -age:14d`, clTitle)
	changes, err := x.Gerrit.QueryChanges(ctx, query)
	if err != nil {
		return "", err
	}
	if len(changes) != 0 {
		return "skipped, existing pending bundle update CL", nil
	}

	build, err := x.CloudBuild.RunScript(ctx, "cd x509roots && go generate", "crypto", []string{"x509roots/fallback/bundle.go"})
	if err != nil {
		return "", err
	}
	files, err := buildToOutputs(ctx, x.CloudBuild, build)
	if err != nil {
		return "", err
	}
	changeInput := gerrit.ChangeInput{
		Project: "crypto",
		Subject: fmt.Sprintf("%s\n\nThis is an automated CL which updates the NSS root bundle.", clTitle),
		Branch:  "master",
	}

	changeID, err := x.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, files)
	if err != nil {
		return "", err
	}
	if changeID == "" {
		return "no diff", nil
	}
	return changeID, nil
}
