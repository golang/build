// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestStripDarwinSig(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}

	var log Log
	stripped := StripDarwinSig(&log, "/bin/"+filepath.Base(exe), data)
	for _, m := range log.Messages {
		t.Log(m.Text)
	}
	if runtime.GOARCH != "amd64" && bytes.Equal(stripped, data) {
		t.Errorf("failed to strip signature")
	}
}

func TestIndexPkg(t *testing.T) {
	dir := t.TempDir()
	check := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}

	subdirs := []string{
		"root/etc/paths.d",
		"root/usr/local/go/subdir",
		"scripts",
		"resources",
		"out1",
		"out2",
	}
	for _, d := range subdirs {
		check(os.MkdirAll(dir+"/"+d, 0777))
	}
	check(os.WriteFile(dir+"/distribution", distributionXML, 0666))
	check(os.WriteFile(dir+"/root/etc/paths.d/go", []byte("ignore me!"), 0666))
	check(os.WriteFile(dir+"/root/usr/local/go/hello.txt", []byte("hello world"), 0666))
	check(os.WriteFile(dir+"/root/usr/local/go/subdir/fortune.txt", []byte("you will be packaged"), 0666))

	cmd := exec.Command("pkgbuild",
		"--identifier=org.golang.go",
		"--version=1.2.3",
		"--scripts=scripts",
		"--root=root",
		"out1/org.golang.go.pkg")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pkgbuild: %v\n%s", err, out)
	}

	cmd = exec.Command("productbuild",
		"--distribution=distribution",
		"--resources=resources",
		"--package-path=out1",
		"out2/go.pkg")
	cmd.Dir = dir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("productbuild: %v\n%s", err, out)
	}

	data, err := os.ReadFile(dir + "/out2/go.pkg")
	check(err)

	var log Log
	ix := indexPkg(&log, data, nil)
	for _, m := range log.Messages {
		t.Log(m.Text)
	}
	if ix == nil {
		t.Fatalf("indexPkg failed")
	}

	var files []*CpioFile
	for _, f := range ix {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})
	if len(files) != 2 || files[0].Name != "go/hello.txt" || files[1].Name != "go/subdir/fortune.txt" {
		t.Errorf("unexpected pkg contents:")
		for _, f := range files {
			t.Logf("%+v", *f)
		}
	}
}

var distributionXML = []byte(`<?xml version="1.0" encoding="utf-8" standalone="no"?>
<installer-gui-script minSpecVersion="1">
  <title>Go</title>
  <choices-outline>
    <line choice="org.golang.go.choice" />
  </choices-outline>
  <choice id="org.golang.go.choice" title="Go">
    <pkg-ref id="org.golang.go.pkg" />
  </choice>
  <pkg-ref id="org.golang.go.pkg" auth="Root">org.golang.go.pkg</pkg-ref>
</installer-gui-script>
`)
