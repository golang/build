// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

type golangorgBuilder struct{}

func prefix8(s string) string {
	if len(s) < 8 {
		return s
	}
	return s[:8]
}

func (b golangorgBuilder) Signature(heads map[string]string) string {
	return fmt.Sprintf("go=%v/website=%v", prefix8(heads["go"]), prefix8(heads["website"]))
}

func (b golangorgBuilder) Init(logger *log.Logger, dir, hostport string, heads map[string]string) (*exec.Cmd, error) {
	goDir := filepath.Join(dir, "go")
	websiteDir := filepath.Join(dir, "website")
	logger.Printf("checking out go repo ...")
	if err := checkout(repoURL+"go", heads["go"], goDir); err != nil {
		return nil, fmt.Errorf("checkout of go: %v", err)
	}
	logger.Printf("checking out website repo ...")
	if err := checkout(repoURL+"website", heads["website"], websiteDir); err != nil {
		return nil, fmt.Errorf("checkout of website: %v", err)
	}

	var logWriter io.Writer = toLoggerWriter{logger}

	make := exec.Command(filepath.Join(goDir, "src/make.bash"))
	make.Dir = filepath.Join(goDir, "src")
	make.Stdout = logWriter
	make.Stderr = logWriter
	logger.Printf("running make.bash in %s ...", make.Dir)
	if err := make.Run(); err != nil {
		return nil, fmt.Errorf("running make.bash: %v", err)
	}

	logger.Printf("installing golangorg ...")
	goBin := filepath.Join(goDir, "bin/go")
	binDir := filepath.Join(dir, "bin")
	install := exec.Command(goBin, "install", "golang.org/x/website/cmd/golangorg")
	install.Stdout = logWriter
	install.Stderr = logWriter
	install.Env = append(os.Environ(),
		"GOROOT="+goDir,
		"GO111MODULE=on",
		"GOBIN="+binDir,
	)
	if err := install.Run(); err != nil {
		return nil, fmt.Errorf("go install golang.org/x/website/cmd/golangorg: %v", err)
	}

	logger.Printf("starting golangorg ...")
	golangorgBin := filepath.Join(binDir, "golangorg")
	golangorg := exec.Command(golangorgBin, "-http="+hostport, "-index", "-index_interval=-1s", "-play")
	golangorg.Env = append(os.Environ(), "GOROOT="+goDir)
	golangorg.Stdout = logWriter
	golangorg.Stderr = logWriter
	if err := golangorg.Start(); err != nil {
		return nil, fmt.Errorf("starting golangorg: %v", err)
	}
	return golangorg, nil
}

var indexingMsg = []byte("Indexing in progress: result may be inaccurate")

func (b golangorgBuilder) HealthCheck(hostport string) error {
	body, err := getOK(fmt.Sprintf("http://%v/search?q=FALLTHROUGH", hostport))
	if err != nil {
		return err
	}
	if bytes.Contains(body, indexingMsg) {
		return errors.New("still indexing")
	}
	return nil
}
