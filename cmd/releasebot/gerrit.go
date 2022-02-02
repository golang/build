// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/secret"
)

const (
	// gerritAPIURL is the Gerrit API URL.
	gerritAPIURL = "https://go-review.googlesource.com"
)

// loadGerritAuth loads Gerrit API credentials.
func loadGerritAuth() (gerrit.Auth, error) {
	sc, err := secret.NewClientInProject(buildenv.Production.ProjectName)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	token, err := sc.Retrieve(context.Background(), secret.NameGobotPassword)
	if err != nil {
		return nil, err
	}
	return gerrit.BasicAuth("git-gobot.golang.org", token), nil
}
