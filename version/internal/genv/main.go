// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The genv command generates version-specific go command source files.
package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: genv <version>...")
	os.Exit(2)
}

func main() {
	if len(os.Args) == 1 {
		usage()
	}
	for _, version := range os.Args[1:] {
		var buf bytes.Buffer
		if err := mainTmpl.Execute(&buf, struct {
			Year    int
			Version string
		}{time.Now().Year(), version}); err != nil {
			failf("mainTmpl.execute: %v", err)
		}
		path := filepath.Join(os.Getenv("GOPATH"), "src/golang.org/x/build/version", version, "main.go")
		if err := os.Mkdir(filepath.Dir(path), 0755); err != nil {
			failf("os.Mkdir: %v", err)
		}
		if err := ioutil.WriteFile(path, buf.Bytes(), 0666); err != nil {
			failf("ioutil.WriteFile: %v", err)
		}
		fmt.Println("Wrote", path)
		if err := exec.Command("gofmt", "-w", path).Run(); err != nil {
			failf("could not gofmt file %q: %v", path, err)
		}
	}
}

func failf(format string, args ...interface{}) {
	if len(format) == 0 || format[len(format)-1] != '\n' {
		format += "\n"
	}
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}

var mainTmpl = template.Must(template.New("main").Parse(`// Copyright {{.Year}} The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The {{.Version}} command runs the go command from {{.Version}}.
//
// To install, run:
//
//     $ go get golang.org/x/build/version/{{.Version}}
//     $ {{.Version}} download
//
// And then use the {{.Version}} command as if it were your normal go
// command.
//
// See the release notes at https://golang.org/doc/{{.Version}}
//
// File bugs at https://golang.org/issues/new
package main

import "golang.org/x/build/version"

func main() {
	version.Run("{{.Version}}")
}
`))
