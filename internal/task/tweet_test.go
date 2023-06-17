// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/build/internal/workflow"
)

func TestTweetRelease(t *testing.T) {
	tests := [...]struct {
		name         string
		published    []Published
		security     string
		announcement string
		randomSeed   int64
		wantLog      string
	}{
		{
			name: "minor",
			published: []Published{
				{Version: "go1.17.1", Files: []WebsiteFile{{
					OS: "linux", Arch: "arm64",
					Filename: "go1.17.1.linux-arm64.tar.gz", Size: 102606384, Kind: "archive"}},
				},
				{Version: "go1.16.8"},
			},
			security:     "Includes security fixes for A and B.",
			announcement: "https://groups.google.com/g/golang-announce/c/dx9d7IOseHw/m/KNH37k37AAAJ",
			randomSeed:   234,
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
			name: "minor-solo",
			published: []Published{{Version: "go1.11.1", Files: []WebsiteFile{{
				OS: "darwin", Arch: "amd64",
				Filename: "go1.11.1.darwin-amd64.tar.gz", Size: 124181190, Kind: "archive"}},
			}},
			announcement: "https://groups.google.com/g/golang-announce/c/pFXKAfoVJqw",
			randomSeed:   23,
			wantLog: `tweet text:
🎆 Go 1.11.1 is released!

📣 Announcement: https://groups.google.com/g/golang-announce/c/pFXKAfoVJqw

📦 Download: https://go.dev/dl/#go1.11.1

#golang
tweet image:
$ go install golang.org/dl/go1.11.1@latest
$ go1.11.1 download
Downloaded   0.0% (        0 / 124181190 bytes) ...
Downloaded  50.0% ( 62090595 / 124181190 bytes) ...
Downloaded 100.0% (124181190 / 124181190 bytes)
Unpacking go1.11.1.darwin-amd64.tar.gz ...
Success. You may now run 'go1.11.1'
$ go1.11.1 version
go version go1.11.1 darwin/amd64` + "\n",
		},
		{
			name: "beta",
			published: []Published{{Version: "go1.17beta1", Files: []WebsiteFile{{
				OS: "darwin", Arch: "amd64",
				Filename: "go1.17beta1.darwin-amd64.tar.gz", Size: 135610703, Kind: "archive"}},
			}},
			announcement: "https://groups.google.com/g/golang-announce/c/i4EliPDV9Ok/m/MxA-nj53AAAJ",
			randomSeed:   678,
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
			name: "rc",
			published: []Published{{Version: "go1.17rc2", Files: []WebsiteFile{{
				OS: "windows", Arch: "arm64",
				Filename: "go1.17rc2.windows-arm64.zip", Size: 116660997, Kind: "archive"}},
			}},
			announcement: "https://groups.google.com/g/golang-announce/c/yk30ovJGXWY/m/p9uUnKbbBQAJ",
			randomSeed:   456,
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
			name: "major",
			published: []Published{{Version: "go1.17", Files: []WebsiteFile{{
				OS: "freebsd", Arch: "amd64",
				Filename: "go1.17.freebsd-amd64.tar.gz", Size: 133579378, Kind: "archive"}},
			}},
			security:   "Includes a super duper security fix (CVE-123).",
			randomSeed: 123,
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
			tweetURL, err := (TweetTasks{RandomSeed: tc.randomSeed}).TweetRelease(ctx, tc.published, tc.security, tc.announcement)
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

var updateFlag = flag.Bool("update", false, "Update golden files.")

func TestDrawTerminal(t *testing.T) {
	got, err := drawTerminal(`$ go install golang.org/dl/go1.18beta1@latest
$ go1.18beta1 download
Downloaded   0.0% (        0 / 111109966 bytes) ...
Downloaded  50.0% ( 55554983 / 111109966 bytes) ...
Downloaded 100.0% (111109966 / 111109966 bytes)
Unpacking go1.18beta1.linux-s390x.tar.gz ...
Success. You may now run 'go1.18beta1'
$ go1.18beta1 version
go version go1.18beta1 linux/s390x`)
	if err != nil {
		t.Fatalf("drawTerminal: got error=%v, want nil", err)
	}
	if *updateFlag {
		encodePNG(t, filepath.Join("testdata", "terminal.png"), got)
		return
	}
	want := decodePNG(t, filepath.Join("testdata", "terminal.png"))
	if !got.Bounds().Eq(want.Bounds()) {
		t.Fatalf("drawTerminal: got image bounds=%v, want %v", got.Bounds(), want.Bounds())
	}
	diff := func(a, b uint32) uint64 {
		if a < b {
			return uint64(b - a)
		}
		return uint64(a - b)
	}
	var total uint64
	for y := 0; y < want.Bounds().Dy(); y++ {
		for x := 0; x < want.Bounds().Dx(); x++ {
			r0, g0, b0, a0 := got.At(x, y).RGBA()
			r1, g1, b1, a1 := want.At(x, y).RGBA()
			const D = 0xffff * 20 / 100 // Diff threshold of 20% for RGB color components.
			if diff(r0, r1) > D || diff(g0, g1) > D || diff(b0, b1) > D || a0 != a1 {
				t.Errorf("at (%d, %d):\n got RGBA %v\nwant RGBA %v", x, y, got.At(x, y), want.At(x, y))
			}
			total += diff(r0, r1) + diff(g0, g1) + diff(b0, b1)
		}
	}
	if testing.Verbose() {
		t.Logf("average pixel color diff: %v%%", 100*float64(total)/float64(0xffff*want.Bounds().Dx()*want.Bounds().Dy()))
	}
}

func encodePNG(t *testing.T, name string, m image.Image) {
	t.Helper()
	var buf bytes.Buffer
	err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, m)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(name, buf.Bytes(), 0644)
	if err != nil {
		t.Fatal(err)
	}
}

func decodePNG(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return m
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
	cl := realTwitterClient{twitterAPI: &http.Client{Transport: localRoundTripper{mux}}}

	tweetURL, err := cl.PostTweet("tweet-text", []byte("image-png-bytes"))
	if err != nil {
		t.Fatal("PostTweet:", err)
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
