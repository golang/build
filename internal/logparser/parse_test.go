// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logparser

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/build/internal/diff"
)

func Test(t *testing.T) {
	// testdata/x.log is a build log, and
	// testdata/x.fail is fmtFails(Parse(log)).
	// Check that we get the same result as in x.fail.
	files, _ := filepath.Glob("testdata/*.log")
	if len(files) == 0 {
		t.Fatalf("no testdata")
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(strings.TrimSuffix(file, ".log") + ".fail")
			if err != nil {
				t.Fatal(err)
			}
			have := fmtFails(Parse(string(data)))
			if !bytes.Equal(have, want) {
				t.Errorf("mismatch:\n%s", diff.Diff("want", want, "have", have))
			}
		})
	}
}

func fmtFails(fails []*Fail) []byte {
	var b bytes.Buffer
	for i, f := range fails {
		if i > 0 {
			fmt.Fprintf(&b, "---\n")
		}
		fmt.Fprintf(&b, "Section: %q\nPkg: %q\nTest: %q\nMode: %q\n", f.Section, f.Pkg, f.Test, f.Mode)
		fmt.Fprintf(&b, "Snippet:\n%s", indent(f.Snippet))
		fmt.Fprintf(&b, "Output:\n%s", indent(f.Output))
	}
	return b.Bytes()
}

// indent indents s with a leading tab on every line.
func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	s = "\t" + strings.ReplaceAll(s, "\n", "\n\t") + "\n"
	s = strings.ReplaceAll(s, "\t\n", "\n")
	return s
}
