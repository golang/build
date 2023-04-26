// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/build/internal/releasetargets"
)

// Converted distributions are treated as versions of golang.org/toolchain.
// The archive for go1.2.3.linux-amd64.tar.gz is stored as version v0.0.1-go1.2.3.linux-amd64.
const (
	modulePath    = "golang.org/toolchain"
	moduleVersion = "v0.0.1"
)

func ToolchainZipPrefix(target *releasetargets.Target, version string) string {
	return modulePath + "@" + ToolchainModuleVersion(target, version)
}

func ToolchainModuleVersion(target *releasetargets.Target, version string) string {
	return fmt.Sprintf("%v-%v.%v-%v", moduleVersion, version, target.GOOS, target.GOARCH)
}

// TarToModFiles converts the distribution archive with the given name and content
// to a collection of module files.
func TarToModFiles(target *releasetargets.Target, version string, t time.Time, tgz io.Reader, w io.Writer) (mod string, info string, _ error) {
	vers := ToolchainModuleVersion(target, version)
	zipPrefix := ToolchainZipPrefix(target, version)

	// rename takes the name of a file found in a distribution archive
	// and returns the name to use for that file in the module archive.
	// The main conversion is go/zzz -> golang.org/toolchain@v0.0.1-go<vers>.<goos>-<goarch>/zzz.
	// If rename returns "", nil, then the file should be omitted from the
	// module archive entirely.
	rename := func(name string) (string, error) {
		if !strings.HasPrefix(name, "go/") {
			return "", fmt.Errorf("unexpected file name %q", name)
		}
		// Modules cannot contain go.mod files, so rename them to _go.mod.
		if strings.HasSuffix(name, "/go.mod") {
			name = strings.TrimSuffix(name, "/go.mod") + "/_go.mod"
		}
		// Omit these directories.
		switch {
		case strings.HasPrefix(name, "go/.github/"),
			strings.HasPrefix(name, "go/api/"),
			strings.HasPrefix(name, "go/doc/"),
			strings.HasPrefix(name, "go/misc/"),
			strings.HasPrefix(name, "go/test/"):
			return "", nil
		}
		return zipPrefix + name[len("go"):], nil
	}

	// Convert archive, extracting its modification time for our metadata.
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	if err := convertTarGz(zw, t, tgz, rename); err == nil {
		err = zw.Close()
	}

	info = fmt.Sprintf("{%q:%q, %q:%q}\n", "Version", vers, "Time", t.UTC().Format(time.RFC3339))
	mod = fmt.Sprintf("module %s\n", modulePath)
	return mod, info, nil
}

// convertTarGz writes a distribution .tar.gz archive's content to zw, applying the rename function.
func convertTarGz(zw *zip.Writer, t time.Time, tgz io.Reader, rename func(string) (string, error)) error {
	gzr, err := gzip.NewReader(tgz)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if hdr.Typeflag != tar.TypeReg { // omit directories
			continue
		}
		name, err := rename(hdr.Name)
		if err != nil {
			return err
		}
		if name == "" { // omit files rejected by rename
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: t,
		})
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			return err
		}
	}
	return nil
}
