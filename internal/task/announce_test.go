// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/build/internal/workflow"
)

// Test that the task doesn't start running if the provided
// context doesn't have sufficient time for the task to run.
func TestAnnounceReleaseShortContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := (AnnounceMailTasks{}).AnnounceRelease(&workflow.TaskContext{Context: ctx}, []string{"go1.18.1", "go1.17.8"}, nil, nil)
	if err == nil {
		t.Errorf("want non-nil error")
	} else if !strings.HasPrefix(err.Error(), "insufficient time") {
		t.Errorf("want error that starts with 'insufficient time' instead of: %s", err)
	}
}

func TestAnnouncementMail(t *testing.T) {
	tests := [...]struct {
		name        string
		in          releaseAnnouncement
		wantSubject string
	}{
		{
			name: "minor",
			in: releaseAnnouncement{
				Version:          "go1.18.1",
				SecondaryVersion: "go1.17.9",
				Names:            []string{"Alice", "Bob", "Charlie"},
			},
			wantSubject: "Go 1.18.1 and Go 1.17.9 are released",
		},
		{
			name: "minor-with-security",
			in: releaseAnnouncement{
				Version:          "go1.18.1",
				SecondaryVersion: "go1.17.9",
				Security: []string{
					`encoding/pem: fix stack overflow in Decode

A large (more than 5 MB) PEM input can cause a stack overflow in Decode, leading the program to crash.

Thanks to Juho Nurminen of Mattermost who reported the error.

This is CVE-2022-24675 and https://go.dev/issue/51853.`,
					`crypto/elliptic: tolerate all oversized scalars in generic P-256

A crafted scalar input longer than 32 bytes can cause P256().ScalarMult or P256().ScalarBaseMult to panic. Indirect uses through crypto/ecdsa and crypto/tls are unaffected. amd64, arm64, ppc64le, and s390x are unaffected.

This was discovered thanks to a Project Wycheproof test vector.

This is CVE-2022-28327 and https://go.dev/issue/52075.`,
					`crypto/x509: non-compliant certificates can cause a panic in Verify on macOS in Go 1.18

Verifying certificate chains containing certificates which are not compliant with RFC 5280 causes Certificate.Verify to panic on macOS.

These chains can be delivered through TLS and can cause a crypto/tls or net/http client to crash.

Thanks to Tailscale for doing weird things and finding this.

This is CVE-2022-27536 and https://go.dev/issue/51759.`,
				},
			},
			wantSubject: "[security] Go 1.18.1 and Go 1.17.9 are released",
		},
		{
			name: "minor-solo",
			in: releaseAnnouncement{
				Version:  "go1.11.1",
				Security: []string{"abc: security fix 1", "xyz: security fix 2"},
				Names:    []string{"Alice"},
			},
			wantSubject: "[security] Go 1.11.1 is released",
		},
		{
			name: "beta",
			in: releaseAnnouncement{
				Version: "go1.19beta5",
			},
			wantSubject: "Go 1.19 Beta 5 is released",
		},
		{
			name: "rc",
			in: releaseAnnouncement{
				Version: "go1.19rc6",
			},
			wantSubject: "Go 1.19 Release Candidate 6 is released",
		},
		{
			name: "major",
			in: releaseAnnouncement{
				Version: "go1.19",
			},
			wantSubject: "Go 1.19 is released",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := announcementMail(tc.in)
			if err != nil {
				t.Fatal("announcementMail returned non-nil error:", err)
			}
			if *updateFlag {
				writeTestdataFile(t, "announce-"+tc.name+".html", []byte(m.BodyHTML))
				writeTestdataFile(t, "announce-"+tc.name+".txt", []byte(m.BodyText))
				return
			}
			if diff := cmp.Diff(tc.wantSubject, m.Subject); diff != "" {
				t.Errorf("subject mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(testdataFile(t, "announce-"+tc.name+".html"), m.BodyHTML); diff != "" {
				t.Errorf("body HTML mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(testdataFile(t, "announce-"+tc.name+".txt"), m.BodyText); diff != "" {
				t.Errorf("body text mismatch (-want +got):\n%s", diff)
			}
			if t.Failed() {
				t.Log("\n\n(if the new output is intentional, use -update flag to update goldens)")
			}
		})
	}
}

// testdataFile reads the named file in the testdata directory.
func testdataFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeTestdataFile writes the named file in the testdata directory.
func writeTestdataFile(t *testing.T, name string, data []byte) {
	t.Helper()
	err := os.WriteFile(filepath.Join("testdata", name), data, 0644)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnnounceRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("not running test that uses internet in short mode")
	}

	tests := [...]struct {
		name     string
		versions []string
		security []string
		names    []string
		want     SentMail
		wantLog  string
	}{
		{
			name:     "minor",
			versions: []string{"go1.18.1", "go1.17.8"}, // Intentionally not 1.17.9 so the real email doesn't get in the way.
			names:    []string{"Alice", "Bob", "Charlie"},
			want:     SentMail{Subject: "Go 1.18.1 and Go 1.17.8 are released"},
			wantLog: `announcement subject: Go 1.18.1 and Go 1.17.8 are released

announcement body HTML:
<p>Hello gophers,</p>
<p>We have just released Go versions 1.18.1 and 1.17.8, minor point releases.</p>
<p>View the release notes for more information:<br>
<a href="https://go.dev/doc/devel/release#go1.18.1">https://go.dev/doc/devel/release#go1.18.1</a></p>
<p>You can download binary and source distributions from the Go website:<br>
<a href="https://go.dev/dl/">https://go.dev/dl/</a></p>
<p>To compile from source using a Git clone, update to the release with<br>
<code>git checkout go1.18.1</code> and build as usual.</p>
<p>Thanks to everyone who contributed to the releases.</p>
<p>Cheers,<br>
Alice, Bob, and Charlie for the Go team</p>

announcement body text:
Hello gophers,

We have just released Go versions 1.18.1 and 1.17.8, minor point releases.

View the release notes for more information:
https://go.dev/doc/devel/release#go1.18.1

You can download binary and source distributions from the Go website:
https://go.dev/dl/

To compile from source using a Git clone, update to the release with
git checkout go1.18.1 and build as usual.

Thanks to everyone who contributed to the releases.

Cheers,
Alice, Bob, and Charlie for the Go team` + "\n",
		},
		// Just one test case is enough, since TestAnnouncementMail
		// has very thorough coverage for all release types.
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			annMail := MailHeader{
				From: mail.Address{Address: "from-address@golang.test"},
				To:   mail.Address{Address: "to-address@golang.test"},
			}
			tasks := AnnounceMailTasks{
				SendMail: func(h MailHeader, c mailContent) error {
					if diff := cmp.Diff(annMail, h); diff != "" {
						t.Errorf("mail header mismatch (-want +got):\n%s", diff)
					}
					if diff := cmp.Diff(tc.want.Subject, c.Subject); diff != "" {
						t.Errorf("mail subject mismatch (-want +got):\n%s", diff)
					}
					return nil
				},
				AnnounceMailHeader: annMail,
			}
			var buf bytes.Buffer
			ctx := &workflow.TaskContext{Context: context.Background(), Logger: fmtWriter{&buf}}
			sentMail, err := tasks.AnnounceRelease(ctx, tc.versions, tc.security, tc.names)
			if err != nil {
				t.Fatal("task function returned non-nil error:", err)
			}
			if diff := cmp.Diff(tc.want, sentMail); diff != "" {
				t.Errorf("sent mail mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantLog, buf.String()); diff != "" {
				t.Errorf("log mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFindGoogleGroupsThread(t *testing.T) {
	if testing.Short() {
		t.Skip("not running test that uses internet in short mode")
	}

	threadURL, err := findGoogleGroupsThread(&workflow.TaskContext{
		Context: context.Background(),
	}, "[security] Go 1.18.3 and Go 1.17.11 are released")
	if err != nil {
		// Note: These test failures are only actionable if the error is not
		// a transient network one.
		t.Fatalf("findGoogleGroupsThread returned a non-nil error: %v", err)
	}
	// Just log the threadURL since we can't rely on stable output.
	// This test is mostly for debugging if we need to.
	t.Logf("threadURL: %q\n", threadURL)
}

func TestMarkdownToText(t *testing.T) {
	const in = `Hello gophers,

This is a simple Markdown document that exercises
a limited set of features used in email templates.

There may be security fixes following the [security policy](https://go.dev/security):

-	abc: Read hangs on extremely large input

	On an operating system, ` + "`Read`" + ` will hang indefinitely if
	the buffer size is larger than 1 << 64 - 1 bytes.

	Thanks to Gopher A for reporting the issue.

	This is CVE-123 and Go issue https://go.dev/issue/123.

-	xyz: Clean("X") returns "Y" when Z

	Some description of the problem here.

	Markdown allows one to use backslash escapes, like \_underscore\_
	or \*literal asterisks\*, so we might encounter that.

View release notes:
https://go.dev/doc/devel/release#go1.18.3

You can download binaries:
https://go.dev/dl/

To builds from source, use
` + "`git checkout`" + `.

An easy way to try go1.19beta1
is by using the go command:
$ go install example.org@latest
$ example download

That's all for now.
`
	_, got, err := renderMarkdown(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}

	const want = `Hello gophers,

This is a simple Markdown document that exercises
a limited set of features used in email templates.

There may be security fixes following the security policy <https://go.dev/security>:

-	abc: Read hangs on extremely large input

	On an operating system, Read will hang indefinitely if
	the buffer size is larger than 1 << 64 - 1 bytes.

	Thanks to Gopher A for reporting the issue.

	This is CVE-123 and Go issue https://go.dev/issue/123.

-	xyz: Clean("X") returns "Y" when Z

	Some description of the problem here.

	Markdown allows one to use backslash escapes, like \_underscore\_
	or \*literal asterisks\*, so we might encounter that.

View release notes:
https://go.dev/doc/devel/release#go1.18.3

You can download binaries:
https://go.dev/dl/

To builds from source, use
git checkout.

An easy way to try go1.19beta1
is by using the go command:
$ go install example.org@latest
$ example download

That's all for now.
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("plain text rendering mismatch (-want +got):\n%s", diff)
	}
}
