// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package darwinpkg encodes the process of building a macOS PKG
// installer from the given Go toolchain .tar.gz binary archive.
package darwinpkg

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"golang.org/x/build/internal/untar"

	goversion "go/version"
)

// InstallerOptions holds options for constructing the installer.
type InstallerOptions struct {
	GOARCH string // The target GOARCH.
	// MinMacOSVersion is the minimum required system.version.ProductVersion.
	// For example, "11" for macOS 11 Big Sur, "10.15" for macOS 10.15 Catalina, etc.
	MinMacOSVersion string
}

// ConstructInstaller constructs an installer for the provided Go toolchain .tar.gz
// binary archive using workDir as a working directory, and returns the output path.
//
// It's intended to run on a macOS system, with Xcode tools available in $PATH.
func ConstructInstaller(_ context.Context, workDir, tgzPath string, opt InstallerOptions) (pkgPath string, _ error) {
	var errs []error
	for _, dep := range [...]string{"pkgbuild", "productbuild"} {
		if _, err := exec.LookPath(dep); err != nil {
			errs = append(errs, fmt.Errorf("dependency %q is not in PATH", dep))
		}
	}
	if opt.GOARCH == "" {
		errs = append(errs, fmt.Errorf("GOARCH is empty"))
	}
	if opt.MinMacOSVersion == "" {
		errs = append(errs, fmt.Errorf("MinMacOSVersion is empty"))
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	oldDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(workDir); err != nil {
		panic(err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			panic(err)
		}
	}()

	fmt.Println("Building inner .pkg with pkgbuild.")
	run("mkdir", "pkg-intermediate")
	putTar(tgzPath, "pkg-root/usr/local")
	put("/usr/local/go/bin\n", "pkg-root/etc/paths.d/go", 0644)
	put(`#!/bin/bash

GOROOT=/usr/local/go
echo "Removing previous installation"
if [ -d $GOROOT ]; then
	rm -r $GOROOT
fi
`, "pkg-scripts/preinstall", 0755)
	version := readVERSION("pkg-root/usr/local/go")
	if goversion.Compare(version, "go1.25rc2") >= 0 {
		put(`#!/bin/bash

GOROOT=/usr/local/go
echo "Fixing permissions"
cd $GOROOT
find . -exec chmod ugo+r \{\} +
find bin -exec chmod ugo+rx \{\} +
find . -type d -exec chmod ugo+rx \{\} +
chmod o-w .
`, "pkg-scripts/postinstall", 0755)
	} else {
		put(`#!/bin/bash

GOROOT=/usr/local/go
echo "Fixing permissions"
cd $GOROOT
find . -exec chmod ugo+r \{\} \;
find bin -exec chmod ugo+rx \{\} \;
find . -type d -exec chmod ugo+rx \{\} \;
chmod o-w .
`, "pkg-scripts/postinstall", 0755)
	}
	run("pkgbuild",
		"--identifier=org.golang.go",
		"--version", version,
		"--scripts=pkg-scripts",
		"--root=pkg-root",
		"pkg-intermediate/org.golang.go.pkg",
	)

	fmt.Println("\nBuilding outer .pkg with productbuild.")
	run("mkdir", "pkg-out")
	bg, err := darwinPKGBackground(opt.GOARCH)
	if err != nil {
		log.Fatalln("darwinPKGBackground:", err)
	}
	put(string(bg), "pkg-resources/background.png", 0644)
	var buf bytes.Buffer
	distData := darwinDistData{
		HostArchs: map[string]string{"amd64": "x86_64", "arm64": "arm64"}[opt.GOARCH],
		MinOS:     opt.MinMacOSVersion,
	}
	if err := darwinDistTmpl.ExecuteTemplate(&buf, "dist.xml", distData); err != nil {
		log.Fatalln("darwinDistTmpl.ExecuteTemplate:", err)
	}
	put(buf.String(), "pkg-distribution", 0644)
	run("productbuild",
		"--distribution=pkg-distribution",
		"--resources=pkg-resources",
		"--package-path=pkg-intermediate",
		"pkg-out/"+version+"-unsigned.pkg",
	)

	return filepath.Join(workDir, "pkg-out", version+"-unsigned.pkg"), nil
}

//go:embed _data
var darwinPKGData embed.FS

func darwinPKGBackground(goarch string) ([]byte, error) {
	switch goarch {
	case "arm64":
		return darwinPKGData.ReadFile("_data/blue-bg.png")
	case "amd64":
		return darwinPKGData.ReadFile("_data/brown-bg.png")
	default:
		return nil, fmt.Errorf("no background for GOARCH %q", goarch)
	}
}

var darwinDistTmpl = template.Must(template.New("").ParseFS(darwinPKGData, "_data/dist.xml"))

type darwinDistData struct {
	HostArchs string // hostArchitectures option value.
	MinOS     string // Minimum required system.version.ProductVersion.
}

func put(content, dst string, perm fs.FileMode) {
	err := os.MkdirAll(filepath.Dir(dst), 0755)
	if err != nil {
		panic(err)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		panic(err)
	}
	_, err = io.WriteString(f, content)
	if err != nil {
		panic(err)
	}
	err = f.Close()
	if err != nil {
		panic(err)
	}
}

func putTar(tgz, dir string) {
	f, err := os.Open(tgz)
	if err != nil {
		panic(err)
	}
	err = untar.Untar(f, dir)
	if err != nil {
		panic(err)
	}
	err = f.Close()
	if err != nil {
		panic(err)
	}
}

// run runs the command and requires that it succeeds.
// If not, it logs the failure and exits with a non-zero code.
// It prints the command line.
func run(name string, args ...string) {
	fmt.Printf("$ %s %s\n", name, strings.Join(args, " "))
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		log.Fatalf("command failed: %v\n%s", err, out)
	}
}

// readVERSION reads the VERSION file and
// returns the first line of the file, the Go version.
func readVERSION(goroot string) (version string) {
	b, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		panic(err)
	}
	version, _, _ = strings.Cut(string(b), "\n")
	return version
}
