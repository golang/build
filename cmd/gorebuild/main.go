// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Gorebuild rebuilds and verifies the distribution files posted at https://go.dev/dl/.
//
// Usage:
//
//	gorebuild [-p N] [goos-goarch][@version]...
//
// With no arguments, gorebuild rebuilds and verifies the files for all systems
// (that is, all operating system-architecture pairs) for up to three versions of Go:
//
//   - the most recent patch release of the latest Go major version,
//   - the most recent patch release of the previous Go major version, and
//   - the latest release candidate of an upcoming Go major version, if there is one.
//
// Only Go versions starting at Go 1.21 or later are considered for this default
// set of versions, because Go 1.20 and earlier did not ship reproducible toolchains.
//
// With arguments, gorebuild rebuilds the files only for the named toolchains:
//
//   - The syntax goos-goarch (for example, "linux-amd64") denotes the files
//     for that specific system's toolchains for the three default versions.
//   - The syntax @version (for example, "@go1.21rc3") denotes the files
//     for all systems, at a specific Go version.
//   - The syntax goos-goarch@version (for example, "linux-amd64@go1.21rc3")
//     denotes the files for a specific system at a specific Go version.
//
// The -p flag specifies how many toolchain rebuilds to run in parallel (default 2).
//
// When running on linux-amd64, gorebuild does a full bootstrap, building Go 1.4
// (written in C) with the host C compiler, then building Go 1.17 with Go 1.4,
// then building Go 1.20 using Go 1.17, and so on, up to the target toolchain.
// On other systems, gorebuild downloads a binary distribution
// of the bootstrap toolchain it needs. For example, Go 1.21 required Go 1.17,
// so to rebuild and verify Go 1.21, gorebuild downloads and uses the latest binary
// distribution of the Go 1.17 toolchain (specifically, Go 1.17.13) from https://go.dev/dl/.
//
// In general, gorebuild checks that the local rebuild produces a bit-for-bit
// identical copy of the file posted at https://go.dev/dl/.
// Similarly, gorebuild checks that the local rebuild produces a bit-for-bit
// identical copy of the module form of the toolchain used by Go 1.21's
// toolchain downloads (also served by https://go.dev/dl/).
//
// However, in a few cases gorebuild does not insist on a bit-for-bit comparison.
// These cases are:
//
//   - For macOS, https://go.dev/dl/ posts .tar.gz files containing binaries
//     signed by Google's code-signing key.
//     Gorebuild has no way to sign the binaries it produces using that same key.
//     Instead, gorebuild compares the content of the rebuilt archive with the
//     content of the posted archive, checking that non-executables match exactly
//     and that executables match exactly after stripping their code signatures.
//     The same comparison is applied to the module form of the toolchain.
//
//   - For macOS, https://go.dev/dl/ posts a .pkg installer file.
//     Gorebuild does not run the macOS tools to rebuild that installer.
//     Instead, it parses the .pkg file and checks that the contents match
//     the rebuilt .tar.gz file exactly, again after stripping code signatures.
//     The .pkg is permitted to have one extra file, /etc/paths.d/go, which
//     is unique to the .pkg form.
//
//   - For Windows, https://go.dev/dl/ posts a .msi installer file.
//     Gorebuild does not run the Windows tools to rebuild that installer.
//     Instead, it invokes the Unix program “msiextract” to unpack the file
//     and then checks that the contents match the rebuilt .zip file exactly.
//     If “msiextract” is not found in the PATH, the .msi file is skipped
//     rather than considered a failure.
//
// Gorebuild prints log messages to standard error but also accumulates them
// in a structured report. Before exiting, it writes the report as JSON to gorebuild.json
// and as HTML to gorebuild.html.
//
// Gorebuild exits with status 0 when it succeeds in writing a report,
// whether or not the report verified all the posted files.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"os"
	"strings"
)

var pFlag = flag.Int("p", 2, "run `n` builds in parallel")

func usage() {
	fmt.Fprintf(os.Stderr, "usage: gorebuild [flags] [goos-goarch][@version]...\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("gorebuild: ")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()

	// Undocumented feature for developers working on report template:
	// pass in a gorebuild.json file and it reformats the gorebuild.html file.
	if len(args) == 1 && strings.HasSuffix(args[0], ".json") {
		reformat(args[0])
		return
	}

	r := Run(args)
	writeJSON(r)
	writeHTML(r)
}

func reformat(file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		log.Fatal(err)
	}
	writeHTML(&r)
}

func writeJSON(r *Report) {
	js, err := json.MarshalIndent(r, "", "\t")
	if err != nil {
		log.Fatal(err)
	}
	js = append(js, '\n')
	if err := os.WriteFile("gorebuild.json", js, 0666); err != nil {
		log.Fatal(err)
	}
}

//go:embed report.tmpl
var reportTmpl string

func writeHTML(r *Report) {
	t, err := template.New("report.tmpl").Parse(reportTmpl)
	if err != nil {
		log.Fatal(err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, &r); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("gorebuild.html", buf.Bytes(), 0666); err != nil {
		log.Fatal(err)
	}
}
