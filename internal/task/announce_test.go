// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"errors"
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
	_, err := (AnnounceMailTasks{}).AnnounceRelease(&workflow.TaskContext{Context: ctx}, KindMinor, []Published{{Version: "go1.18.1"}, {Version: "go1.17.8"}}, nil, nil)
	if err == nil {
		t.Errorf("want non-nil error")
	} else if !strings.HasPrefix(err.Error(), "insufficient time") {
		t.Errorf("want error that starts with 'insufficient time' instead of: %s", err)
	}
}

func TestAnnouncementMail(t *testing.T) {
	tests := [...]struct {
		name        string
		in          any
		wantSubject string
	}{
		{
			name: "announce-minor",
			in: releaseAnnouncement{
				Kind:             KindMinor,
				Version:          "go1.18.1",
				SecondaryVersion: "go1.17.9",
				Names:            []string{"Alice", "Bob", "Charlie"},
			},
			wantSubject: "Go 1.18.1 and Go 1.17.9 are released",
		},
		{
			name: "announce-minor-with-security",
			in: releaseAnnouncement{
				Kind:             KindMinor,
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
			name: "announce-minor-solo",
			in: releaseAnnouncement{
				Kind:     KindMinor,
				Version:  "go1.11.1",
				Security: []string{"abc: security fix 1", "xyz: security fix 2"},
				Names:    []string{"Alice"},
			},
			wantSubject: "[security] Go 1.11.1 is released",
		},
		{
			name: "announce-beta",
			in: releaseAnnouncement{
				Kind:    KindBeta,
				Version: "go1.19beta5",
			},
			wantSubject: "Go 1.19 Beta 5 is released",
		},
		{
			name: "announce-rc",
			in: releaseAnnouncement{
				Kind:    KindRC,
				Version: "go1.23rc1",
			},
			wantSubject: "Go 1.23 Release Candidate 1 is released",
		},
		{
			name: "announce-major",
			in: releaseAnnouncement{
				Kind:    KindMajor,
				Version: "go1.21.0",
			},
			wantSubject: "Go 1.21.0 is released",
		},

		{
			name: "pre-announce-minor",
			in: releasePreAnnouncement{
				Target:           Date{2022, time.July, 12},
				Version:          "go1.18.4",
				SecondaryVersion: "go1.17.12",
				Security:         "the standard library",
				CVEs:             []string{"cve-1234", "cve-5678"},
				Names:            []string{"Alice"},
			},
			wantSubject: "[security] Go 1.18.4 and Go 1.17.12 pre-announcement",
		},
		{
			name: "pre-announce-minor-solo",
			in: releasePreAnnouncement{
				Target:   Date{2022, time.July, 12},
				Version:  "go1.18.4",
				Security: "the toolchain",
				CVEs:     []string{"cve-1234", "cve-5678"},
				Names:    []string{"Alice", "Bob"},
			},
			wantSubject: "[security] Go 1.18.4 pre-announcement",
		},
		{
			name: "gopls-pre-announce",
			in: goplsPrereleaseAnnouncement{
				Version: "v0.16.2-pre.1",
				Branch:  "gopls-release-branch.0.16",
				Commit:  "abc123def456ghi789",
				Issue:   12345,
			},
			wantSubject: "Gopls v0.16.2-pre.1 is released",
		},
		{
			name: "gopls-announce",
			in: goplsReleaseAnnouncement{
				Version: "v0.16.2",
				Branch:  "gopls-release-branch.0.16",
				Commit:  "abc123def456ghi789",
			},
			wantSubject: "Gopls v0.16.2 is released",
		},
		{
			name: "vscode-go-announce",
			in: vscodeGoReleaseAnnouncement{
				Version: "v0.44.2",
				Branch:  "release-v0.44",
				Commit:  "abc123def456ghi789",
			},
			wantSubject: "VSCode-Go extension v0.44.2 is released",
		},
		{
			name: "vscode-go-pre-announce",
			in: vscodeGoPrereleaseAnnouncement{
				Version: "v0.44.2-rc.1",
				Branch:  "release-v0.44",
				Commit:  "abc123def456ghi789",
				Issue:   12345,
			},
			wantSubject: "VSCode-Go extension v0.44.2-rc.1 is released",
		},
		{
			name: "vscode-go-insider-announce",
			in: vscodeGoInsiderAnnouncement{
				Version:       "v0.43.2",
				Commit:        "abc123def456ghi789",
				StableVersion: "v0.44.0",
			},
			wantSubject: "VSCode-Go extension v0.43.2 is released",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := announcementMail(tc.in)
			if err != nil {
				t.Fatal("announcementMail returned non-nil error:", err)
			}
			if *updateFlag {
				writeTestdataFile(t, tc.name+".html", []byte(m.BodyHTML))
				writeTestdataFile(t, tc.name+".txt", []byte(m.BodyText))
				return
			}
			if diff := cmp.Diff(tc.wantSubject, m.Subject); diff != "" {
				t.Errorf("subject mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(testdataFile(t, tc.name+".html"), m.BodyHTML); diff != "" {
				t.Errorf("body HTML mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(testdataFile(t, tc.name+".txt"), m.BodyText); diff != "" {
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
		name         string
		kind         ReleaseKind
		published    []Published
		security     []string
		coordinators []string
		want         SentMail
		wantLog      string
	}{
		{
			name:         "minor",
			kind:         KindMinor,
			published:    []Published{{Version: "go1.18.1"}, {Version: "go1.17.8"}}, // Intentionally not 1.17.9 so the real email doesn't get in the way.
			coordinators: []string{"heschi", "dmitshur"},
			want:         SentMail{Subject: "Go 1.18.1 and Go 1.17.8 are released"},
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
Heschi and Dmitri for the Go team</p>

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
Heschi and Dmitri for the Go team` + "\n",
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
				SendMail: func(h MailHeader, c MailContent) error {
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
			sentMail, err := tasks.AnnounceRelease(ctx, tc.kind, tc.published, tc.security, tc.coordinators)
			if err != nil {
				if fe := (fetchError{}); errors.As(err, &fe) && fe.PossiblyRetryable {
					t.Skip("test run produced no actionable signal due to a transient network error:", err) // See go.dev/issue/60541.
				}
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

func TestPreAnnounceRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("not running test that uses internet in short mode")
	}

	tests := [...]struct {
		name         string
		versions     []string
		target       Date
		security     string
		cves         []string
		coordinators []string
		want         SentMail
		wantLog      string
	}{
		{
			name:         "minor",
			versions:     []string{"go1.18.4", "go1.17.11"}, // Intentionally not 1.17.12 so the real email doesn't get in the way.
			target:       Date{2022, time.July, 12},
			security:     "the standard library",
			cves:         []string{"cve-2022-1234", "cve-2023-1234"},
			coordinators: []string{"tatiana"},
			want:         SentMail{Subject: "[security] Go 1.18.4 and Go 1.17.11 pre-announcement"},
			wantLog: `pre-announcement subject: [security] Go 1.18.4 and Go 1.17.11 pre-announcement

pre-announcement body HTML:
<p>Hello gophers,</p>
<p>We plan to issue Go 1.18.4 and Go 1.17.11 during US business hours on Tuesday, July 12.</p>
<p>These minor releases include PRIVATE security fixes to the standard library, covering the following CVEs:</p>
<ul>
<li>cve-2022-1234</li>
<li>cve-2023-1234</li>
</ul>
<p>Following our security policy, this is the pre-announcement of those releases.</p>
<p>Thanks,<br>
Tatiana for the Go team</p>

pre-announcement body text:
Hello gophers,

We plan to issue Go 1.18.4 and Go 1.17.11 during US business hours on Tuesday, July 12.

These minor releases include PRIVATE security fixes to the standard library, covering the following CVEs:

-	cve-2022-1234

-	cve-2023-1234

Following our security policy, this is the pre-announcement of those releases.

Thanks,
Tatiana for the Go team` + "\n",
		},
		// TestAnnouncementMail has additional coverage.
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tasks := AnnounceMailTasks{
				SendMail:    func(h MailHeader, c MailContent) error { return nil },
				testHookNow: func() time.Time { return time.Date(2022, time.July, 7, 0, 0, 0, 0, time.UTC) },
			}
			var buf bytes.Buffer
			ctx := &workflow.TaskContext{Context: context.Background(), Logger: fmtWriter{&buf}}
			sentMail, err := tasks.PreAnnounceRelease(ctx, tc.versions, tc.target, tc.security, tc.cves, tc.coordinators)
			if err != nil {
				if fe := (fetchError{}); errors.As(err, &fe) && fe.PossiblyRetryable {
					t.Skip("test run produced no actionable signal due to a transient network error:", err) // See go.dev/issue/60541.
				}
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
		if fe := (fetchError{}); errors.As(err, &fe) && fe.PossiblyRetryable {
			t.Skip("test run produced no actionable signal due to a transient network error:", err) // See go.dev/issue/60541.
		}
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

		Regular Code Block
		Can
		Be
		Here

	Another paragraph.

	` + "```" + `
	Fenced Code Block
	Can
	Be
	Here
	` + "```" + `

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

` + "```" + `
$ go install example.org@latest
$ example download
` + "```" + `

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

	Regular Code Block
	Can
	Be
	Here

	Another paragraph.

	Fenced Code Block
	Can
	Be
	Here

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
