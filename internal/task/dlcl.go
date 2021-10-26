// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"path"
	"regexp"
	"strings"
	"text/template"
	"time"

	"golang.org/x/build/buildenv"
	"golang.org/x/build/gerrit"
	"golang.org/x/build/internal/secret"
)

// MailDLCL mails a golang.org/dl CL that adds commands for the
// specified Go versions. It accepts one or two versions only.
//
// The versions must use the same format as Go tags. For example:
// 	• "go1.17.2" and "go1.16.9" for a minor Go release
// 	• "go1.18" for a major Go release
// 	• "go1.18beta1" or "go1.18rc1" for a pre-release
//
// Credentials are fetched from Secret Manager.
// On success, the URL of the change is returned, like "https://golang.org/cl/123".
func MailDLCL(ctx context.Context, versions []string) (changeURL string, _ error) {
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
			DocHost             string // "golang.org" or "tip.golang.org" for rc/beta
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
	}

	// Create a Gerrit CL using the Gerrit API.
	gobot, err := gobot()
	if err != nil {
		return "", err
	}
	ci, err := gobot.CreateChange(ctx, gerrit.ChangeInput{
		Project: "dl",
		Subject: "dl: add " + strings.Join(versions, " and "),
		Branch:  "master",
	})
	if err != nil {
		return "", err
	}
	changeID := fmt.Sprintf("%s~%d", ci.Project, ci.ChangeNumber)
	for path, content := range files {
		err := gobot.ChangeFileContentInChangeEdit(ctx, changeID, path, content)
		if err != nil {
			return "", err
		}
	}
	err = gobot.PublishChangeEdit(ctx, changeID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://golang.org/cl/%d", ci.ChangeNumber), nil
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
	return "golang.org"
}

func versionNoPatch(ver string) (string, error) {
	rx := regexp.MustCompile(`^(go\d+\.\d+)($|[\.]?\d*)($|rc|beta|\.)`)
	m := rx.FindStringSubmatch(ver)
	if m == nil {
		return "", fmt.Errorf("unrecognized version %q", ver)
	}
	if m[2] != "" {
		return "devel/release.html#" + m[1] + ".minor", nil
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
//     $ go install golang.org/dl/{{.Version}}@latest
//     $ {{.Version}} download
//
// And then use the {{.Version}} command as if it were your normal go
// command.
//
// See the release notes at https://{{.DocHost}}/doc/{{.VersionNoPatch}}
//
// File bugs at https://golang.org/issues/new
package main

import "golang.org/dl/internal/version"

func main() {
	version.Run("{{.Version}}")
}
`))

// gobot creates an authenticated Gerrit API client
// that uses the gobot@golang.org Gerrit account.
func gobot() (*gerrit.Client, error) {
	sc, err := secret.NewClientInProject(buildenv.Production.ProjectName)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	token, err := sc.Retrieve(context.Background(), secret.NameGobotPassword)
	if err != nil {
		return nil, err
	}
	return gerrit.NewClient("https://go-review.googlesource.com", gerrit.BasicAuth("git-gobot.golang.org", token)), nil
}
