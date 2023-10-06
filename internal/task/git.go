// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/oauth2"
)

// A Git manages a set of Git repositories.
type Git struct {
	ts         oauth2.TokenSource
	cookieFile string
}

// UseOAuth2Auth configures Git authentication using ts.
func (g *Git) UseOAuth2Auth(ts oauth2.TokenSource) error {
	g.ts = ts
	f, err := os.CreateTemp("", "gitcookies")
	if err != nil {
		return err
	}
	g.cookieFile = f.Name()
	return f.Close()
}

// Clone checks out the repository at origin into a temporary directory owned
// by the resulting GitDir.
func (g *Git) Clone(ctx context.Context, origin string) (*GitDir, error) {
	dir, err := os.MkdirTemp("", "relui-git-clone-*")
	if err != nil {
		return nil, err
	}
	if _, err := g.run(ctx, "", "clone", origin, dir); err != nil {
		return nil, err
	}
	return &GitDir{g, dir}, err
}

func (g *Git) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := g.runGitStreamed(ctx, stdout, stderr, dir, args...); err != nil {
		return stdout.Bytes(), fmt.Errorf("git command failed: %v, stderr %v", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (g *Git) runGitStreamed(ctx context.Context, stdout, stderr io.Writer, dir string, args ...string) error {
	if g.ts != nil {
		tok, err := g.ts.Token()
		if err != nil {
			return err
		}
		// https://github.com/curl/curl/blob/master/docs/HTTP-COOKIES.md
		cookieLine := fmt.Sprintf(".googlesource.com\tTRUE\t/\tTRUE\t%v\to\t%v\n", tok.Expiry.Unix(), tok.AccessToken)
		if err := os.WriteFile(g.cookieFile, []byte(cookieLine), 0o700); err != nil {
			return fmt.Errorf("error writing git cookies: %v", err)
		}
		args = append([]string{"-c", "http.cookiefile=" + g.cookieFile}, args...)
	}
	args = append([]string{
		"-c", "user.email=gobot@golang.org",
		"-c", "user.name='Gopher Robot'",
	}, args...)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// A GitDir is a single Git repository.
type GitDir struct {
	git *Git
	dir string
}

// RunCommand runs a Git command, returning its stdout if it succeeds, or an
// error containing its stderr if it fails.
func (g *GitDir) RunCommand(ctx context.Context, args ...string) ([]byte, error) {
	return g.git.run(ctx, g.dir, args...)
}

// Close cleans up the repository.
func (g *GitDir) Close() error {
	return os.RemoveAll(g.dir)
}
