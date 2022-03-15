// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/build/internal/workflow"
)

func TestTweetRelease(t *testing.T) {
	if testing.Short() {
		// This test is useful when modifying the tweet text and image templates,
		// but don't run it in -short mode since tweetImage involves making some
		// HTTP GET requests to the internet.
		t.Skip("skipping test that hits go.dev/dl/?mode=json read-only API in -short mode")
	}

	tests := [...]struct {
		name    string
		taskFn  func(*workflow.TaskContext, ReleaseTweet, ExternalConfig) (string, error)
		in      ReleaseTweet
		wantLog string
	}{
		{
			name:   "minor",
			taskFn: TweetMinorRelease,
			in: ReleaseTweet{
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

⬇️ Download: https://go.dev/dl/#go1.17.1

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
			taskFn: TweetBetaRelease,
			in: ReleaseTweet{
				Version:      "go1.17beta1",
				Announcement: "https://groups.google.com/g/golang-announce/c/i4EliPDV9Ok/m/MxA-nj53AAAJ",
				RandomSeed:   678,
			},
			wantLog: `tweet text:
⚡️ Go 1.17 Beta 1 is released!

⚙️ Try it! File bugs! https://go.dev/issue/new

🗣 Announcement: https://groups.google.com/g/golang-announce/c/i4EliPDV9Ok/m/MxA-nj53AAAJ

📦 Download: https://go.dev/dl/#go1.17beta1

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
			taskFn: TweetRCRelease,
			in: ReleaseTweet{
				Version:      "go1.17rc2",
				Announcement: "https://groups.google.com/g/golang-announce/c/yk30ovJGXWY/m/p9uUnKbbBQAJ",
				RandomSeed:   456,
			},
			wantLog: `tweet text:
🎉 Go 1.17 Release Candidate 2 is released!

🏖 Run it in dev! Run it in prod! File bugs! https://go.dev/issue/new

🔈 Announcement: https://groups.google.com/g/golang-announce/c/yk30ovJGXWY/m/p9uUnKbbBQAJ

📦 Download: https://go.dev/dl/#go1.17rc2

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
			taskFn: TweetMajorRelease,
			in: ReleaseTweet{
				Version:    "go1.17",
				Security:   "Includes a super duper security fix (CVE-123).",
				RandomSeed: 123,
			},
			wantLog: `tweet text:
🥳 Go 1.17 is released!

🔐 Security: Includes a super duper security fix (CVE-123).

📝 Release notes: https://go.dev/doc/go1.17

📦 Download: https://go.dev/dl/#go1.17

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
			ctx := &workflow.TaskContext{Context: context.Background(), Logger: fmtWriter{&buf}}
			tweetURL, err := tc.taskFn(ctx, tc.in, ExternalConfig{DryRun: true})
			if err != nil {
				t.Fatal("got a non-nil error:", err)
			}
			if got, want := tweetURL, "(dry-run)"; got != want {
				t.Errorf("unexpected tweetURL: got = %q, want %q", got, want)
			}
			if got, want := buf.String(), tc.wantLog; got != want {
				t.Errorf("unexpected log:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

type fmtWriter struct{ w io.Writer }

func (f fmtWriter) Printf(format string, v ...interface{}) {
	fmt.Fprintf(f.w, format, v...)
}

func TestPostTweet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("upload.twitter.com/1.1/media/upload.json", func(w http.ResponseWriter, req *http.Request) {
		if got, want := req.Method, http.MethodPost; got != want {
			t.Errorf("media/upload: got method %s, want %s", got, want)
			return
		}
		if got, want := req.FormValue("media_category"), "tweet_image"; got != want {
			t.Errorf("media/upload: got media_category=%q, want %q", got, want)
		}
		f, hdr, err := req.FormFile("media")
		if err != nil {
			t.Errorf("media/upload: error getting image file: %v", err)
			return
		}
		if got, want := hdr.Filename, "image.png"; got != want {
			t.Errorf("media/upload: got file name=%q, want %q", got, want)
		}
		if got, want := mustRead(f), "image-png-bytes"; got != want {
			t.Errorf("media/upload: got file content=%q, want %q", got, want)
			return
		}
		mustWrite(w, `{"media_id_string": "media-123"}`)
	})
	mux.HandleFunc("api.twitter.com/1.1/statuses/update.json", func(w http.ResponseWriter, req *http.Request) {
		if got, want := req.Method, http.MethodPost; got != want {
			t.Errorf("statuses/update: got method %s, want %s", got, want)
			return
		}
		if got, want := req.FormValue("status"), "tweet-text"; got != want {
			t.Errorf("statuses/update: got status=%q, want %q", got, want)
		}
		if got, want := req.FormValue("media_ids"), "media-123"; got != want {
			t.Errorf("statuses/update: got media_ids=%q, want %q", got, want)
		}
		mustWrite(w, `{"id_str": "tweet-123", "user": {"screen_name": "golang"}}`)
	})
	httpClient := &http.Client{Transport: localRoundTripper{mux}}

	tweetURL, err := postTweet(httpClient, "tweet-text", []byte("image-png-bytes"))
	if err != nil {
		t.Fatal("postTweet:", err)
	}
	if got, want := tweetURL, "https://twitter.com/golang/status/tweet-123"; got != want {
		t.Errorf("got tweetURL=%q, want %q", got, want)
	}
}

// localRoundTripper is an http.RoundTripper that executes HTTP transactions
// by using handler directly, instead of going over an HTTP connection.
type localRoundTripper struct {
	handler http.Handler
}

func (l localRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	l.handler.ServeHTTP(w, req)
	return w.Result(), nil
}

func mustRead(r io.Reader) string {
	b, err := io.ReadAll(r)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func mustWrite(w io.Writer, s string) {
	_, err := io.WriteString(w, s)
	if err != nil {
		panic(err)
	}
}
