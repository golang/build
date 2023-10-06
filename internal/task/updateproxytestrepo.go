// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wf "golang.org/x/build/internal/workflow"
)

type UpdateProxyTestRepoTasks struct {
	Git       *Git
	GerritURL string
	Branch    string
}

func (t *UpdateProxyTestRepoTasks) NewDefinition() *wf.Definition {
	wd := wf.New()
	p := Published{
		Version: "go1.21.3",
	}
	done := wf.Task1(wd, "update", t.UpdateProxyTestRepo, wf.Const(p))
	wf.Output(wd, fmt.Sprintf("Updated proxy test repo to %s", p.Version), done)
	return wd
}

func (t *UpdateProxyTestRepoTasks) UpdateProxyTestRepo(ctx *wf.TaskContext, published Published) (string, error) {
	version := strings.TrimPrefix(published.Version, "go")

	repo, err := t.Git.Clone(ctx, t.GerritURL)
	if err != nil {
		return "", err
	}

	// Update the go.mod file for the new release.
	if err := os.WriteFile(filepath.Join(repo.dir, "go.mod"), []byte(fmt.Sprintf("module test\n\ngo %s\n", version)), 0666); err != nil {
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

	return "finished", nil
}
