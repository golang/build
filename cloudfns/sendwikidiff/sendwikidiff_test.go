// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sendwikidiff

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestWikiPubSub(t *testing.T) {
	if testing.Short() {
		return
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available")
	}
	oldSendgridKey := sendgridAPIKey
	sendgridAPIKey = "super secret key"
	oldSendEmail := sendEmail
	sendEmail = func(page, diff string) error {
		return nil
	}
	defer func() {
		sendgridAPIKey = oldSendgridKey
		sendEmail = oldSendEmail
		dir := tempRepoDir(repoURL)
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("Could not remove temp repo dir %q: %v", dir, err)
		}
	}()
	m := pubsubMessage{Data: []byte(`{
		"pages": [{
			"page_name": "CodeReviewComments",
			"sha": "962830bfa92499e00a31572b0eeff9efdd68d374"
		}]
	}
  `)}
	if err := HandleWikiChangePubSub(context.Background(), m); err != nil {
		t.Errorf("Unexpected error from HandleWikiChangePubSub: %v", err)
	}
}

func TestEmailDiff(t *testing.T) {
	want := `<p><a href="https://golang.org/wiki/CodeReviewComments">View page</a></p>
<pre style="font-family: monospace,monospace; white-space: pre-wrap;">Diff

Code</pre>
`
	got, err := emailBody("CodeReviewComments", "Diff\n\nCode")
	if err != nil {
		t.Fatalf("emailBody: got unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("Unexpected email body: got %q; want %q", got, want)
	}
}
