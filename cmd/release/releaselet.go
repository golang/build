// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore

// Command releaselet does buildlet-side release construction tasks.
// It is intended to be executed on the buildlet preparing a release.
package main

import (
	"bytes"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
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
	var err error
	switch runtime.GOOS {
	case "windows":
		// TODO(adg): build msi files on windows
	case "darwin":
		// TOOD(adg): build pkg files on darwin
		err = darwinPkg()
	}
	if err != nil {
		log.Fatal(err)
	}
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

func darwinPkg() error {
	// Learn a little about the environment.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	versionBytes, err := ioutil.ReadFile("go/VERSION")
	if err != nil {
		return err
	}
	version := string(bytes.TrimSpace(versionBytes))

	// Write out darwin data that is used by packaging process.
	defer os.RemoveAll("darwin")
	for name, body := range darwinData {
		dst := filepath.Join("darwin", name)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		// (We really mean 0755 on the next line; some of these files
		// are executable, and there's no harm in making them all so.)
		if err := ioutil.WriteFile(dst, []byte(body), 0755); err != nil {
			return err
		}
	}

	// Create a work directory and place inside the files as they should
	// be on the destination file system.
	work := filepath.Join(cwd, "darwinpkg")
	if err := os.MkdirAll(work, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// Write out /etc/paths.d/go.
	const pathsBody = "/usr/local/go/bin"
	pathsDir := filepath.Join(work, "etc/paths.d")
	pathsFile := filepath.Join(pathsDir, "go")
	if err := os.MkdirAll(pathsDir, 0755); err != nil {
		return err
	}
	if err = ioutil.WriteFile(pathsFile, []byte(pathsBody), 0644); err != nil {
		return err
	}

	// Copy Go installation to /usr/local/go.
	goDir := filepath.Join(work, "usr/local/go")
	if err := os.MkdirAll(goDir, 0755); err != nil {
		return err
	}
	if err := cpDir(goDir, "go"); err != nil {
		return err
	}

	// Build the package file.
	dest := "package"
	if err := os.Mkdir(dest, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(dest)

	if err := run("pkgbuild",
		"--identifier", "com.googlecode.go",
		"--version", version,
		"--scripts", "darwin/scripts",
		"--root", work,
		filepath.Join(dest, "com.googlecode.go.pkg"),
	); err != nil {
		return err
	}

	const pkg = "pkg" // known to cmd/release
	if err := os.Mkdir(pkg, 0755); err != nil {
		return err
	}
	return run("productbuild",
		"--distribution", "darwin/Distribution",
		"--resources", "darwin/Resources",
		"--package-path", dest,
		filepath.Join(cwd, pkg, "go.pkg"), // file name irrelevant
	)
}

func run(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
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
	// Windows doesn't implement Fchmod.
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

var darwinData = map[string]string{

	"scripts/postinstall": `#!/bin/bash
GOROOT=/usr/local/go
echo "Fixing permissions"
cd $GOROOT
find . -exec chmod ugo+r \{\} \;
find bin -exec chmod ugo+rx \{\} \;
find . -type d -exec chmod ugo+rx \{\} \;
chmod o-w .
`,

	"scripts/preinstall": `#!/bin/bash
GOROOT=/usr/local/go
echo "Removing previous installation"
if [ -d $GOROOT ]; then
	rm -r $GOROOT
fi
`,

	"Distribution": `<?xml version="1.0" encoding="utf-8" standalone="no"?>
<installer-script minSpecVersion="1.000000">
    <title>Go</title>
    <background mime-type="image/png" file="bg.png"/>
    <options customize="never" allow-external-scripts="no"/>
    <domains enable_localSystem="true" />
    <installation-check script="installCheck();"/>
    <script>
function installCheck() {
    if(!(system.compareVersions(system.version.ProductVersion, '10.6.0') >= 0)) {
        my.result.title = 'Unable to install';
        my.result.message = 'Go requires Mac OS X 10.6 or later.';
        my.result.type = 'Fatal';
        return false;
    }
    if(system.files.fileExistsAtPath('/usr/local/go/bin/go')) {
	    my.result.title = 'Previous Installation Detected';
	    my.result.message = 'A previous installation of Go exists at /usr/local/go. This installer will remove the previous installation prior to installing. Please back up any data before proceeding.';
	    my.result.type = 'Warning';
	    return false;
	}
    return true;    
}
    </script>
    <choices-outline>
        <line choice="com.googlecode.go.choice"/>
    </choices-outline>
    <choice id="com.googlecode.go.choice" title="Go">
        <pkg-ref id="com.googlecode.go.pkg"/>
    </choice>
    <pkg-ref id="com.googlecode.go.pkg" auth="Root">com.googlecode.go.pkg</pkg-ref>
</installer-script>
`,

	"Resources/bg.png": "TODO(adg): populate this",
}
