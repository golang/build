// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relnote

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
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
			// questions and exclamations are OK
			"# H1\n Are questions ok? \n# H2\n Must write this note!",
			"",
		},
		{
			"",
			"empty",
		},
		{
			"# heading",
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
	testFiles, err := filepath.Glob(filepath.Join("testdata", "merge", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(testFiles) == 0 {
		t.Fatal("no tests")
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

func TestStdlibPackage(t *testing.T) {
	for _, test := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"net/a.md", ""},
		{"stdlib/net/a.md", ""},
		{"stdlib/minor/net/a.md", "net"},
		{"stdlib/minor/heading.md", ""},
		{"stdlib/minor/net/http/a.md", "net/http"},
	} {
		got := stdlibPackage(test.in)
		if w := test.want; got != w {
			t.Errorf("%q: got %q, want %q", test.in, got, w)
		}
	}
}

func TestStdlibPackageHeading(t *testing.T) {
	h := stdlibPackageHeading("net/http", 1)
	got := md.ToMarkdown(h)
	want := "#### [net/http](/pkg/net/http/)\n"
	if got != want {
		t.Errorf("\ngot  %q\nwant %q", got, want)
	}
}

func dump(d *md.Document) {
	for _, b := range d.Blocks {
		fmt.Printf("## %T   %v\n", b, b.Pos())
		switch b := b.(type) {
		case *md.Paragraph:
			fmt.Printf("   %q\n", text(b.Text))
		case *md.Heading:
			for _, in := range b.Text.Inline {
				fmt.Printf("    %#v\n", in)
			}
		}
	}
}

// parseTestFile translates a txtar archive into an fs.FS, except for the
// file "want", whose contents are returned separately.
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

func TestRemoveEmptySections(t *testing.T) {
	doc := NewParser().Parse(`
# h1
not empty

# h2

## h3

### h4

#### h5

### h6

### h7

## h8
something

## h9

# h10
`)
	bs := removeEmptySections(doc.Blocks)
	got := md.ToMarkdown(&md.Document{Blocks: bs})
	want := md.ToMarkdown(NewParser().Parse(`
# h1
not empty

# h2

## h8
something
`))
	if got != want {
		t.Errorf("\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseAPIFile(t *testing.T) {
	fsys := fstest.MapFS{
		"123.next": &fstest.MapFile{Data: []byte(`
pkg p1, type T struct
pkg p2, func F(int, bool) #123
	`)},
	}
	got, err := parseAPIFile(fsys, "123.next")
	if err != nil {
		t.Fatal(err)
	}
	want := []APIFeature{
		{"p1", "type T struct", 0},
		{"p2", "func F(int, bool)", 123},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\ngot  %+v\nwant %+v", got, want)
	}
}

func TestCheckAPIFile(t *testing.T) {
	testFiles, err := filepath.Glob(filepath.Join("testdata", "checkAPIFile", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(testFiles) == 0 {
		t.Fatal("no tests")
	}
	for _, f := range testFiles {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".txt"), func(t *testing.T) {
			fsys, want, err := parseTestFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			gotErr := CheckAPIFile(fsys, "api.txt", fsys)
			if gotErr != nil {
				got = gotErr.Error()
			}
			want = strings.TrimSpace(want)
			if got != want {
				t.Errorf("\ngot  %s\nwant %s", got, want)
			}
		})
	}
}
