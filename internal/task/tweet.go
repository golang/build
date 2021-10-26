// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package task

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/esimov/stackblur-go"
	"github.com/golang/freetype/truetype"
	"golang.org/x/build/internal/workflow"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/math/fixed"
)

// ReleaseTweet describes a tweet that announces a Go release.
type ReleaseTweet struct {
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

	// Security is an optional sentence describing security fixes
	// included in this release.
	//
	// The empty string means there are no security fixes to highlight.
	// Past examples:
	// 	• "Includes a security fix for the Wasm port (CVE-2021-38297)."
	// 	• "Includes a security fix for archive/zip (CVE-2021-39293)."
	// 	• "Includes a security fix for crypto/tls (CVE-2021-34558)."
	// 	• "Includes security fixes for archive/zip, net, net/http/httputil, and math/big packages."
	Security string

	// Announcement is the announcement URL.
	//
	// It's applicable to all release types other than major,
	// since major releases point to release notes instead.
	// For example, "https://groups.google.com/g/golang-announce/c/wB1fph5RpsE/m/ZGwOsStwAwAJ".
	Announcement string

	// RandomSeed is the pseudo-random number generator seed to use for presentational
	// choices, such as selecting one out of many available emoji or release archives.
	// The zero value means to use time.Now().UnixNano().
	RandomSeed int64
}

func (r ReleaseTweet) seed() int64 {
	if r.RandomSeed == 0 {
		return time.Now().UnixNano()
	}
	return r.RandomSeed
}

// TweetMinorRelease posts a tweet announcing a minor Go release.
// ErrTweetTooLong is returned if the inputs result in a tweet that's too long.
func TweetMinorRelease(ctx workflow.TaskContext, r ReleaseTweet, dryRun bool) (tweetURL string, _ error) {
	if err := verifyGoVersions(r.Version, r.SecondaryVersion); err != nil {
		return "", err
	}
	if !strings.HasPrefix(r.Announcement, announcementPrefix) {
		return "", fmt.Errorf("announcement URL %q doesn't have the expected prefix %q", r.Announcement, announcementPrefix)
	}

	return tweetRelease(ctx, r, dryRun)
}

// TweetBetaRelease posts a tweet announcing a beta Go release.
// ErrTweetTooLong is returned if the inputs result in a tweet that's too long.
func TweetBetaRelease(ctx workflow.TaskContext, r ReleaseTweet, dryRun bool) (tweetURL string, _ error) {
	if r.SecondaryVersion != "" {
		return "", fmt.Errorf("got 2 Go versions, want 1")
	}
	if err := verifyGoVersions(r.Version); err != nil {
		return "", err
	}
	if !strings.HasPrefix(r.Announcement, announcementPrefix) {
		return "", fmt.Errorf("announcement URL %q doesn't have the expected prefix %q", r.Announcement, announcementPrefix)
	}

	return tweetRelease(ctx, r, dryRun)
}

// TweetRCRelease posts a tweet announcing a Go release candidate.
// ErrTweetTooLong is returned if the inputs result in a tweet that's too long.
func TweetRCRelease(ctx workflow.TaskContext, r ReleaseTweet, dryRun bool) (tweetURL string, _ error) {
	if r.SecondaryVersion != "" {
		return "", fmt.Errorf("got 2 Go versions, want 1")
	}
	if err := verifyGoVersions(r.Version); err != nil {
		return "", err
	}
	if !strings.HasPrefix(r.Announcement, announcementPrefix) {
		return "", fmt.Errorf("announcement URL %q doesn't have the expected prefix %q", r.Announcement, announcementPrefix)
	}

	return tweetRelease(ctx, r, dryRun)
}

const announcementPrefix = "https://groups.google.com/g/golang-announce/c/"

// TweetMajorRelease posts a tweet announcing a major Go release.
// ErrTweetTooLong is returned if the inputs result in a tweet that's too long.
func TweetMajorRelease(ctx workflow.TaskContext, r ReleaseTweet, dryRun bool) (tweetURL string, _ error) {
	if r.SecondaryVersion != "" {
		return "", fmt.Errorf("got 2 Go versions, want 1")
	}
	if err := verifyGoVersions(r.Version); err != nil {
		return "", err
	}
	if r.Announcement != "" {
		return "", fmt.Errorf("major release tweet doesn't accept an accouncement URL")
	}

	return tweetRelease(ctx, r, dryRun)
}

