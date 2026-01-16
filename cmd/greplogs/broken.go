// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed _embed/broken.go
var brokenScript []byte

// listBrokenBuilders returns the builders that are marked
// as broken in golang.org/x/build/dashboard at HEAD.
func listBrokenBuilders() (broken []string, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("identifying broken builders: %v", err)
		}
	}()

	// Though this be madness, yet there is method in 't.
	//
	// Our goals here are:
	//
	// 	1. Always use the most up-to-date information about broken builders, even
	// 	   if the user hasn't recently updated the greplogs binary.
	//
	// 	2. Avoid the need to massively refactor the builder configuration right
	// 	   now. (Currently, the Go builders are configured programmatically in the
	// 	   x/build/dashboard package, not in external configuration files.)
	//
	// 	3. Avoid the need to redeploy a production x/build/cmd/coordinator or
	// 	   x/build/cmd/devapp to pick up changes. (A user triaging test failures might
	// 	   not have access to deploy the coordinator, or might not want to disrupt
	// 	   running tests or active gomotes by pushing it.)
	//
	// Goals (2) and (3) imply that we must use x/build/dashboard, not fetch the
	// list from build.golang.org or dev.golang.org. Since that is a Go package,
	// we must run it as a Go program in order to evaluate it.
	//
	// Goal (1) implies that we must use x/build at HEAD, not (say) at whatever
	// version of x/build this command was built with. We could perhaps relax that
	// constraint if we move greplogs itself into x/build and consistently triage
	// using 'go run golang.org/x/build/cmd/greplogs@HEAD' instead of an installed
	// 'greplogs'.

	if os.Getenv("GO111MODULE") == "off" {
		return nil, errors.New("operation requires GO111MODULE=on or auto")
	}

	modDir, err := os.MkdirTemp("", "greplogs")
	if err != nil {
		return nil, err
	}
	defer func() {
		removeErr := os.RemoveAll(modDir)
		if err == nil {
			err = removeErr
		}
	}()

	runCommand := func(name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = modDir
		cmd.Env = append(os.Environ(),
			"PWD="+modDir,                  // match cmd.Dir
			"GOPRIVATE=golang.org/x/build", // avoid proxy cache; see https://go.dev/issue/38065
		)
		cmd.Stderr = new(strings.Builder)

		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%s: %w\nstderr:\n%s", strings.Join(cmd.Args, " "), err, cmd.Stderr)
		}
		return out, nil
	}

	_, err = runCommand("go", "mod", "init", "github.com/aclements/go-misc/greplogs/_embed")
	if err != nil {
		return nil, err
	}

	_, err = runCommand("go", "get", "golang.org/x/build/dashboard@HEAD")
	if err != nil {
		return nil, err
	}

	err = os.WriteFile(filepath.Join(modDir, "broken.go"), brokenScript, 0644)
	if err != nil {
		return nil, err
	}

	out, err := runCommand("go", "run", "broken.go")
	if err != nil {
		return nil, err
	}

	broken = strings.Split(strings.TrimSpace(string(out)), "\n")
	sort.Strings(broken)
	return broken, nil
}
