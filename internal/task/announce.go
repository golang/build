// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"embed"
	"fmt"
	"strings"
	"text/template"

	"golang.org/x/build/maintner/maintnerd/maintapi/version"
)

type ReleaseAnnouncement struct {
	// Version is the Go version that has been released.
	//
	// The version string must use the same format as Go tags. For example:
	// 	• "go1.17.2" for a minor Go release
	// 	• "go1.18" for a major Go release
	// 	• "go1.18beta1" or "go1.18rc1" for a pre-release
	Version string
	// SecondaryVersion is an older Go version that was also released.
	// This only applies to minor releases. For example, "go1.16.10".
	SecondaryVersion string

	// Security is a list of descriptions, one for each distinct
	// security fix included in this release, in Markdown format.
	//
	// The empty list means there are no security fixes included.
	//
	// This field applies only to minor releases; it is an error
	// to try to use it another release type.
	Security []string

	// Names is an optional list of release coordinator names to
	// include in the sign-off message.
	Names []string
}

type mailContent struct {
	Subject  string
	BodyHTML string
	BodyText string
}

// announcementMail generates the announcement email for release r.
func announcementMail(r ReleaseAnnouncement) (mailContent, error) {
	// Pick a template name for this type of release.
	var name string
	if i := strings.Index(r.Version, "beta"); i != -1 { // A beta release.
		name = "announce-beta.md"
	} else if i := strings.Index(r.Version, "rc"); i != -1 { // Release Candidate.
		name = "announce-rc.md"
	} else if strings.Count(r.Version, ".") == 1 { // Major release like "go1.X".
		name = "announce-major.md"
	} else if strings.Count(r.Version, ".") == 2 { // Minor release like "go1.X.Y".
		name = "announce-minor.md"
	} else {
		return mailContent{}, fmt.Errorf("unknown version format: %q", r.Version)
	}

	if len(r.Security) > 0 && name != "announce-minor.md" {
		// The Security field isn't supported in templates other than minor,
		// so report an error instead of silently dropping it.
		//
		// Note: Maybe in the future we'd want to consider support for including sentences like
		// "This beta release includes the same security fixes as in Go X.Y.Z and Go A.B.C.",
		// but we'll have a better idea after these initial templates get more practical use.
		return mailContent{}, fmt.Errorf("email template %q doesn't support the Security field; this field can only be used in minor releases", name)
	}

	// TODO(go.dev/issue/47405): Render the announcement template.
	// Get the email subject.
	// Render the email body.
	return mailContent{}, fmt.Errorf("not implemented yet")
}

// announceTmpl holds templates for Go release announcement emails.
//
// Each email template starts with a MIME-style header with a Subject key,
// and the rest of it is Markdown for the email body.
var announceTmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"join": func(s []string) string {
		switch len(s) {
		case 0:
			return ""
		case 1:
			return s[0]
		case 2:
			return s[0] + " and " + s[1]
		default:
			return strings.Join(s[:len(s)-1], ", ") + ", and " + s[len(s)-1]
		}
	},
	"indent": func(s string) string { return "\t" + strings.ReplaceAll(s, "\n", "\n\t") },

	// subjectPrefix returns the email subject prefix for release r, if any.
	"subjectPrefix": func(r ReleaseAnnouncement) string {
		switch {
		case len(r.Security) > 0:
			// Include a security prefix as documented at https://go.dev/security#receiving-security-updates:
			//
			//	> The best way to receive security announcements is to subscribe to the golang-announce mailing list.
			//	> Any messages pertaining to a security issue will be prefixed with [security].
			//
			return "[security]"
		default:
			return ""
		}
	},

	// short and helpers below manipulate valid Go version strings
	// for the current needs of the announcement templates.
	"short": func(v string) string { return strings.TrimPrefix(v, "go") },
	// major extracts the major part of a valid Go version.
	// For example, major("go1.18.4") == "1.18".
	"major": func(v string) (string, error) {
		x, ok := version.Go1PointX(v)
		if !ok {
			return "", fmt.Errorf("internal error: version.Go1PointX(%q) is not ok", v)
		}
		return fmt.Sprintf("1.%d", x), nil
	},
	// build extracts the pre-release build number of a valid Go version.
	// For example, build("go1.19beta2") == "2".
	"build": func(v string) (string, error) {
		if i := strings.Index(v, "beta"); i != -1 {
			return v[i+len("beta"):], nil
		} else if i := strings.Index(v, "rc"); i != -1 {
			return v[i+len("rc"):], nil
		}
		return "", fmt.Errorf("internal error: unhandled pre-release Go version %q", v)
	},
}).ParseFS(tmplDir, "template/announce-*.md"))

//go:embed template
var tmplDir embed.FS