// tweetRelease posts a tweet announcing a Go release.
func tweetRelease(ctx workflow.TaskContext, r ReleaseTweet, dryRun bool) (tweetURL string, _ error) {
	rnd := rand.New(rand.NewSource(r.seed()))

	// Generate tweet text.
	tweetText, err := tweetText(r, rnd)
	if err != nil {
		return "", err
	}
	if log := ctx.Logger; log != nil {
		log.Printf("tweet text:\n%s\n", tweetText)
	}

	// Generate tweet image.
	_, imageText, err := tweetImage(r.Version, rnd)
	if err != nil {
		return "", err
	}
	if log := ctx.Logger; log != nil {
		log.Printf("tweet image:\n%s\n", imageText)
	}

	// Post a tweet using the twitter.com/golang account.
	if dryRun {
		return "(dry-run)", nil
	}
	// TODO(golang.org/issue/47403): Use Twitter API.
	return "", fmt.Errorf("use of twitter API not implemented yet")
}

// tweetText generates the text to use in the announcement
// tweet for release r.
func tweetText(r ReleaseTweet, rnd *rand.Rand) (string, error) {
	// Parse the tweet text template
	// using rnd for emoji selection.
	t, err := template.New("").Funcs(template.FuncMap{
		"emoji": func(category string) (string, error) {
			es, ok := emoji[category]
			if !ok {
				return "", fmt.Errorf("unknown emoji category %q", category)
			}
			return es[rnd.Intn(len(es))], nil
		},
	}).Parse(tweetTextTmpl)
	if err != nil {
		return "", err
	}

	// Pick a template name and populate template data
	// for this type of release.
	var (
		name string
		data interface{}
	)
	if i := strings.Index(r.Version, "beta"); i != -1 { // A beta release.
		name, data = "beta", struct {
			Maj, Beta string
			ReleaseTweet
		}{
			Maj:          r.Version[len("go"):i],
			Beta:         r.Version[i+len("beta"):],
			ReleaseTweet: r,
		}
	} else if i := strings.Index(r.Version, "rc"); i != -1 { // Release Candidate.
		name, data = "rc", struct {
			Maj, RC string
			ReleaseTweet
		}{
			Maj:          r.Version[len("go"):i],
			RC:           r.Version[i+len("rc"):],
			ReleaseTweet: r,
		}
	} else if strings.Count(r.Version, ".") == 1 { // Major release like "go1.X".
		name, data = "major", r
	} else if strings.Count(r.Version, ".") == 2 { // Minor release like "go1.X.Y".
		name, data = "minor", struct {
			Curr, Prev string
			ReleaseTweet
		}{
			Curr:         r.Version[len("go"):],
			Prev:         r.SecondaryVersion[len("go"):],
			ReleaseTweet: r,
		}
	} else {
		return "", fmt.Errorf("unknown version format: %q", r.Version)
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const tweetTextTmpl = `{{define "minor" -}}
{{emoji "release"}} Go {{.Curr}} and {{.Prev}} are released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://golang.org/dl/#{{.Version}}

#golang{{end}}


{{define "beta" -}}
{{emoji "beta-release"}} Go {{.Maj}} Beta {{.Beta}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "try"}} Try it! File bugs! https://golang.org/issue/new

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://golang.org/dl/#{{.Version}}

#golang{{end}}


{{define "rc" -}}
{{emoji "rc-release"}} Go {{.Maj}} Release Candidate {{.RC}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "run"}} Run it in dev! Run it in prod! File bugs! https://golang.org/issue/new

{{emoji "announce"}} Announcement: {{.Announcement}}

{{emoji "download"}} Download: https://golang.org/dl/#{{.Version}}

#golang{{end}}


{{define "major" -}}
{{emoji "release"}} Go {{.Version}} is released!

{{with .Security}}{{emoji "security"}} Security: {{.}}{{"\n\n"}}{{end -}}

{{emoji "notes"}} Release notes: https://golang.org/doc/{{.Version}}

{{emoji "download"}} Download: https://golang.org/dl/#{{.Version}}

#golang{{end}}`

// emoji is an atlas of emoji for different categories.
//
// The more often an emoji is included in a category,
// the more likely it is to be randomly chosen.
var emoji = map[string][]string{
	"release": {
		"🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳",
		"🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉",
		"🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊",
		"🌟", "🌟", "🌟", "🌟", "🌟", "🌟", "🌟", "🌟",
		"🎆", "🎆", "🎆", "🎆", "🎆", "🎆",
		"🆒",
		"🕶",
		"🤯",
		"🧨",
		"💃",
		"🐕",
		"👩🏽‍🔬",
		"🌞",
	},
	"beta-release": {
		"🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪", "🧪",
		"⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️", "⚡️",
		"💥",
	},
	"rc-release": {
		"🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳", "🥳",
		"🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉", "🎉",
		"🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊", "🎊",
		"🌞",
	},
	"security": {
		"🔐", "🔐", "🔐", "🔐", "🔐",
		"🔏", "🔏",
		"🔒",
	},
	"try": {
		"⚙️",
	},
	"run": {
		"🏃‍♀️",
		"🏃‍♂️",
		"🏖",
	},
	"announce": {
		"🗣", "🗣", "🗣", "🗣", "🗣", "🗣",
		"📣", "📣", "📣", "📣", "📣", "📣",
		"📢", "📢", "📢", "📢", "📢", "📢",
		"🔈", "🔈", "🔈", "🔈", "🔈",
		"📡", "📡", "📡", "📡",
		"📰",
	},
	"notes": {
		"📝", "📝", "📝", "📝", "📝",
		"🗒️", "🗒️", "🗒️", "🗒️", "🗒️",
		"📰",
	},
	"download": {
		"⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️", "⬇️",
		"📦", "📦", "📦", "📦", "📦", "📦", "📦", "📦", "📦",
		"🗃",
		"🚚",
	},
}

// tweetImage generates an image to use in the announcement
// tweet for goVersion. It returns the image encoded as PNG,
// and the text displayed in the image.
//
// tweetImage makes an HTTP GET request to the golang.org/dl/?mode=json
// read-only API to select a random release archive to highlight.
func tweetImage(goVersion string, rnd *rand.Rand) (imagePNG []byte, imageText string, _ error) {
	a, err := fetchRandomArchive(goVersion, rnd)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := goCmdTmpl.Execute(&buf, map[string]string{
		"GoVer":    goVersion,
		"GOOS":     a.OS,
		"GOARCH":   a.GOARCH(),
		"Filename": a.Filename,
		"ZeroSize": fmt.Sprintf("%*d", digits(a.Size), 0),
		"HalfSize": fmt.Sprintf("%*d", digits(a.Size), a.Size/2),
		"FullSize": fmt.Sprint(a.Size),
	}); err != nil {
		return nil, "", err
	}
	imageText = buf.String()
	m, err := drawTerminal(imageText)
	if err != nil {
		return nil, "", err
	}
	// Encode the image in PNG format.
	buf.Reset()
	err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, m)
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), imageText, nil
}

