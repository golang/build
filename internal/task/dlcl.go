// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"fmt"
	"go/format"
	"path"
	"regexp"
	"strings"
	"text/template"
	"time"

	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/workflow"
)

// MailDLCL mails a golang.org/dl CL that adds commands for the
// specified Go versions. It accepts one or two versions only.
//
// The versions must use the same format as Go tags. For example:
//   - "go1.17.2" and "go1.16.9" for a minor Go release
//   - "go1.18" for a major Go release
//   - "go1.18beta1" or "go1.18rc1" for a pre-release
//
// On success, the ID of the change is returned, like "dl~1234".
func (t *VersionTasks) MailDLCL(ctx *workflow.TaskContext, versions []string, dryRun bool) (changeID string, _ error) {
	if len(versions) < 1 || len(versions) > 2 {
		return "", fmt.Errorf("got %d Go versions, want 1 or 2", len(versions))
	}
	if err := verifyGoVersions(versions...); err != nil {
		return "", err
	}

	var files = make(map[string]string) // Map key is relative path, and map value is file content.

	// Generate main.go files for versions from the template.
	for _, ver := range versions {
		var buf bytes.Buffer
		versionNoPatch, err := versionNoPatch(ver)
		if err != nil {
			return "", err
		}
		if err := dlTmpl.Execute(&buf, struct {
			Year                int
			Version             string // "go1.5.3rc2"
			VersionNoPatch      string // "go1.5"
			CapitalSpaceVersion string // "Go 1.5"
			DocHost             string // "go.dev" or "tip.golang.org" for rc/beta
		}{
			Year:                time.Now().UTC().Year(),
			Version:             ver,
			VersionNoPatch:      versionNoPatch,
			DocHost:             docHost(ver),
			CapitalSpaceVersion: strings.Replace(ver, "go", "Go ", 1),
		}); err != nil {
			return "", fmt.Errorf("dlTmpl.Execute: %v", err)
		}
		gofmted, err := format.Source(buf.Bytes())
		if err != nil {
			return "", fmt.Errorf("could not gofmt: %v", err)
		}
		files[path.Join(ver, "main.go")] = string(gofmted)
		if log := ctx.Logger; log != nil {
			log.Printf("file %q (command %q):\n%s", path.Join(ver, "main.go"), "golang.org/dl/"+ver, gofmted)
		}
	}

	// Create a Gerrit CL using the Gerrit API.
	if dryRun {
		return "(dry-run)", nil
	}
	changeInput := gerrit.ChangeInput{
		Project: "dl",
		Subject: "dl: add " + strings.Join(versions, " and "),
		Branch:  "master",
	}
	return t.Gerrit.CreateAutoSubmitChange(ctx, changeInput, files)
}

func verifyGoVersions(versions ...string) error {
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

func docHost(ver string) string {
	if strings.Contains(ver, "rc") || strings.Contains(ver, "beta") {
		return "tip.golang.org"
	}
	return "go.dev"
}

func versionNoPatch(ver string) (string, error) {
	rx := regexp.MustCompile(`^(go\d+\.\d+)($|[\.]?\d*)($|rc|beta|\.)`)
	m := rx.FindStringSubmatch(ver)
	if m == nil {
		return "", fmt.Errorf("unrecognized version %q", ver)
	}
	if m[2] != "" {
		return "devel/release#" + m[1] + ".minor", nil
	}
	return m[1], nil
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
// See the release notes at https://{{.DocHost}}/doc/{{.VersionNoPatch}}.
//
// File bugs at https://go.dev/issue/new.
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("{{.Version}}")
}
`))
