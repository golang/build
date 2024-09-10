// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"
	goversion "go/version"
	"os"
	"path/filepath"
	"strings"

	wf "golang.org/x/build/internal/workflow"
	"golang.org/x/mod/modfile"
)

type UpdateProxyTestRepoTasks struct {
	Git       *Git
	GerritURL string
	Branch    string
}

func (t *UpdateProxyTestRepoTasks) UpdateProxyTestRepo(ctx *wf.TaskContext, published Published) (string, error) {
	repo, err := t.Git.Clone(ctx, t.GerritURL)
	if err != nil {
		return "", err
	}

	// Read the file and check if version is higher.
	modFile := filepath.Join(repo.dir, "go.mod")
	contents, err := os.ReadFile(modFile)
	if err != nil {
		return "", err
	}
	f, err := modfile.ParseLax(modFile, contents, nil)
	if err != nil {
		return "", err
	}
	// If the published version is lower than the current go.mod version, don't update.
	// If we could parse the go.mod file, assume we should update.
	if f.Go != nil && goversion.Compare(published.Version, "go"+f.Go.Version) < 0 {
		return "no update", nil
	}

	version := strings.TrimPrefix(published.Version, "go")
	// Update the go.mod file for the new release.
	if err := os.WriteFile(modFile, []byte(fmt.Sprintf("module test\n\ngo %s\n", version)), 0666); err != nil {
		return "", err
	}
	if _, err := repo.RunCommand(ctx, "commit", "-am", fmt.Sprintf("update go version to %s", version)); err != nil {
		return "", fmt.Errorf("git commit error: %v", err)
	}
	// Force move the tag.
	if _, err := repo.RunCommand(ctx, "tag", "-af", "v1.0.0", "-m", fmt.Sprintf("moving tag to include go version %s", version)); err != nil {
		return "", fmt.Errorf("git tag error: %v", err)
	}
	if _, err := repo.RunCommand(ctx, "push", "--force", "--tags", "origin", t.Branch); err != nil {
		return "", fmt.Errorf("git push --tags error: %v", err)
	}
	return "updated", nil
}
