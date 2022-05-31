// Copyright 2022 Go Authors All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/tar"
	"testing"

	"golang.org/x/build/internal/releasetargets"
)

func TestFixPermissions(t *testing.T) {
	tests := []struct {
		hdr      *tar.Header
		wantMode int64
	}{
		{&tar.Header{Name: "dir", Typeflag: tar.TypeDir, Mode: 0}, 0755},
		{&tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0}, 0644},
		{&tar.Header{Name: "xfile", Typeflag: tar.TypeReg, Mode: 0700}, 0755},
	}
	for _, tt := range tests {
		t.Run(tt.hdr.Name, func(t *testing.T) {
			adjusted := fixPermissions()(tt.hdr)
			if adjusted.Mode != tt.wantMode {
				t.Errorf("mode = %o, want %o", adjusted.Mode, tt.wantMode)
			}
		})
	}
}

func TestDropRegexpMatches(t *testing.T) {
	tests := []struct {
		name string
		keep bool
	}{
		{".gitattributes", false},
		{"VERSION", true},
		{"bin/go", true},
		{"pkg/obj/README", false},
		{"pkg/tool/linux_amd64/api", false},
		{"pkg/linux_amd64/cmd/go.a", false},
		{"pkg/linux_amd64/runtime.a", true},
		{"bin/go.exe~", false},
		{"bin/go.exe", true},
	}
	adjust := dropRegexpMatches(dropPatterns)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adjusted := adjust(&tar.Header{Name: tt.name})
			if (adjusted != nil) != tt.keep {
				t.Errorf("file %q: keep = %v, want %v", tt.name, adjusted != nil, tt.keep)
			}
		})
	}
}

func TestDropUnwantedSysos(t *testing.T) {
	tests := []struct {
		name string
		keep bool
	}{
		{"src/runtime/race/race_linux_amd64.syso", true},
		{"src/runtime/race/race_openbsd_amd64.syso", false},
	}
	adjust := dropUnwantedSysos(&releasetargets.Target{GOOS: "linux", GOARCH: "amd64"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adjusted := adjust(&tar.Header{Name: tt.name})
			if (adjusted != nil) != tt.keep {
				t.Errorf("file %q: keep = %v, want %v", tt.name, adjusted != nil, tt.keep)
			}
		})
	}
}

func TestFixupCrossCompile(t *testing.T) {
	tests := []struct {
		name, newName string
	}{
		{"bin/go", ""},
		{"bin/linux_s390x/go", "bin/go"},
		{"pkg/linux_amd64/runtime.a", ""},
		{"pkg/linux_s390x/runtime.a", "pkg/linux_s390x/runtime.a"},
		{"pkg/tool/linux_amd64/api", ""},
		{"pkg/tool/linux_s390x/api", "pkg/tool/linux_s390x/api"},
	}
	adjust := fixupCrossCompile(&releasetargets.Target{GOOS: "linux", GOARCH: "s390x", Builder: "linux-s390x-crosscompile"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adjusted := adjust(&tar.Header{Name: tt.name})
			gotName := ""
			if adjusted != nil {
				gotName = adjusted.Name
			}
			if gotName != tt.newName {
				t.Errorf("file %q: new location = %q, want %q", tt.name, gotName, tt.newName)
			}
		})
	}
}
