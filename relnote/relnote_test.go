// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relnote

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/tools/txtar"
	md "rsc.io/markdown"
)

func TestCheckFragment(t *testing.T) {
	for _, test := range []struct {
		in string
		// part of err.Error(), or empty if success
		want string
	}{
		{
			// has a TODO
			"# heading\nTODO(jba)",
			"",
		},
		{
			// has a sentence
			"# heading\nSomething.",
			"",
		},
		{
			// sentence is inside some formatting
			"# heading\n- _Some_*thing.*",
			"",
		},
		{
			// multiple sections have what they need
			"# H1\n\nTODO\n\n## H2\nOk.",
			"",
		},
		{
			// questions and exclamations are OK
			"# H1\n Are questions ok? \n# H2\n Must write this note!",
			"",
		},
		{
			"TODO\n# heading",
			"does not start with a heading",
		},
		{
			"#   \t\nTODO",
			"starts with an empty heading",
		},
		{
			"# +heading\nTODO",
			"starts with a non-matching head",
		},
		{
			"# heading",
			"needs",
		},
		{
			"# H1\n non-final section has a problem\n## H2\n TODO",
			"needs",
		},
	} {
		got := CheckFragment(test.in)
		if test.want == "" {
			if got != nil {
				t.Errorf("%q: got %q, want nil", test.in, got)
			}
		} else if got == nil || !strings.Contains(got.Error(), test.want) {
			t.Errorf("%q: got %q, want error containing %q", test.in, got, test.want)
		}
	}
}

func TestMerge(t *testing.T) {
	testFiles, err := filepath.Glob(filepath.Join("testdata", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range testFiles {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".txt"), func(t *testing.T) {
			fsys, want, err := parseTestFile(f)
			if err != nil {
				t.Fatal(err)
			}
			gotDoc, err := Merge(fsys)
			if err != nil {
				t.Fatal(err)
			}
			got := md.ToMarkdown(gotDoc)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("mismatch (-want, +got)\n%s", diff)
			}
		})
	}
}

func parseTestFile(filename string) (fsys fs.FS, want string, err error) {
	ar, err := txtar.ParseFile(filename)
	if err != nil {
		return nil, "", err
	}
	mfs := make(fstest.MapFS)
	for _, f := range ar.Files {
		if f.Name == "want" {
			want = string(f.Data)
		} else {
			mfs[f.Name] = &fstest.MapFile{Data: f.Data}
		}
	}
	if want == "" {
		return nil, "", fmt.Errorf("%s: missing 'want'", filename)
	}
	return mfs, want, nil
}

func TestSortedMarkdownFilenames(t *testing.T) {
	want := []string{
		"a.md",
		"b.md",
		"b/a.md",
		"b/c.md",
		"ba/a.md",
	}
	mfs := make(fstest.MapFS)
	for _, fn := range want {
		mfs[fn] = &fstest.MapFile{}
	}
	mfs["README"] = &fstest.MapFile{}
	mfs["b/other.txt"] = &fstest.MapFile{}
	got, err := sortedMarkdownFilenames(mfs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("\ngot  %v\nwant %v", got, want)
	}

}
