// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !cgo

package windowsmsi

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var (
	inFlag  = flag.String("in", "", "Path to the .tar.gz archive containing a built Go toolchain.")
	outFlag = flag.String("out", filepath.Join(os.TempDir(), "out.msi"), "Path where to write out the result.")
)

func TestConstructInstaller(t *testing.T) {
	if *inFlag == "" || *outFlag == "" {
		t.Skip("skipping manual test since -in/-out flags are not set")
	}

	out, err := ConstructInstaller(context.Background(), t.TempDir(), *inFlag, InstallerOptions{
		GOARCH: "amd64",
	})
	if err != nil {
		t.Fatal("ConstructInstaller:", err)
	}
	if err := os.Rename(out, *outFlag); err != nil {
		t.Fatal("moving result to output location failed:", err)
	}
	t.Log("constructed installer at:", *outFlag)
}

func TestSplitVersion(t *testing.T) {
	// Test splitVersion.
	for _, tt := range []struct {
		v            string
		minor, patch int
	}{
		{"go1", 0, 0},
		{"go1.34", 34, 0},
		{"go1.34.7", 34, 7},
	} {
		minor, patch := splitVersion(tt.v)
		if minor != tt.minor || patch != tt.patch {
			t.Errorf("splitVersion(%q) = %v, %v; want %v, %v",
				tt.v, minor, patch, tt.minor, tt.patch)
		}
	}
}
