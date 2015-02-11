// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore

// Command releaselet does buildlet-side release construction tasks.
// It is intended to be executed on the buildlet preparing a release.
package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

func main() {
	if err := blog(); err != nil {
		log.Fatal(err)
	}
	if err := tour(); err != nil {
		log.Fatal(err)
	}
	// TODO(adg): build msi files on windows
	// TODO(adg): build pkg files on osx
}

const blogPath = "golang.org/x/blog"

var blogContent = []string{
	"content",
	"template",
}

func blog() error {
	// Copy blog content to $GOROOT/blog.
	blogSrc := filepath.Join("gopath/src", blogPath)
	contentDir := filepath.FromSlash("go/blog")
	return cpAllDir(contentDir, blogSrc, blogContent...)
}

const tourPath = "golang.org/x/tour"

var tourContent = []string{
	"content",
	"solutions",
	"static",
	"template",
}

var tourPackages = []string{
	"pic",
	"tree",
	"wc",
}

func tour() error {
	tourSrc := filepath.Join("gopath/src", tourPath)
	contentDir := filepath.FromSlash("go/misc/tour")

	// Copy all the tour content to $GOROOT/misc/tour.
	if err := cpAllDir(contentDir, tourSrc, tourContent...); err != nil {
		return err
	}

	// Copy the tour source code so it's accessible with $GOPATH pointing to $GOROOT/misc/tour.
	tourPkgDir := filepath.Join(contentDir, "src", tourPath)
	if err := cpAllDir(tourPkgDir, tourSrc, tourPackages...); err != nil {
		return err
	}

	// Copy gotour binary to tool directory as "tour"; invoked as "go tool tour".
	return cp(
		filepath.FromSlash("go/pkg/tool/"+runtime.GOOS+"_"+runtime.GOARCH+"/tour"+ext()),
		filepath.FromSlash("gopath/bin/gotour"+ext()),
	)
}

func cp(dst, src string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	fi, err := sf.Stat()
	if err != nil {
		return err
	}
	df, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer df.Close()
	// Windows doesn't currently implement Fchmod
	// TODO(adg): is this still true?
	if runtime.GOOS != "windows" {
		if err := df.Chmod(fi.Mode()); err != nil {
			return err
		}
	}
	_, err = io.Copy(df, sf)
	return err
}

func cpDir(dst, src string) error {
	walk := func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, srcPath[len(src):])
		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}
		return cp(dstPath, srcPath)
	}
	return filepath.Walk(src, walk)
}

func cpAllDir(dst, basePath string, dirs ...string) error {
	for _, dir := range dirs {
		if err := cpDir(filepath.Join(dst, dir), filepath.Join(basePath, dir)); err != nil {
			return err
		}
	}
	return nil
}

func ext() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
