// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/internal/secret"
)

// loadTwitterAuth loads Twitter API credentials.
func loadTwitterAuth() (secret.TwitterCredentials, error) {
	sc, err := secret.NewClientInProject(buildenv.Production.ProjectName)
	if err != nil {
		return secret.TwitterCredentials{}, err
	}
	defer sc.Close()
	secretJSON, err := sc.Retrieve(context.Background(), secret.NameTwitterAPISecret)
	if err != nil {
		return secret.TwitterCredentials{}, err
	}
	var v secret.TwitterCredentials
	err = json.Unmarshal([]byte(secretJSON), &v)
	if err != nil {
		return secret.TwitterCredentials{}, err
	}
	return v, nil
}