var goCmdTmpl = template.Must(template.New("").Parse(`$ go install golang.org/dl/{{.GoVer}}@latest
$ {{.GoVer}} download
Downloaded   0.0% ({{.ZeroSize}} / {{.FullSize}} bytes) ...
Downloaded  50.0% ({{.HalfSize}} / {{.FullSize}} bytes) ...
Downloaded 100.0% ({{.FullSize}} / {{.FullSize}} bytes)
Unpacking {{.Filename}} ...
Success. You may now run '{{.GoVer}}'
$ {{.GoVer}} version
go version {{.GoVer}} {{.GOOS}}/{{.GOARCH}}`))

// digits reports the number of digits in the integer i. i must be non-zero.
func digits(i int64) int {
	var n int
	for ; i != 0; i /= 10 {
		n++
	}
	return n
}

// fetchRandomArchive downloads all release archives for Go version goVer,
// and selects a random archive to showcase in the image that displays
// sample output from the 'go install golang.org/dl/...@latest' command.
func fetchRandomArchive(goVer string, rnd *rand.Rand) (archive golangorgDLFile, _ error) {
	archives, err := fetchReleaseArchives(goVer)
	if err != nil {
		return golangorgDLFile{}, err
	}
	return archives[rnd.Intn(len(archives))], nil
}

func fetchReleaseArchives(goVer string) (archives []golangorgDLFile, _ error) {
	url := "https://golang.org/dl/?mode=json"
	if strings.Contains(goVer, "beta") || strings.Contains(goVer, "rc") ||
		goVer == "go1.17" || goVer == "go1.17.1" /* For TestTweetRelease. */ {

		url += "&include=all"
	}
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200 OK status code: %v", resp.Status)
	} else if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		return nil, fmt.Errorf("got Content-Type %q, want %q", ct, "application/json")
	}
	var releases []golangorgDLRelease
	err = json.NewDecoder(resp.Body).Decode(&releases)
	if err != nil {
		return nil, err
	}
	for _, r := range releases {
		if r.Version != goVer {
			continue
		}
		var archives []golangorgDLFile
		for _, f := range r.Files {
			if f.Kind != "archive" {
				continue
			}
			archives = append(archives, f)
		}
		if len(archives) == 0 {
			return nil, fmt.Errorf("release version %q has 0 archive files", goVer)
		}
		// Return archives.
		return archives, nil
	}
	return nil, fmt.Errorf("release version %q not found", goVer)
}

