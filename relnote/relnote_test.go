// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package relnote

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
			"must contain a complete sentence",
		},
		{
			"# heading",
			"must contain a complete sentence",
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
	want := "#### [`net/http`](/pkg/net/http/)\n"
	if got != want {
		t.Errorf("\ngot  %q\nwant %q", got, want)
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
pkg syscall (windows-386), const WSAENOPROTOOPT = 10042 #62254
	`)},
	}
	got, err := parseAPIFile(fsys, "123.next")
	if err != nil {
		t.Fatal(err)
	}
	want := []APIFeature{
		{"p1", "", "type T struct", 0},
		{"p2", "", "func F(int, bool)", 123},
		{"syscall", "(windows-386)", "const WSAENOPROTOOPT = 10042", 62254},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\ngot  %#v\nwant %#v", got, want)
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
			gotErr := CheckAPIFile(fsys, "api.txt", fsys, "doc/next")
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

func TestAllAPIFilesForErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	fsys := os.DirFS(filepath.Join(runtime.GOROOT(), "api"))
	apiFiles, err := fs.Glob(fsys, "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range apiFiles {
		if _, err := parseAPIFile(fsys, f); err != nil {
			t.Errorf("parseTestFile(%q) failed with %v", f, err)
		}
	}
}

func TestSymbolLinks(t *testing.T) {
	for _, test := range []struct {
		in   string
		want string
	}{
		{"a b", "a b"},
		{"a [b", "a [b"},
		{"a [b[", "a [b["},
		{"a b[X]", "a b[X]"},
		{"a [Buffer] b", "a [`Buffer`](/pkg/bytes#Buffer) b"},
		{"a [Buffer]\nb", "a [`Buffer`](/pkg/bytes#Buffer)\nb"},
		{"a [bytes.Buffer], b", "a [`bytes.Buffer`](/pkg/bytes#Buffer), b"},
		{"[bytes.Buffer.String]", "[`bytes.Buffer.String`](/pkg/bytes#Buffer.String)"},
		{"a--[encoding/json.Marshal].", "a--[`encoding/json.Marshal`](/pkg/encoding/json#Marshal)."},
		{"a [math] and s[math] and [NewBuffer].", "a [`math`](/pkg/math) and s[math] and [`NewBuffer`](/pkg/bytes#NewBuffer)."},
		{"A [*log/slog.Logger]", "A [`*log/slog.Logger`](/pkg/log/slog#Logger)"},
		{"Not in code `[math]`.", "Not in code `[math]`."},
		// Link text that already has backticks.
		{"a [`Buffer`] b", "a [`Buffer`](/pkg/bytes#Buffer) b"},
		{"[`bytes.Buffer.String`]", "[`bytes.Buffer.String`](/pkg/bytes#Buffer.String)"},
		// Links inside inline elements with nested content.
		{"**must use [Buffer]**", "**must use [`Buffer`](/pkg/bytes#Buffer)**"},
		{"*must use [Buffer] value*", "*must use [`Buffer`](/pkg/bytes#Buffer) value*"},
		{"_**[Buffer]**_", "_**[`Buffer`](/pkg/bytes#Buffer)**_"},
	} {
		doc := NewParser().Parse(test.in)
		addSymbolLinks(doc, "bytes")
		got := strings.TrimSpace(md.ToMarkdown(doc))
		if got != test.want {
			t.Errorf("\nin:   %s\ngot:  %s\nwant: %s", test.in, got, test.want)
		}
	}

}
