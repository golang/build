// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cloud.google.com/go/compute/metadata"
)

var (
	tokenFile = flag.String("token", "", "read GitHub token personal access token from `file` (default $HOME/.github-issue-token)")

	githubToken   string
	githubOnceErr error
	githubOnce    sync.Once
)

func getTokenFromFile(ctx context.Context) (string, error) {
	githubOnce.Do(func() {
		const short = ".github-issue-token"
		filename := filepath.Clean(os.Getenv("HOME") + "/" + short)
		shortFilename := filepath.Clean("$HOME/" + short)
		if *tokenFile != "" {
			filename = *tokenFile
			shortFilename = *tokenFile
		}
		data, err := ioutil.ReadFile(filename)
		if err != nil {
			const fmtStr = `reading token: %v

Please create a personal access token at https://github.com/settings/tokens/new
and write it to %s or store it in GCE metadata using the
key 'maintner-github-token' to use this program.
The token only needs the repo scope, or private_repo if you want to view or edit
issues for private repositories. The benefit of using a personal access token
over using your GitHub password directly is that you can limit its use and revoke
it at any time.`
			githubOnceErr = fmt.Errorf(fmtStr, err, shortFilename)
			return
		}
		fi, err := os.Stat(filename)
		if err != nil {
			githubOnceErr = err
			return
		}
		if fi.Mode()&0077 != 0 {
			githubOnceErr = fmt.Errorf("reading token: %s mode is %#o, want %#o", shortFilename, fi.Mode()&0777, fi.Mode()&0700)
			return
		}
		githubToken = strings.TrimSpace(string(data))
	})
	return githubToken, githubOnceErr
}

func getToken(ctx context.Context) (string, error) {
	if metadata.OnGCE() {
		// Re-use maintner-github-token until this is migrated to using the maintner API.
		token, err := metadata.ProjectAttributeValue("maintner-github-token")
		if len(token) > 0 && err == nil {
			return token, nil
		}
	}
	return getTokenFromFile(ctx)
}
