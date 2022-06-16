// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestAnnouncementMail(t *testing.T) {
	tests := [...]struct {
		name        string
		in          ReleaseAnnouncement
		wantSubject string
	}{
		{
			name: "minor",
			in: ReleaseAnnouncement{
				Version:          "go1.18.1",
				SecondaryVersion: "go1.17.9",
				Names:            []string{"Alice", "Bob", "Charlie"},
			},
			wantSubject: "Go 1.18.1 and Go 1.17.9 are released",
		},
		{
			name: "minor-with-security",
			in: ReleaseAnnouncement{
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
			name: "beta",
			in: ReleaseAnnouncement{
				Version: "go1.19beta5",
			},
			wantSubject: "Go 1.19 Beta 5 is released",
		},
		{
			name: "rc",
			in: ReleaseAnnouncement{
				Version: "go1.19rc6",
			},
			wantSubject: "Go 1.19 Release Candidate 6 is released",
		},
		{
			name: "major",
			in: ReleaseAnnouncement{
				Version: "go1.19",
			},
			wantSubject: "Go 1.19 is released",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := announcementMail(tc.in)
			if err != nil {
				t.Skip("announcementMail is not implemented yet") // TODO(go.dev/issue/47405): Implement.
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
