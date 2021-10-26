// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"golang.org/x/build/internal/task"
	"golang.org/x/build/internal/workflow"
)

func TestTweetRelease(t *testing.T) {
	if testing.Short() {
		// This test is useful when modifying the tweet text and image templates,
		// but don't run it in -short mode since tweetImage involves making some
		// HTTP GET requests to the internet.
		t.Skip("skipping test that hits golang.org/dl/?mode=json read-only API in -short mode")
	}

	tests := [...]struct {
		name    string
		taskFn  func(workflow.TaskContext, task.ReleaseTweet, bool) (string, error)
		in      task.ReleaseTweet
		wantLog string
	}{
		{
			name:   "minor",
			taskFn: task.TweetMinorRelease,
			in: task.ReleaseTweet{
				Version:          "go1.17.1",
				SecondaryVersion: "go1.16.8",
				Security:         "Includes security fixes for A and B.",
				Announcement:     "https://groups.google.com/g/golang-announce/c/dx9d7IOseHw/m/KNH37k37AAAJ",
				RandomSeed:       234,
			},
			wantLog: `tweet text:
🎊 Go 1.17.1 and 1.16.8 are released!

🔐 Security: Includes security fixes for A and B.

📢 Announcement: https://groups.google.com/g/golang-announce/c/dx9d7IOseHw/m/KNH37k37AAAJ

⬇️ Download: https://golang.org/dl/#go1.17.1

#golang
tweet image:
$ go install golang.org/dl/go1.17.1@latest
$ go1.17.1 download
Downloaded   0.0% (        0 / 102606384 bytes) ...
Downloaded  50.0% ( 51303192 / 102606384 bytes) ...
Downloaded 100.0% (102606384 / 102606384 bytes)
Unpacking go1.17.1.linux-arm64.tar.gz ...
Success. You may now run 'go1.17.1'
$ go1.17.1 version
go version go1.17.1 linux/arm64` + "\n",
		},
		{
			name:   "beta",
			taskFn: task.TweetBetaRelease,
			in: task.ReleaseTweet{
				Version:      "go1.17beta1",
				Announcement: "https://groups.google.com/g/golang-announce/c/i4EliPDV9Ok/m/MxA-nj53AAAJ",
				RandomSeed:   678,
			},
			wantLog: `tweet text:
⚡️ Go 1.17 Beta 1 is released!

⚙️ Try it! File bugs! https://golang.org/issue/new

🗣 Announcement: https://groups.google.com/g/golang-announce/c/i4EliPDV9Ok/m/MxA-nj53AAAJ

📦 Download: https://golang.org/dl/#go1.17beta1

#golang
tweet image:
$ go install golang.org/dl/go1.17beta1@latest
$ go1.17beta1 download
Downloaded   0.0% (        0 / 135610703 bytes) ...
Downloaded  50.0% ( 67805351 / 135610703 bytes) ...
Downloaded 100.0% (135610703 / 135610703 bytes)
Unpacking go1.17beta1.darwin-amd64.tar.gz ...
Success. You may now run 'go1.17beta1'
$ go1.17beta1 version
go version go1.17beta1 darwin/amd64` + "\n",
		},
		{
			name:   "rc",
			taskFn: task.TweetRCRelease,
			in: task.ReleaseTweet{
				Version:      "go1.17rc2",
				Announcement: "https://groups.google.com/g/golang-announce/c/yk30ovJGXWY/m/p9uUnKbbBQAJ",
				RandomSeed:   456,
			},
			wantLog: `tweet text:
🎉 Go 1.17 Release Candidate 2 is released!

🏖 Run it in dev! Run it in prod! File bugs! https://golang.org/issue/new

🔈 Announcement: https://groups.google.com/g/golang-announce/c/yk30ovJGXWY/m/p9uUnKbbBQAJ

📦 Download: https://golang.org/dl/#go1.17rc2

#golang
tweet image:
$ go install golang.org/dl/go1.17rc2@latest
$ go1.17rc2 download
Downloaded   0.0% (        0 / 116660997 bytes) ...
Downloaded  50.0% ( 58330498 / 116660997 bytes) ...
Downloaded 100.0% (116660997 / 116660997 bytes)
Unpacking go1.17rc2.windows-arm64.zip ...
Success. You may now run 'go1.17rc2'
$ go1.17rc2 version
go version go1.17rc2 windows/arm64` + "\n",
		},
		{
			name:   "major",
			taskFn: task.TweetMajorRelease,
			in: task.ReleaseTweet{
				Version:    "go1.17",
				Security:   "Includes a super duper security fix (CVE-123).",
				RandomSeed: 123,
			},
			wantLog: `tweet text:
🥳 Go go1.17 is released!

🔐 Security: Includes a super duper security fix (CVE-123).

📝 Release notes: https://golang.org/doc/go1.17

📦 Download: https://golang.org/dl/#go1.17

#golang
tweet image:
$ go install golang.org/dl/go1.17@latest
$ go1.17 download
Downloaded   0.0% (        0 / 133579378 bytes) ...
Downloaded  50.0% ( 66789689 / 133579378 bytes) ...
Downloaded 100.0% (133579378 / 133579378 bytes)
Unpacking go1.17.freebsd-amd64.tar.gz ...
Success. You may now run 'go1.17'
$ go1.17 version
go version go1.17 freebsd/amd64` + "\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Call the tweet task function in dry-run mode so it
			// doesn't actually try to tweet, but capture its log.
			var buf bytes.Buffer
			ctx := workflow.TaskContext{Context: context.Background(), Logger: fmtWriter{&buf}}
			tweetURL, err := tc.taskFn(ctx, tc.in, true)
			if err != nil {
				t.Fatal("got a non-nil error:", err)
			}
			if got, want := tweetURL, "(dry-run)"; got != want {
				t.Errorf("unexpected tweetURL: got = %q, want %q", got, want)
			}
			if got, want := buf.String(), tc.wantLog; got != want {
				t.Errorf("unexpected log: got = %q, want %q", got, want)
			}
		})
	}
}

type fmtWriter struct{ w io.Writer }

func (f fmtWriter) Printf(format string, v ...interface{}) {
	fmt.Fprintf(f.w, format, v...)
}
