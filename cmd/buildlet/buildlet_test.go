// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"runtime"
	"testing"
)

func TestPathEnv(t *testing.T) {
	for _, c := range []struct {
		goos string // default "linux"
		wd   string // default "/workdir"
		env  []string
		path []string
		want string
		noOp bool
	}{
		{ // No change to PATH
			env:  []string{"A=1", "PATH=/bin:/usr/bin", "B=2"},
			path: []string{"$PATH"},
			want: "PATH=/bin:/usr/bin",
			noOp: true,
		},
		{ // Test that $EMPTY rewrites the path to be empty
			env:  []string{"A=1", "PATH=/bin:/usr/bin", "B=2"},
			path: []string{"$EMPTY"},
			want: "PATH=",
		},
		{ // Test that clearing an already-unset PATH is a no-op
			env:  []string{"A=1", "B=2"},
			path: []string{"$EMPTY"},
			want: "PATH=",
			noOp: true,
		},
		{ // Test $WORKDIR expansion
			env:  []string{"A=1", "PATH=/bin:/usr/bin", "B=2"},
			path: []string{"/go/bin", "$WORKDIR/foo"},
			want: "PATH=/go/bin:/workdir/foo",
		},
		{ // Test $PATH expansion
			env:  []string{"A=1", "PATH=/bin:/usr/bin", "B=2"},
			path: []string{"/go/bin", "$PATH", "$WORKDIR/foo"},
			want: "PATH=/go/bin:/bin:/usr/bin:/workdir/foo",
		},
		{ // Test $PATH expansion (prepend only)
			env:  []string{"A=1", "PATH=/bin:/usr/bin", "B=2"},
			path: []string{"/go/bin", "/a/b", "$PATH"},
			want: "PATH=/go/bin:/a/b:/bin:/usr/bin",
		},
		{ // Test $PATH expansion (append only)
			env:  []string{"A=1", "PATH=/bin:/usr/bin", "B=2"},
			path: []string{"$PATH", "/go/bin", "/a/b"},
			want: "PATH=/bin:/usr/bin:/go/bin:/a/b",
		},
		{ // Test that empty $PATH expansion is a no-op
			env:  []string{"A=1", "B=2"},
			path: []string{"$PATH"},
			want: "PATH=",
			noOp: true,
		},
		{ // Test that empty $PATH expansion does not add extraneous separators
			env:  []string{"A=1", "B=2"},
			path: []string{"$PATH", "$WORKDIR/foo"},
			want: "PATH=/workdir/foo",
		},
		{ // Test that in case of multiple PATH entries we modify the last one,
			// not the first.
			env:  []string{"PATH=/bin:/usr/bin", "PATH=/bin:/usr/bin:/usr/local/bin"},
			path: []string{"$WORKDIR/foo", "$PATH"},
			want: "PATH=/workdir/foo:/bin:/usr/bin:/usr/local/bin",
		},
		{ // Test that Windows reads the existing variable regardless of case
			goos: "windows",
			wd:   `C:\workdir`,
			env:  []string{"A=1", `PaTh=C:\Go\bin;C:\windows`, "B=2"},
			path: []string{"$PATH", `$WORKDIR\foo`},
			want: `PATH=C:\Go\bin;C:\windows;C:\workdir\foo`,
		},
		{ // Test that plan9 uses plan9 separators and "path" instead of "PATH"
			goos: "plan9",
			env:  []string{"path=/bin\x00/usr/bin", "PATH=/bananas"},
			path: []string{"$PATH", "$WORKDIR/foo"},
			want: "path=/bin\x00/usr/bin\x00/workdir/foo",
		},
	} {
		goos := c.goos
		if goos == "" {
			goos = "linux"
		}
		wd := c.wd
		if wd == "" {
			wd = "/workdir"
		}
		got, gotOk := pathEnv(goos, c.env, c.path, wd)
		wantOk := !c.noOp
		if got != c.want || gotOk != wantOk {
			t.Errorf("pathEnv(%q, %q, %q, %q) =\n\t%q, %t\nwant:\n\t%q, %t", goos, c.env, c.path, wd, got, gotOk, c.want, wantOk)
		}
	}
}

func TestPathListSeparator(t *testing.T) {
	sep := pathListSeparator(runtime.GOOS)
	want := string(os.PathListSeparator)
	if sep != want {
		t.Errorf("pathListSeparator(%q) = %q; want %q", runtime.GOOS, sep, want)
	}
}
