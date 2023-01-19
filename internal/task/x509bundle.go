// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"context"
	"fmt"

	"golang.org/x/build/buildlet"
	"golang.org/x/build/gerrit"
	wf "golang.org/x/build/internal/workflow"
)

// This file contains a workflow definition for updating the X.509 root bundle
// in golang.org/x/crypto/x509roots. It is intended to be recurring, using the
// cron mechanism, in order to keep the bundle up to date with the upstream
// Mozilla NSS source.

type BundleNSSRootsTask struct {
	Gerrit           GerritClient
	GerritURL        string
	CreateBuildlet   func(context.Context, string) (buildlet.RemoteClient, error)
	LatestGoBinaries func(context.Context) (string, error)
}

func (x *BundleNSSRootsTask) NewDefinition() *wf.Definition {
	wd := wf.New()
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

	binaries, err := x.LatestGoBinaries(ctx)
	if err != nil {
		return "", err
	}
	// linux-amd64 automatically disables outbound network access, unless explicitly specified by
	// setting GO_DISABLE_OUTBOUND_NETWORK=0. This has to be done every time Exec is called, since
	// once the network is disabled it cannot be undone. We could also use linux-amd64-longtest,
	// which does not have this property.
	bc, err := x.CreateBuildlet(ctx, "linux-amd64")
	if err != nil {
		return "", err
	}
	defer bc.Close()
	if err := bc.PutTarFromURL(ctx, binaries, ""); err != nil {
		return "", err
	}
	cryptoTarURL := fmt.Sprintf("%s/%s/+archive/%s.tar.gz", x.GerritURL, "crypto", "master")
	if err := bc.PutTarFromURL(ctx, cryptoTarURL, "crypto"); err != nil {
		return "", err
	}

	writer := &LogWriter{Logger: ctx}
	go writer.Run(ctx)

	remoteErr, execErr := bc.Exec(ctx, "go/bin/go", buildlet.ExecOpts{
		Dir:      "crypto/x509roots",
		Args:     []string{"generate", "."},
		Output:   writer,
		ExtraEnv: []string{"GO_DISABLE_OUTBOUND_NETWORK=0"},
	})
	if execErr != nil {
		return "", fmt.Errorf("Exec failed: %v", execErr)
	}
	if remoteErr != nil {
		return "", fmt.Errorf("Command failed: %v", remoteErr)
	}
	tgz, err := bc.GetTar(context.Background(), "crypto/x509roots/fallback")
	if err != nil {
		return "", err
	}
	defer tgz.Close()
	crypto, err := tgzToMap(tgz)
	if err != nil {
		return "", err
	}

	files := map[string]string{
		"x509roots/fallback/bundle.go": crypto["bundle.go"],
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
