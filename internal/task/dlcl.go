// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"fmt"
	"go/format"
	"path"
	"strings"
	"text/template"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

// MailDLCL mails a golang.org/dl CL that adds commands for the
// specified Go version.
//
// The version string must use the same format as Go tags. For example:
//   - "go1.21rc2" for a pre-release
//   - "go1.21.0" for a major Go release
//   - "go1.21.1" for a minor Go release
//
// On success, the ID of the change is returned, like "dl~1234".
func (t *VersionTasks) MailDLCL(ctx *workflow.TaskContext, major int, kind ReleaseKind, version string, reviewers []string, dryRun bool) (changeID string, _ error) {
	var files = make(map[string]string) // Map key is relative path, and map value is file content.

	// Generate main.go files for versions from the template.
	var buf bytes.Buffer
	if err := dlTmpl.Execute(&buf, struct {
		Year    int
		Version string // "go1.21rc2"
		DocLink string // "https://go.dev/doc/go1.21"
	}{
		Year:    time.Now().UTC().Year(),
		Version: version,
		DocLink: docLink(major, kind, version),
	}); err != nil {
		return "", fmt.Errorf("dlTmpl.Execute: %v", err)
	}
	gofmted, err := format.Source(buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("could not gofmt: %v", err)
	}
	files[path.Join(version, "main.go")] = string(gofmted)
	ctx.Printf("file %q (command %q):\n%s", path.Join(version, "main.go"), "golang.org/dl/"+version, gofmted)

	// Create a Gerrit CL using the Gerrit API.
	if dryRun {
		return "(dry-run)", nil
	}
	changeInput := gerrit.ChangeInput{
		Project: "dl",
		Subject: "dl: add " + version,
		Branch:  "master",
	}
	return t.Gerrit.CreateAutoSubmitChange(ctx, changeInput, reviewers, files)
}

func docLink(major int, kind ReleaseKind, ver string) string {
	if kind == KindMinor {
		return fmt.Sprintf("https://go.dev/doc/devel/release#%v", ver)
	}

	host := "go.dev"
	if kind == KindBeta || kind == KindRC {
		host = "tip.golang.org"
	}
	return fmt.Sprintf("https://%v/doc/go1.%d", host, major)
}

var dlTmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"short": func(v string) string { return strings.TrimPrefix(v, "go") },
}).Parse(`// Copyright {{.Year}} The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The {{.Version}} command runs the go command from Go {{.Version|short}}.
//
// To install, run:
//
//	$ go install golang.org/dl/{{.Version}}@latest
//	$ {{.Version}} download
//
// And then use the {{.Version}} command as if it were your normal go
// command.
//
// See the release notes at {{.DocLink}}.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("{{.Version}}")
}
`))
