// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package darwinpkg_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/build/internal/installer/darwinpkg"
)

var (
	inFlag  = flag.String("in", "", "Path to the .tar.gz archive containing a built Go toolchain.")
	outFlag = flag.String("out", filepath.Join(os.TempDir(), "out.pkg"), "Path where to write out the result.")
)

func TestConstructInstaller(t *testing.T) {
	if *inFlag == "" || *outFlag == "" {
		t.Skip("skipping manual test since -in/-out flags are not set")
	}

	out, err := darwinpkg.ConstructInstaller(context.Background(), t.TempDir(), *inFlag, darwinpkg.InstallerOptions{
		GOARCH:          "arm64",
		MinMacOSVersion: "12",
	})
	if err != nil {
		t.Fatal("ConstructInstaller:", err)
	}
	if err := os.Rename(out, *outFlag); err != nil {
		t.Fatal("moving result to output location failed:", err)
	}
	t.Log("constructed installer at:", *outFlag)
}
