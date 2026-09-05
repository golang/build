// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gcsfs

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

func TestGCSReadDir(t *testing.T) {
	// GCS lists child directories as prefixes, but returns a directory's own
	// marker object in items when its name equals the query prefix.
	for _, test := range []struct {
		name, dir, response string
		want                []string
	}{
		{
			name:     "root",
			dir:      ".",
			response: `{"prefixes":["dir/","empty/","implicit/"],"items":[{"name":"zero.txt","size":"0"}]}`,
			want:     []string{"dir/", "empty/", "implicit/", "zero.txt"},
		},
		{
			name:     "implicit",
			dir:      "implicit",
			response: `{"items":[{"name":"implicit/file.txt","size":"4"}]}`,
			want:     []string{"file.txt"},
		},
		{
			name:     "explicit",
			dir:      "dir",
			response: `{"items":[{"name":"dir/","size":"0"},{"name":"dir/file.txt","size":"4"}]}`,
			want:     []string{"file.txt"},
		},
		{
			name:     "empty",
			dir:      "empty",
			response: `{"items":[{"name":"empty/","size":"0"}]}`,
		},
		{
			name:     "nested",
			dir:      "dir/nested",
			response: `{"prefixes":["dir/nested/empty/"],"items":[{"name":"dir/nested/","size":"0"},{"name":"dir/nested/file.txt","size":"4"}]}`,
			want:     []string{"empty/", "file.txt"},
		},
		{
			name:     "nested_empty",
			dir:      "dir/nested/empty",
			response: `{"items":[{"name":"dir/nested/empty/","size":"0"}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				prefix := ""
				if test.dir != "." {
					prefix = test.dir + "/"
				}
				if r.Method != "GET" || r.URL.Path != "/b/test/o" || r.URL.Query().Get("prefix") != prefix || r.URL.Query().Get("delimiter") != "/" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, test.response)
			}))
			defer server.Close()
			client, err := storage.NewClient(t.Context(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			fsys := NewFS(t.Context(), client, "test")
			for _, sub := range []bool{false, true} {
				for _, n := range []int{-1, 0, 1, 2} {
					t.Run(fmt.Sprintf("sub=%v/n=%d", sub, n), func(t *testing.T) {
						fsys, dir := fsys, test.dir
						if sub {
							var err error
							fsys, err = fs.Sub(fsys, dir)
							if err != nil {
								t.Fatal(err)
							}
							dir = "."
						}
						f, err := fsys.Open(dir)
						if err != nil {
							t.Fatal(err)
						}
						defer f.Close()
						var got []string
						for {
							entries, err := f.(fs.ReadDirFile).ReadDir(n)
							if n > 0 && err == io.EOF {
								// OK.
							} else if err != nil {
								t.Fatal(err)
							}
							if n > 0 && (len(entries) > n || len(entries) == 0 && err == nil) {
								t.Fatalf("ReadDir(%d) returned %d entries, %v", n, len(entries), err)
							}
							for _, entry := range entries {
								name := entry.Name()
								if entry.IsDir() {
									name += "/"
								}
								got = append(got, name)
							}
							if n <= 0 || err == io.EOF {
								break
							}
						}
						slices.Sort(got)
						if !slices.Equal(got, test.want) {
							t.Errorf("ReadDir: got %q, want %q", got, test.want)
						}
					})
				}
			}
		})
	}
}

func TestGCSFS(t *testing.T) {
	if testing.Short() {
		t.Skip("reads a real GCS bucket over the internet")
	}

	client, err := storage.NewClient(t.Context(), option.WithScopes(storage.ScopeReadOnly), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	// Note: It may be somewhat preferable to have a dedicated GCS bucket for this test,
	// as that would make it viable to test NewFS without having to wrap it with fs.Sub.
	// In the meantime, settle on using a dedicated directory in an existing GCS bucket.
	fsys, err := fs.Sub(NewFS(t.Context(), client, "go-build-log"), "gcsfs-testdata")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"a",
		"b",
		"dir/x",
	}
	if err := fstest.TestFS(fsys, expected...); err != nil {
		t.Error(err)
	}

	sub, err := fs.Sub(fsys, "dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(sub, "x"); err != nil {
		t.Error(err)
	}
}

func TestDirFS(t *testing.T) {
	if err := fstest.TestFS(DirFS("./testdata/dirfs"), "a", "b", "dir/x"); err != nil {
		t.Fatal(err)
	}
}

func TestDirFSDotFiles(t *testing.T) {
	temp := t.TempDir()
	if err := os.WriteFile(temp+"/.foo", nil, 0777); err != nil {
		t.Fatal(err)
	}
	files, err := fs.ReadDir(DirFS(temp), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("ReadDir didn't hide . files: %v", files)
	}
}

func TestDirFSWrite(t *testing.T) {
	temp := t.TempDir()
	fsys := DirFS(temp)
	f, err := Create(fsys, "fsystest.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hey\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(temp, "fsystest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hey\n" {
		t.Fatalf("unexpected file contents %q, want %q", string(b), "hey\n")
	}
}
