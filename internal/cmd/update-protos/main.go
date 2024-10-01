// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The update-protos tool updates .pb.go files in
// the golang.org/x/build source tree.
package main

import (
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	bindir, err := os.MkdirTemp("", "update-protos")
	if err != nil {
		log.Fatal(err)
	}

	var protos []string
	filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if strings.HasSuffix(path, ".proto") {
			protos = append(protos, path)
		}
		return err
	})

	// Install the code generator plugins into a temp dir,
	// to ensure we always use the versions from our go.mod.
	os.Setenv("GOBIN", bindir)
	run("go", "install", "google.golang.org/protobuf/cmd/protoc-gen-go")
	run("go", "install", "google.golang.org/grpc/cmd/protoc-gen-go-grpc")

	// We could also install protoc here, to ensure we use a consistent version.

	cmd := append([]string{
		"--plugin=go=" + filepath.Join(bindir, "protoc-gen-go"),
		"--plugin=go-grpc=" + filepath.Join(bindir, "protoc-gen-go"),
		"--go_opt=module=golang.org/x/build",
		"--go-grpc_opt=module=golang.org/x/build",
		"--go_out=.",
		"--go-grpc_out=.",
	}, protos...)
	run("protoc", cmd...)
}

func run(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to run: %v %v", name, strings.Join(args, " "))
	}

}
