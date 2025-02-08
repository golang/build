// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task_test

import (
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/internal/task"
)

var readPKGFlag = flag.String("read-pkg", "", "Path to a Go macOS .pkg installer to run TestReadBinariesFromPKG with.")

func TestReadBinariesFromPKG(t *testing.T) {
	if *readPKGFlag == "" {
		t.Skip("skipping manual test since -read-pkg flag is not set")
	}
	if _, err := exec.LookPath("pkgutil"); err != nil {
		// Since this is a manual test, we can afford to fail
		// rather than skip if required dependencies are missing.
		t.Fatal("required dependency pkgutil not found in PATH:", err)
	}
	if ext := filepath.Ext(*readPKGFlag); ext != ".pkg" {
		t.Fatalf("got input file extension %q, want .pkg", ext)
	}
	f, err := os.Open(*readPKGFlag)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := task.ReadBinariesFromPKG(f)
	if err != nil {
		t.Fatal(err)
	}
	want, err := readBinariesFromPKGUsingXcode(t, *readPKGFlag)
	if err != nil {
		t.Fatal(err)
	}
	// Compare with reflect.DeepEqual first for speed;
	// there's 100 MB or so of binary data to compare.
	if !reflect.DeepEqual(want, got) {
		t.Log("got files:")
		for path := range got {
			t.Log("\t" + path)
		}
		t.Log("want files:")
		for path := range want {
			t.Log("\t" + path)
		}
		t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got))
	}
}

// readBinariesFromPKGUsingXcode implements the same functionality as
// ReadBinariesFromPKG but uses Xcode's pkgutil as its implementation.
func readBinariesFromPKGUsingXcode(t *testing.T, pkgPath string) (map[string][]byte, error) {
	expanded := filepath.Join(t.TempDir(), "expanded")
	out, err := exec.Command("pkgutil", "--expand-full", pkgPath, expanded).CombinedOutput()
	if err != nil {
		t.Fatalf("pkgutil failed: %v\noutput: %s", err, out)
	}
	var binaries = make(map[string][]byte) // Relative path starting with "go/" → binary data.
	root := filepath.Join(expanded, "org.golang.go.pkg/Payload/usr/local")
	err = filepath.Walk(root, func(path string, fi fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(name, "go/bin/") && !strings.HasPrefix(name, "go/pkg/tool/") {
			return nil
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm()&0100 == 0 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		binaries[name] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return binaries, nil
}
