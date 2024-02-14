// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"path"
	"strings"
)

// ExtractFile copies the first file in tgz matching glob to dest.
func ExtractFile(tgz io.Reader, dest io.Writer, glob string) error {
	zr, err := gzip.NewReader(tgz)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		} else if err != nil {
			return err
		}
		if match, _ := path.Match(glob, h.Name); !h.FileInfo().IsDir() && match {
			break
		}
	}
	_, err = io.Copy(dest, tr)
	return err
}

func ConvertZIPToTGZ(r io.ReaderAt, size int64, w io.Writer) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}

	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)

	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     f.Name,
			Typeflag: tar.TypeReg,
			Mode:     0o777,
			Size:     int64(f.UncompressedSize64),

			AccessTime: f.Modified,
			ChangeTime: f.Modified,
			ModTime:    f.Modified,
		}); err != nil {
			return err
		}
		content, err := f.Open()
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, content); err != nil {
			return err
		}
		if err := content.Close(); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}
