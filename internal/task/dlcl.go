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
// The version must use the same format as Go tags. For example:
//   - "go1.17.2" and "go1.16.9" for a minor Go release
//   - "go1.21.0" for a major Go release
//   - "go1.18beta1" or "go1.18rc1" for a pre-release
//
// On success, the ID of the change is returned, like "dl~1234".
func (t *VersionTasks) MailDLCL(ctx *workflow.TaskContext, major int, kind ReleaseKind, version string, reviewers []string, dryRun bool) (changeID string, _ error) {
	var files = make(map[string]string) // Map key is relative path, and map value is file content.

	// Generate main.go files for versions from the template.
	var buf bytes.Buffer
	if err := dlTmpl.Execute(&buf, struct {
		Year                int
		Version             string // "go1.5.3rc2"
		DocLink             string // "https://go.dev/doc/go1.5"
		CapitalSpaceVersion string // "Go 1.5.0"
	}{
		Year:                time.Now().UTC().Year(),
		Version:             version,
		DocLink:             docLink(major, kind, version),
		CapitalSpaceVersion: strings.Replace(version, "go", "Go ", 1),
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

// oneOrTwoGoVersions returns true iff len(versions) is exactly 1 or 2
// and each version passes some lightweight checks that catch problems.
func oneOrTwoGoVersions(versions []string) error {
	if len(versions) < 1 || len(versions) > 2 {
		return fmt.Errorf("got %d Go versions, want 1 or 2", len(versions))
	}
	for _, ver := range versions {
		if ver != strings.ToLower(ver) {
			return fmt.Errorf("version %q is not lowercase", ver)
		} else if strings.Contains(ver, " ") {
			return fmt.Errorf("version %q contains a space", ver)
		} else if !strings.HasPrefix(ver, "go") {
			return fmt.Errorf("version %q doesn't have the 'go' prefix", ver)
		}
	}
	return nil
}

func docLink(major int, kind ReleaseKind, ver string) string {
	if kind == KindCurrentMinor || kind == KindPrevMinor {
		return fmt.Sprintf("https://go.dev/doc/devel/release#%v", ver)
	}

	host := "go.dev"
	if kind == KindBeta || kind == KindRC {
		host = "tip.golang.org"
	}
	return fmt.Sprintf("https://%v/doc/go1.%d", host, major)
}

var dlTmpl = template.Must(template.New("").Parse(`// Copyright {{.Year}} The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The {{.Version}} command runs the go command from {{.CapitalSpaceVersion}}.
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