// golangorgDLRelease represents a release on the golang.org downloads page.
type golangorgDLRelease struct {
	Version string
	Files   []golangorgDLFile
}

// golangorgDLFile represents a file on the golang.org downloads page.
// It should be kept in sync with code in x/build/cmd/release and x/website/internal/dl.
type golangorgDLFile struct {
	Filename string
	OS       string
	Arch     string
	Size     int64
	Kind     string // One of "archive", "installer", "source".
}

func (f golangorgDLFile) GOARCH() string {
	if f.OS == "linux" && f.Arch == "armv6l" {
		return "arm"
	}
	return f.Arch
}

// drawTerminal draws an image of a terminal window
// with the given text displayed.
func drawTerminal(text string) (image.Image, error) {
	// Load font from TTF data.
	f, err := truetype.Parse(gomono.TTF)
	if err != nil {
		return nil, err
	}

	// Keep image width within 900 px, so that Twitter doesn't convert it to a lossy JPEG.
	// See https://twittercommunity.com/t/upcoming-changes-to-png-image-support/118695.
	const width, height = 900, 520
	m := image.NewNRGBA(image.Rect(0, 0, width, height))

	// Background.
	draw.Draw(m, m.Bounds(), image.NewUniform(gopherBlue), image.Point{}, draw.Src)

	// Shadow.
	draw.DrawMask(m, m.Bounds(), image.NewUniform(shadowColor), image.Point{},
		roundedRect(image.Rect(50, 80, width-50, height-80).Add(image.Point{Y: 20}), 10), image.Point{}, draw.Over)

	// Blur.
	m = stackblur.Process(m, 80).(*image.NRGBA)

	// Terminal.
	draw.DrawMask(m, m.Bounds(), image.NewUniform(terminalColor), image.Point{},
		roundedRect(image.Rect(50, 80, width-50, height-80), 10), image.Point{}, draw.Over)

	// Text.
	d := font.Drawer{Dst: m, Src: image.White, Face: truetype.NewFace(f, &truetype.Options{Size: 24})}
	const lineHeight = 32
	for n, line := range strings.Split(text, "\n") {
		d.Dot = fixed.P(80, 135+n*lineHeight)
		d.DrawString(line)
	}

	return m, nil
}

// roundedRect returns a rounded rectangle with the specified border radius.
func roundedRect(r image.Rectangle, borderRadius int) image.Image {
	return roundedRectangle{
		r:  r,
		i:  r.Inset(borderRadius),
		br: borderRadius,
	}
}

type roundedRectangle struct {
	r  image.Rectangle // Outer bounds.
	i  image.Rectangle // Inner bounds, border radius away from outer.
	br int             // Border radius.
}

func (roundedRectangle) ColorModel() color.Model   { return color.Alpha16Model }
func (r roundedRectangle) Bounds() image.Rectangle { return r.r }
func (r roundedRectangle) At(x, y int) color.Color {
	switch {
	case x < r.i.Min.X && y < r.i.Min.Y:
		return circle(x-r.i.Min.X, y-r.i.Min.Y, r.br)
	case x > r.i.Max.X-1 && y < r.i.Min.Y:
		return circle(x-(r.i.Max.X-1), y-r.i.Min.Y, r.br)
	case x < r.i.Min.X && y > r.i.Max.Y-1:
		return circle(x-r.i.Min.X, y-(r.i.Max.Y-1), r.br)
	case x > r.i.Max.X-1 && y > r.i.Max.Y-1:
		return circle(x-(r.i.Max.X-1), y-(r.i.Max.Y-1), r.br)
	default:
		return color.Opaque
	}
}
func circle(x, y, r int) color.Alpha16 {
	xxyy := float64(x)*float64(x) + float64(y)*float64(y)
	if xxyy > float64((r+1)*(r+1)) {
		return color.Transparent
	} else if xxyy > float64(r*r) {
		return color.Alpha16{uint16(0xFFFF * (1 - math.Sqrt(xxyy) - float64(r)))}
	}
	return color.Opaque
}

var (
	// gopherBlue is the Gopher Blue primary color from the Go color palette.
	//
	// Reference: https://golang.org/s/brandbook.
	gopherBlue = color.NRGBA{0, 173, 216, 255} // #00add8.

	// terminalColor is the color used as the terminal color.
	terminalColor = color.NRGBA{52, 61, 70, 255} // #343d46.

	// shadowColor is the color used as the shadow color.
	shadowColor = color.NRGBA{0, 0, 0, 140} // #0000008c.
)
